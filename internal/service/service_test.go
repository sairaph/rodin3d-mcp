package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sairaph/mcp-wizard/render"

	"github.com/sairaph/rodin3d-mcp/internal/auth"
	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/rodin"
	"github.com/sairaph/rodin3d-mcp/internal/stage"
)

func TestSortInputNamesIsUploadOrder(t *testing.T) {
	names := []string{"0010.webp", "b.png", "0002.webp", "0000.webp", "a.png", "0001.jpg"}
	sortInputNames(names)
	want := []string{"0000.webp", "0001.jpg", "0002.webp", "0010.webp", "a.png", "b.png"}
	if !slices.Equal(names, want) {
		t.Fatalf("%v, want %v", names, want)
	}
}

func TestCompactValueHidesNestedAddresses(t *testing.T) {
	in := map[string]any{
		"top":    "https://file.example/a.webp?sig=S1",
		"list":   []any{"plain", "http://file.example/b.webp?sig=S2", []any{map[string]any{"deep": "https://file.example/c?sig=S3"}}},
		"map":    map[string]any{"image": "https://file.example/d?sig=S4", "n": 3.0},
		"bulk":   strings.Repeat("A", 300),
		"many":   make([]any, 30),
		"number": 500000.0,
	}
	out := fmt.Sprint(compactValue(in))
	for _, leak := range []string{"S1", "S2", "S3", "S4", "file.example"} {
		if strings.Contains(out, leak) {
			t.Fatalf("%q survives: %s", leak, out)
		}
	}
	for _, want := range []string{"(file address)", "plain", "(300 characters)", "(30 items)", "500000", "3"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q missing: %s", want, out)
		}
	}
	// The input is not changed (it is the live card's settings).
	if in["top"] != "https://file.example/a.webp?sig=S1" {
		t.Error("compactValue changed its input")
	}
}

func TestNotPackedSaysWhy(t *testing.T) {
	cases := []struct {
		front      TaskFront
		msg, hint  string
		notInHints []string
	}{
		{TaskFront{Stage: stage.Material, State: stage.Packed, Step: "TextureRefine", UserState: "DoneBasic"},
			"lists no files", "Pack it again with export_model", nil},
		{TaskFront{Stage: stage.MaterialDraft, State: stage.Packed, Step: "TextureGenerate", UserState: "DoneBasic"},
			"(material draft)", "Pack it again", nil},
		{TaskFront{Stage: stage.GeometryDraft, State: stage.Running, Step: "ModelGenerate", UserState: "Generating"},
			"still working", "Call wait_task", nil},
		{TaskFront{Stage: stage.GeometryDraft, State: stage.Ready, Step: "ModelGenerate", UserState: "DonePreview"},
			"geometry draft", "Confirm the geometry with confirm_geometry", nil},
		{TaskFront{Stage: stage.Geometry, State: stage.Ready, Step: "ModelRefine", UserState: "DonePreview"},
			"(geometry confirmed) has no finished pack", "Pack it with export_model", nil},
	}
	for _, c := range cases {
		e := Classify(notPacked(c.front))
		if e.Code != render.CodeConflict || !strings.Contains(e.Message, c.msg) || !strings.Contains(e.Hint, c.hint) {
			t.Errorf("%s/%s: %s | %s", c.front.Stage, c.front.State, e.Message, e.Hint)
		}
		// Since P2 both tools exist, so no hint sends the user to the web app.
		if strings.Contains(e.Hint, "web app") {
			t.Errorf("%s/%s: hint names the web app although this build packs and confirms: %s", c.front.Stage, c.front.State, e.Hint)
		}
		if strings.Contains(e.Message+e.Hint, "((") || strings.Contains(e.Message+e.Hint, "))") {
			t.Errorf("nested parentheses: %s | %s", e.Message, e.Hint)
		}
	}
}

func TestClassifyFileHostFailures(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("download x: %w", &rodin.HTTPError{Procedure: rodin.FetchPrefix + "file.hyper3d.com", Status: 403}),
		fmt.Errorf("download x: %w", &rodin.HTTPError{Procedure: rodin.FetchPrefix + "file.hyper3d.com", Status: 404}),
		&rodin.NetError{Procedure: rodin.FetchPrefix + "file.hyper3d.com", Err: errors.New("connection reset")},
	} {
		e := Classify(err)
		if e.Code != render.CodeUnavailable || !strings.Contains(e.Hint, "fresh addresses") {
			t.Errorf("%v: %+v", err, e)
		}
	}
	// The API itself answering 403 or 404 keeps its own meaning.
	if e := Classify(&rodin.HTTPError{Procedure: "task/card/x", Status: 403}); e.Code != render.CodeForbidden {
		t.Errorf("API 403: %+v", e)
	}
	if e := Classify(&rodin.HTTPError{Procedure: "task/card/x", Status: 404}); e.Code != render.CodeNotFound {
		t.Errorf("API 404: %+v", e)
	}
}

func TestClassifySignInErrors(t *testing.T) {
	cases := map[error]string{
		auth.ErrNotRedirect:    "still pending",
		auth.ErrStateMismatch:  "latest link",
		auth.ErrNoPendingLogin: "new sign-in link",
		auth.ErrLoginExpired:   "new sign-in link",
	}
	for err, hint := range cases {
		e := Classify(fmt.Errorf("complete: %w", err))
		if e.Code != render.CodeInvalidInput || !strings.Contains(e.Hint, hint) {
			t.Errorf("%v: %+v", err, e)
		}
	}
	// A cut-short paste must never send the agent to a new link first.
	if e := Classify(auth.ErrNotRedirect); strings.HasPrefix(e.Hint, "Call login with no arguments") {
		t.Errorf("paste hint starts a new sign-in: %s", e.Hint)
	}
	// A body-level session refusal is an authentication error.
	e := Classify(&rodin.AuthError{Procedure: "user/get_info", Status: 201, Code: "NO_CURRENT_USER"})
	if e.Code != render.CodeAuth || !strings.Contains(e.Message, "HTTP 201") {
		t.Errorf("body refusal: %+v", e)
	}
}

// The daemon subcommand sweeps leftovers of a hard stop once it holds the
// lock, before it syncs (here it ends at once: nobody is signed in).
func TestRunDaemonSweepsLeftovers(t *testing.T) {
	paths := domain.PathsUnder(t.TempDir())
	old := time.Now().Add(-2 * time.Hour)
	leftover := filepath.Join(paths.Cache, "task", ".fetch-1")
	if err := os.MkdirAll(filepath.Dir(leftover), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(leftover, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(leftover, old, old); err != nil {
		t.Fatal(err)
	}
	svc := New(Options{Paths: paths, BaseURL: "http://127.0.0.1:1/api", Version: "test"})
	defer svc.Close()
	if err := svc.RunDaemon(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatalf("leftover still there: %v", err)
	}
}
