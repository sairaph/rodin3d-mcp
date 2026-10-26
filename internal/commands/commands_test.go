package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sairaph/mcp-wizard/command"

	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/fakehyper"
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

func TestParse(t *testing.T) {
	a := parse([]string{"abc", "tier=high", "json", "C:\\x=y\\f.png", "http://127.0.0.1:1/callback?code=a&state=b"})
	if !a.json || a.opts["tier"] != "high" || len(a.pos) != 3 {
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
	if _, code := reg.Dispatch(ctx, "quote", []string{"confirm_geometry", "json"}); code != 0 {
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
