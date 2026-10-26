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
