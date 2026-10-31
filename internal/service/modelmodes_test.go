package service

import (
	"slices"
	"testing"

	"github.com/sairaph/rodin3d-mcp/internal/modelfile"
	"github.com/sairaph/rodin3d-mcp/internal/render3d"
)

// TestModelModes: a model file with materials offers the textured modes in
// front, with or without texture coordinates; one without materials does
// not; the modes that need texture coordinates go without them.
func TestModelModes(t *testing.T) {
	mesh := func(uv, mats bool) *modelfile.Model {
		m := &render3d.Mesh{Pos: [][3]float32{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}}, Idx: []uint32{0, 1, 2}}
		if uv {
			m.UV = [][2]float32{{0, 0}, {1, 0}, {0, 1}}
		}
		if mats {
			m.Mats = []*render3d.Material{render3d.DefaultMaterial()}
		}
		return &modelfile.Model{Mesh: m}
	}
	textured := []string{"pbr", "shaded", "toon", "tracer"}
	for _, tc := range []struct {
		uv, mats       bool
		front, without []string
	}{
		{true, true, textured, nil},
		{false, true, textured, []string{"copper", "uv_checker"}},
		{true, false, []string{"normal"}, textured},
		{false, false, []string{"normal"}, append([]string{"copper", "uv_checker"}, textured...)},
	} {
		got := ModelModes(mesh(tc.uv, tc.mats))
		if len(got) < len(tc.front) || !slices.Equal(got[:len(tc.front)], tc.front) {
			t.Errorf("uv %v, materials %v: %v, want %v first", tc.uv, tc.mats, got, tc.front)
		}
		for _, m := range tc.without {
			if slices.Contains(got, m) {
				t.Errorf("uv %v, materials %v: %v has %s", tc.uv, tc.mats, got, m)
			}
		}
		for _, m := range got {
			if !render3d.Mode(m).Known() {
				t.Errorf("unknown mode %s", m)
			}
		}
	}
}
