package stage

import (
	"slices"
	"testing"
)

func TestOf(t *testing.T) {
	cases := []struct {
		step, state string
		pending     int
		stage, st   string
	}{
		{"Created", "Generating", 0, GeometryDraft, Running},
		{"Created", "", 0, GeometryDraft, Running},
		{"ModelGenerate", "Generating", 1, GeometryDraft, Running},
		{"ModelGenerate", "DonePreview", 0, GeometryDraft, Ready},
		{"ModelGenerate", "DonePreview", 1, GeometryDraft, Running},
		{"ModelRefine", "Generating", 0, Geometry, Running},
		{"ModelRefine", "DonePreview", 0, Geometry, Ready},
		{"ModelRefine", "DoneBasic", 0, Geometry, Packed},
		{"TextureGenerate", "DonePreview", 0, MaterialDraft, Ready},
		{"TextureGenerate", "Liked", 0, MaterialDraft, Ready},
		{"TextureRefine", "DonePreview", 0, Material, Ready},
		{"TextureRefine", "DoneHigh", 0, Material, Packed},
		{"TextureRefine", "Failedish", 0, Material, Unknown},
	}
	for _, c := range cases {
		got := Of(c.step, c.state, c.pending)
		if got.Stage != c.stage || got.State != c.st {
			t.Errorf("Of(%s, %s, %d) = %s/%s, want %s/%s", c.step, c.state, c.pending, got.Stage, got.State, c.stage, c.st)
		}
	}
}

func TestActions(t *testing.T) {
	if a := Actions(Of("ModelGenerate", "Generating", 0)); !slices.Equal(a, []string{"wait_task"}) {
		t.Errorf("running draft: %v", a)
	}
	if a := Actions(Of("ModelGenerate", "DonePreview", 0)); !slices.Contains(a, "confirm_geometry") || slices.Contains(a, "download_model") {
		t.Errorf("draft: %v", a)
	}
	if a := Actions(Of("TextureRefine", "DoneBasic", 0)); !slices.Contains(a, "download_model") || !slices.Contains(a, "get_preview") {
		t.Errorf("packed: %v", a)
	}
}

func TestRedo(t *testing.T) {
	if g, m := RedoLimits([]string{"RodinMediumRedoCount"}); g != 20 || m != 6 {
		t.Errorf("creator %d %d", g, m)
	}
	if g, m := RedoLimits([]string{"RodinHighRedoCount", "RodinMediumRedoCount"}); g != 50 || m != 15 {
		t.Errorf("business %d %d", g, m)
	}
	if g, m := RedoLimits(nil); g != 10 || m != 3 {
		t.Errorf("free %d %d", g, m)
	}
	// The web shows "Redo x19" for a Creator draft with versions v1, v2.
	if n := RedosLeft(20, 2); n != 19 {
		t.Errorf("left %d", n)
	}
	if n := RedosLeft(3, 9); n != 0 {
		t.Errorf("never negative: %d", n)
	}
}

func TestModelAndTier(t *testing.T) {
	if Tier("gen-2.5-extreme-low") != "extreme-low" || Tier("x") != "" {
		t.Error("tier")
	}
	if Model("Brilliant") != "gen-2.5" || Model("Detail") != "gen-1.5" || Gen15Mode("Smooth") != "zero" {
		t.Error("model")
	}
}
