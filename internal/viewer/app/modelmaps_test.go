package app

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/sairaph/rodin3d-mcp/internal/modelfile"
	"github.com/sairaph/rodin3d-mcp/internal/render3d"
	"github.com/sairaph/rodin3d-mcp/internal/viewer"
)

// TestModelFileMaps: a model file pane follows the task panes' memory rule
// (notes 41): its materials' maps are decoded only when a textured mode is
// shown, released when the mode samples none, and decoded again when one is
// chosen again; a quick frame draws with the same maps. The textures panel
// decodes a picture only when it is shown.
func TestModelFileMaps(t *testing.T) {
	ctx := context.Background()
	file, _ := filepath.Abs(filepath.Join("..", "..", "modelfile", "testdata", "cube_multi.glb"))
	m, err := modelfile.Load(ctx, file, modelfile.Decoders{}, false)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := mapsApp(newFakeMaps(), "classic")
	p := &pane{kind: viewer.KindModelFile, file: file, model: m, mesh: m.Mesh, simple: m.Mesh, frame: render3d.FrameOf(m.Mesh),
		modes: []string{"pbr", "classic"}, status: "ready", div: 2}
	a.panes, a.kind = []*pane{p}, viewer.KindModelFile
	var decodes, pictures atomic.Int32
	a.modelTextures = func(ctx context.Context, m *modelfile.Model) (*modelfile.Model, error) {
		decodes.Add(1)
		return m.WithTextures(ctx, modelfile.Decoders{})
	}
	a.modelPicture = func(ctx context.Context, path string, tex int) (*render3d.Texture, error) {
		pictures.Add(1)
		return modelfile.Picture(ctx, path, modelfile.Decoders{}, tex)
	}
	textured := func() bool { return p.mesh.Mats[0].Base != nil && p.simple.Mats[0].Base != nil }

	if j := tick(a, p); j == nil || textured() || decodes.Load() != 0 {
		t.Fatalf("classic: job %v, textured %v, %d decodes", j != nil, textured(), decodes.Load())
	}
	a.setMode("pbr")
	if j := tick(a, p); j != nil || !p.mapsWanted {
		t.Fatal("pbr drew before the maps were decoded")
	}
	runPost(t, a)
	if !textured() || decodes.Load() != 1 || paneMapCount(p) != 1 {
		t.Fatalf("pbr: textured %v, %d decodes, map count %d", textured(), decodes.Load(), paneMapCount(p))
	}
	if j := tick(a, p); j == nil || j.mesh.Mats[0].Base == nil {
		t.Fatal("pbr: no job with the maps")
	}
	a.setMode("classic")
	if textured() || !p.model.Pending || paneMapCount(p) != 0 {
		t.Fatal("classic kept the maps")
	}
	a.setMode("pbr")
	tick(a, p)
	runPost(t, a)
	if !textured() || decodes.Load() != 2 {
		t.Fatalf("pbr again: textured %v, %d decodes", textured(), decodes.Load())
	}

	thumbs := a.modelMapThumbs(file, m.Textures)
	if len(thumbs) != 2 || thumbs[0].name != "first/base_color" || pictures.Load() != 0 {
		t.Fatalf("thumbs %d, first %q, %d pictures before showing", len(thumbs), thumbs[0].name, pictures.Load())
	}
	if img, err := thumbs[1].src(); err != nil || img.Bounds().Dx() != 16 || pictures.Load() != 1 {
		t.Errorf("shown thumb: %v, %d pictures", err, pictures.Load())
	}
}
