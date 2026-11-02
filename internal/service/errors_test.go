package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sairaph/mcp-wizard/render"

	"github.com/sairaph/rodin3d-mcp/internal/rodin"
)

// TestClassifyLocalFileErrors: a file of this computer that cannot be
// opened is not reported as Hyper3D out of reach (a *fs.PathError passes
// as a net.Error): a missing one is not_found and names the path, any
// other names the file and its cause with a local hint. Network errors
// stay network errors.
func TestClassifyLocalFileErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone.glb")
	_, openErr := os.Open(missing)
	if openErr == nil {
		t.Fatal("the missing file opened")
	}
	for _, err := range []error{openErr, fmt.Errorf("read the cached file: %w", openErr)} {
		e := Classify(err)
		if e.Code != render.CodeNotFound || e.Message != "No file at "+missing+"." || e.Hint != "Check the path." {
			t.Errorf("missing file %q: %+v", err, e)
		}
	}

	denied := &fs.PathError{Op: "open", Path: `C:\models\locked.glb`, Err: errors.New("Access is denied.")}
	e := Classify(fmt.Errorf("save the model: %w", denied))
	if e.Code != render.CodeUnavailable || !strings.Contains(e.Message, `C:\models\locked.glb`) || !strings.Contains(e.Message, "Access is denied") ||
		strings.Contains(e.Message, "Hyper3D") || e.Hint != hintLocalFile {
		t.Errorf("unreadable file: %+v", e)
	}

	for name, err := range map[string]error{
		"dial":              &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
		"rodin transport":   &rodin.NetError{Procedure: "task/card", Err: errors.New("connection reset")},
		"deadline":          fmt.Errorf("call: %w", context.DeadlineExceeded),
		"transport of file": &rodin.NetError{Procedure: "upload", Err: denied},
	} {
		if e := Classify(err); e.Code != render.CodeUnavailable || !strings.HasPrefix(e.Message, "Hyper3D could not be reached") || e.Hint != hintRetry {
			t.Errorf("%s: %+v", name, e)
		}
	}
}

// TestModelFileGoneIsNotFound: a model file deleted after the check reads
// as a show of a missing path does; a file beside it that is missing is
// named as such.
func TestModelFileGoneIsNotFound(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone.glb")
	want := Classify(CheckModelFile(missing))
	_, err := (&Service{}).ViewModel(context.Background(), missing, false)
	if got := Classify(err); got.Code != render.CodeNotFound || got.Message != want.Message || got.Message != "No model file at "+missing+"." || got.Hint != want.Hint {
		t.Fatalf("ViewModel of a missing file: %+v, want %+v", got, want)
	}

	dir := t.TempDir()
	gltf := filepath.Join(dir, "cube.gltf")
	if err := os.WriteFile(gltf, []byte(`{"asset":{"version":"2.0"},"buffers":[{"uri":"cube.bin","byteLength":4}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = (&Service{}).ViewModel(context.Background(), gltf, false)
	if got := Classify(err); got.Code != render.CodeNotFound || got.Message != "No file at "+filepath.Join(dir, "cube.bin")+"." {
		t.Fatalf("ViewModel of a .gltf without its buffer: %+v", got)
	}
}

// TestSendRefused: only a clear refusal of a request that charges or changes
// a task leaves it free to send again; every other error after it left may
// have come after Hyper3D took it (a 5xx from a gateway, no answer, a broken
// reply), and sendFailure answers that it may have started.
func TestSendRefused(t *testing.T) {
	proc := "task/rodin_update"
	cases := []struct {
		name    string
		err     error
		refused bool
	}{
		{"body error code", &rodin.ServiceError{Procedure: proc, Code: "INSUFFICIENT_BALANCE"}, true},
		{"body rate limit", &rodin.ServiceError{Procedure: proc, Code: "RATE_LIMITED"}, true},
		{"2xx naming no task", &rodin.ServiceError{Procedure: "task/rodin_mesh", Code: rodin.CodeNoTaskInReply}, false},
		{"401", &rodin.AuthError{Procedure: proc, Status: 401}, true},
		{"403", &rodin.HTTPError{Procedure: proc, Status: 403}, true},
		{"400", &rodin.HTTPError{Procedure: proc, Status: 400}, true},
		{"429", &rodin.HTTPError{Procedure: proc, Status: 429}, true},
		{"500", &rodin.HTTPError{Procedure: proc, Status: 500}, false},
		{"502", &rodin.HTTPError{Procedure: proc, Status: 502}, false},
		{"503", &rodin.HTTPError{Procedure: proc, Status: 503}, false},
		{"no answer", &rodin.NetError{Procedure: proc, Err: context.DeadlineExceeded}, false},
		{"broken reply", fmt.Errorf("%s: decode reply: %w", proc, errors.New("unexpected end of JSON input")), false},
	}
	for _, c := range cases {
		if got := sendRefused(c.err); got != c.refused {
			t.Errorf("%s: sendRefused = %v, want %v", c.name, got, c.refused)
		}
		err := sendFailure("redo_geometry", "u-1", c.err)
		f, isFailure := err.(*Failure)
		switch {
		case c.refused && isFailure:
			t.Errorf("%s: a refusal became %q", c.name, f.E.Message)
		case !c.refused && (!isFailure || f.E.Code != render.CodeUnavailable || !strings.Contains(f.E.Message, "may have started") ||
			!strings.HasPrefix(f.E.Hint, "Do not send it again: call get_task with u-1")):
			t.Errorf("%s: not answered as may have started: %v", c.name, err)
		}
	}
}
