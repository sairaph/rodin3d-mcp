package service

import (
	"strings"
	"testing"

	"github.com/sairaph/rodin3d-mcp/internal/viewer"
)

// N17: describe names a ready pane's error (a mode it cannot draw) and a
// pane's note in the reply text, and says nothing of either when there is
// none.
func TestDescribePaneError(t *testing.T) {
	st := viewer.State{
		Content: viewer.ContentInfo{Kind: viewer.KindTask, TaskID: "0123456789abcdef", Prompt: "a chair"},
		Layout:  viewer.LayoutSingle, Stage: "material", Mode: "pbr", View: "front",
		Panes: []viewer.PaneState{{Index: 0, Kind: "task", Status: "ready",
			Error: "mode pbr: the normal map did not load: Disk read failed. Choose the mode again to retry."}},
	}
	got := describe(st)
	if !strings.Contains(got, "Pane 0 cannot draw mode pbr: the normal map did not load: Disk read failed. Choose the mode again to retry.") {
		t.Fatalf("ready pane error missing: %q", got)
	}
	if strings.Contains(got, "..") {
		t.Fatalf("doubled full stop: %q", got)
	}

	st.Panes[0].Error = ""
	st.Panes[0].Note = "the model has no texture coordinates, so mode classic is shown"
	got = describe(st)
	if strings.Contains(got, "cannot draw") || !strings.Contains(got, "Pane 0: the model has no texture coordinates, so mode classic is shown.") {
		t.Fatalf("note only: %q", got)
	}

	st.Panes[0].Note = ""
	if got = describe(st); strings.Contains(got, "Pane 0") {
		t.Fatalf("nothing to say about the pane: %q", got)
	}

	st.Panes[0].Status, st.Panes[0].Error = "error", "the file is not a model"
	if got = describe(st); !strings.Contains(got, "Pane 0 could not be drawn: the file is not a model") {
		t.Fatalf("pane error: %q", got)
	}
}
