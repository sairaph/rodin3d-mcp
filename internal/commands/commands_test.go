package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sairaph/mcp-wizard/command"

	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/fakehyper"
	"github.com/sairaph/rodin3d-mcp/internal/fbx"
	"github.com/sairaph/rodin3d-mcp/internal/fbx/fbxtest"
	"github.com/sairaph/rodin3d-mcp/internal/service"
)

func setup(t *testing.T) (*command.Registry, *bytes.Buffer, *bytes.Buffer, *fakehyper.Server, func() *service.Service) {
	t.Helper()
	fake := fakehyper.Standard()
	t.Cleanup(fake.Close)
	paths := domain.PathsUnder(t.TempDir())
	newSvc := func() *service.Service {
		return service.New(service.Options{Paths: paths, BaseURL: fake.Base(), Version: "test"})
	}
	var out, errOut bytes.Buffer
	reg := command.New()
	Register(reg, Env{Service: newSvc, Stdout: &out, Stderr: &errOut})
	return reg, &out, &errOut, fake, newSvc
}

// TestParse: options are key=value with a plain-word key; a path or an
// address holding an = is positional on every operating system, whichever
// system's path form it is.
func TestParse(t *testing.T) {
	pos := []string{"abc", `C:\x=y\f.png`, `\\host\share\a=b.png`, "/tmp/a=b.png", "out/a=b.png",
		"http://127.0.0.1:1/callback?code=a&state=b", "?code=a"}
	raw := append([]string{"tier=high", "json", "Bang_Version = b1", `fbx=C:\x=y\f.fbx`}, pos...)
	a := parse(raw)
	if !a.json || a.opts["tier"] != "high" || a.opts["bang_version"] != "b1" || a.opts["fbx"] != `C:\x=y\f.fbx` ||
		len(a.opts) != 3 || !slices.Equal(a.pos, pos) {
		t.Fatalf("%+v", a)
	}
}

