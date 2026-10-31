package mcpserver

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sairaph/rodin3d-mcp/internal/fakehyper"
)

// TestPreviewModelFile: get_preview renders a local model file in its
// modes, pbr by default for a glTF file with materials, and lists them and
// what the file is drawn without; a mode it lacks is a conflict, a server
// image or a task as well is invalid input.
func TestPreviewModelFile(t *testing.T) {
	f := newFixture(t, true, false)
	abs := func(name string) string {
		p, err := filepath.Abs(filepath.Join("..", "modelfile", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	multi := abs("cube_multi.glb")
	r := f.call("get_preview", map[string]any{"model_file": multi, "size": 256})
	if r.res.IsError {
		t.Fatalf("default mode: %s", r.text)
	}
	if mode, _ := get(r.front, "preview", "mode").(string); mode != "pbr" {
		t.Errorf("default mode %q, want pbr", mode)
	}
	imgs := r.images()
	if len(imgs) != 1 {
		t.Fatalf("%d images", len(imgs))
	}
	if cfg, err := png.DecodeConfig(bytes.NewReader(imgs[0])); err != nil || cfg.Width != 256 {
		t.Errorf("image %v %+v", err, cfg)
	}
	if file, _ := get(r.front, "preview", "file").(string); file == "" {
		t.Error("no preview file")
	} else if _, err := os.Stat(file); err != nil {
		t.Error(err)
	}
	modes, _ := r.front["available"].([]any)
	if !slices.Contains(modes, any("tracer")) || !strings.Contains(r.text, "KHR_materials_clearcoat") {
		t.Errorf("available %v; reply %s", modes, r.text)
	}
	out := filepath.Join(t.TempDir(), "cube.png")
	if r := f.call("get_preview", map[string]any{"model_file": abs("cube_etc1s.glb"), "mode": "shaded", "view": "grid", "size": 256, "save_to": out}); r.res.IsError {
		t.Errorf("KTX2 shaded grid: %s", r.text)
	} else if _, err := os.Stat(out); err != nil {
		t.Error(err)
	}
	// An STL has no materials and no texture coordinates.
	stl := filepath.Join(t.TempDir(), "tri.stl")
	if err := os.WriteFile(stl, []byte("solid t\nfacet normal 0 0 1\nouter loop\nvertex 0 0 0\nvertex 1 0 0\nvertex 0 1 0\nendloop\nendfacet\nendsolid t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := f.call("get_preview", map[string]any{"model_file": stl, "size": 256}); r.res.IsError || get(r.front, "preview", "mode") != "classic" {
		t.Errorf("STL default: %s", r.text)
	}
	if r := f.call("get_preview", map[string]any{"model_file": stl, "mode": "pbr"}); r.code() != "conflict" || !strings.Contains(r.text, "classic") {
		t.Errorf("STL in pbr: %s", r.text)
	}
	for name, args := range map[string]map[string]any{
		"server image":     {"model_file": multi, "mode": "poster"},
		"task and file":    {"model_file": multi, "task_id": fakehyper.PackedID},
		"relative path":    {"model_file": "cube.glb"},
		"not a model file": {"model_file": out},
	} {
		if r := f.call("get_preview", args); r.code() != "invalid_input" {
			t.Errorf("%s: %s", name, r.text)
		}
	}
}