func TestLoginArgs(t *testing.T) {
	cases := []struct {
		raw     []string
		open    bool
		json    bool
		address string
	}{
		{nil, true, false, ""},
		{[]string{"no-browser"}, false, false, ""},
		{[]string{"--no-browser"}, false, false, ""},
		{[]string{"-no-browser", "--json"}, false, true, ""},
		{[]string{"json"}, true, true, ""},
		{[]string{"http://127.0.0.1:53682/callback?code=a&state=b"}, true, false, "http://127.0.0.1:53682/callback?code=a&state=b"},
		// Forms parseRedirect takes that look like a path or an option.
		{[]string{"/callback?code=a&state=b"}, true, false, "/callback?code=a&state=b"},
		{[]string{"code=a&state=b", "no-browser"}, false, false, "code=a&state=b"},
		{[]string{"?error=access_denied&state=b"}, true, false, "?error=access_denied&state=b"},
		// A truncated address is still the address (Complete explains what is missing).
		{[]string{"http://127.0.0.1:53682/callback"}, true, false, "http://127.0.0.1:53682/callback"},
	}
	for _, c := range cases {
		a, open, address, err := loginArgs(c.raw)
		if err != nil || open != c.open || a.json != c.json || address != c.address {
			t.Errorf("%q: open %v json %v address %q err %v", c.raw, open, a.json, address, err)
		}
	}
	for _, bad := range [][]string{{"--foo"}, {"hello"}, {"tier=high"}} {
		if _, _, _, err := loginArgs(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// A bad paste on the command line never starts a new sign-in: the pending
// one (the approval the user just gave) stays usable.
func TestLoginWithABadPasteKeepsThePendingSignIn(t *testing.T) {
	_, out, errOut, fake, newSvc := setup(t)
	ctx := context.Background()
	svc := newSvc()
	_, started, err := svc.Login(ctx, service.LoginInput{})
	if err != nil {
		t.Fatal(err)
	}
	svc.Close()
	redirect, _ := fake.Approve(started.AuthorizeURL)
	paths := svc.Paths()
	before, err := os.ReadFile(paths.PendingLogin)
	if err != nil {
		t.Fatal(err)
	}
	env := Env{Service: newSvc, Stdout: out, Stderr: errOut}
	for _, bad := range [][]string{{"hello"}, {"http://127.0.0.1:53682/callback"}, {redirect[:strings.Index(redirect, "&")]}} {
		errOut.Reset()
		if code := Login(ctx, env, bad); code != 2 {
			t.Fatalf("%q: exit %d %s", bad, code, errOut)
		}
		after, _ := os.ReadFile(paths.PendingLogin)
		if !bytes.Equal(before, after) {
			t.Fatalf("%q replaced the pending sign-in", bad)
		}
	}
	if !strings.Contains(errOut.String(), "still pending") {
		t.Errorf("no paste-again hint: %s", errOut)
	}
	out.Reset()
	if code := Login(ctx, env, []string{redirect}); code != 0 || !strings.Contains(out.String(), "Signed in as owner") {
		t.Fatalf("full address: %d %s %s", code, out, errOut)
	}
}

func TestQRFitsTheTerminal(t *testing.T) {
	qr := "ab\nabcd\nabc\n"
	if fitsWidth(qr, 0) || fitsWidth(qr, 3) || !fitsWidth(qr, 4) || !fitsWidth(qr, 200) {
		t.Fatal("fitsWidth")
	}
	if w := textWidth(qr); w != 4 {
		t.Fatalf("textWidth %d", w)
	}
	// The QR of a real authorize URL (about 390 characters) is about 80
	// columns wide.
	link := "https://api.hyper3d.com/api/grant/oauth/oauth2/authorize?" + strings.Repeat("x", 330)
	if w := textWidth(service.QRText(link)); w < 60 || w > 100 {
		t.Fatalf("QR of the sign-in link is %d columns", w)
	}
}

func TestCommandsAgainstTheFake(t *testing.T) {
	reg, out, errOut, fake, newSvc := setup(t)
	ctx := context.Background()

	// Signed out: an error with the login hint, exit 1.
	if _, code := reg.Dispatch(ctx, "account", nil); code != 1 || !strings.Contains(errOut.String(), "login") {
		t.Fatalf("signed out: %d %s", code, errOut)
	}

	// Sign in through the pasted address, as `login <address>` does.
	svc := newSvc()
	_, started, err := svc.Login(ctx, service.LoginInput{})
	if err != nil {
		t.Fatal(err)
	}
	redirect, _ := fake.Approve(started.AuthorizeURL)
	svc.Close()
	out.Reset()
	if code := Login(ctx, Env{Service: newSvc, Stdout: out, Stderr: errOut}, []string{redirect}); code != 0 {
		t.Fatalf("login: %d %s", code, errOut)
	}

	out.Reset()
	if _, code := reg.Dispatch(ctx, "quote", []string{"confirm_geometry", "tier=extreme-low", "mesh=raw", "json"}); code != 0 {
		t.Fatalf("quote: %d %s", code, errOut)
	}
	var q map[string]any
	if err := json.Unmarshal(out.Bytes(), &q); err != nil || q["quote"].(map[string]any)["credits"] != 0.5 {
		t.Fatalf("quote json: %v %s", err, out)
	}

	out.Reset()
	file := filepath.Join(t.TempDir(), "draft.png")
	if _, code := reg.Dispatch(ctx, "preview", []string{fakehyper.DraftID, "poster", file}); code != 0 {
		t.Fatalf("preview: %d %s", code, errOut)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if _, code := reg.Dispatch(ctx, "download", []string{fakehyper.GeometryID, t.TempDir()}); code != 0 || !strings.Contains(out.String(), "base.obj") {
		t.Fatalf("download: %d %s %s", code, out, errOut)
	}

	errOut.Reset()
	if _, code := reg.Dispatch(ctx, "preview", []string{fakehyper.DraftID, "sideways"}); code != 2 {
		t.Fatalf("bad argument: %d %s", code, errOut)
	}
	out.Reset()
	if _, code := reg.Dispatch(ctx, "permissions", nil); code != 0 || !strings.Contains(out.String(), "generate = deny") {
		t.Fatalf("permissions: %s", out)
	}
	if strings.Contains(out.String()+errOut.String(), fakehyper.SignatureMark) {
		t.Fatal("output carries a signature")
	}
}

// M-2: rig <id> fbx=<file> reskin sends the reskin only, as rig_model's
// reskin does, in both written forms; a word or an option a command does
// not take is refused before anything is sent.
func TestRigReskinAndUnknownArguments(t *testing.T) {
	reg, _, errOut, fake, newSvc := setup(t)
	ctx := context.Background()
	svc := newSvc()
	_, started, err := svc.Login(ctx, service.LoginInput{})
	if err != nil {
		t.Fatal(err)
	}
	redirect, _ := fake.Approve(started.AuthorizeURL)
	if _, err := svc.CompleteLogin(ctx, redirect); err != nil {
		t.Fatal(err)
	}
	cfg := svc.Paths().Config
	svc.Close()
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("[permission]\nrig = \"allow\"\nconfirm_geometry = \"allow\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := fbxtest.Human()
	path := filepath.Join(t.TempDir(), "skeleton.fbx")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	bones, err := fbx.Skeleton(data)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	var parents []int
	var inv [][16]float64
	for _, b := range bones {
		names, parents, inv = append(names, b.Name), append(parents, b.Parent), append(inv, b.Transform.Invert())
	}
	fake.SetFile(fakehyper.PackedID+"/model.glb.rigging.rodinrig", fbxtest.RigFile(names, parents, inv))
	fake.Tasks(fakehyper.PackedID).Rig, fake.Tasks(fakehyper.PackedID).RigSkinning = "custom", "shell"

	rigging := func() int { return len(fake.Bodies("task/rodin_rigging")) }
	for i, form := range []string{"reskin", "reskin=true"} {
		errOut.Reset()
		if _, code := reg.Dispatch(ctx, "rig", []string{fakehyper.PackedID, "fbx=" + path, form, "expected=-1"}); code != 0 || rigging() != i+1 {
			t.Fatalf("%s: exit %d, %d rigging calls: %s", form, code, rigging(), errOut)
		}
		h, _ := fake.Bodies("task/rodin_rigging")[i]["settings"].(map[string]any)["rodinrig_hierarchy"].([]any)
		if len(h) != len(bones) || h[0].(map[string]any)["transform"] == nil {
			t.Fatalf("%s: the call is not the reskin: %v", form, h)
		}
	}
	for _, c := range [][]string{
		{"rig", fakehyper.PackedID, "fbx=" + path, "reskin", "reskin=false", "expected=-1"},
		{"rig", fakehyper.PackedID, "fbx=" + path, "reskn", "expected=-1"},
		{"rig", fakehyper.PackedID, "fbx=" + path, "reskni=true", "expected=-1"},
		{"confirm-geometry", fakehyper.DraftID, "bang_versoin=b1", "expected=-1"},
	} {
		errOut.Reset()
		if _, code := reg.Dispatch(ctx, c[0], c[1:]); code != 2 {
			t.Errorf("%v: exit %d: %s", c, code, errOut)
		}
	}
	if rigging() != 2 || fake.Calls("task/rodin_confirm") != 0 {
		t.Fatalf("a refused command sent a request: %d rigging, %d confirm", rigging(), fake.Calls("task/rodin_confirm"))
	}
}
