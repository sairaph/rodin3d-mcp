package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	a.modelPicture = func(ctx context.Context, file modelfile.File, tex int) (*render3d.Texture, error) {
		pictures.Add(1)
		return modelfile.Picture(ctx, file, modelfile.Decoders{}, tex)
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

	thumbs := a.modelMapThumbs(m.File, m.Textures)
	if len(thumbs) != 2 || thumbs[0].name != "first/base_color" || pictures.Load() != 0 {
		t.Fatalf("thumbs %d, first %q, %d pictures before showing", len(thumbs), thumbs[0].name, pictures.Load())
	}
	if img, err := thumbs[1].src(); err != nil || img.Bounds().Dx() != 16 || pictures.Load() != 1 {
		t.Errorf("shown thumb: %v, %d pictures", err, pictures.Load())
	}
}

// testdataFile is a fixture of package modelfile.
func testdataFile(name string) string {
	f, _ := filepath.Abs(filepath.Join("..", "..", "modelfile", "testdata", name))
	return f
}

// modelFileApp is a window showing the model file file in mode, loaded by
// the pane's own load (as a show does) through the real reader; loads
// counts the reads of the file.
func modelFileApp(t *testing.T, file, mode string) (a *App, p *pane, loads *atomic.Int32) {
	t.Helper()
	a, _ = mapsApp(newFakeMaps(), mode)
	loads = &atomic.Int32{}
	a.viewModel = func(ctx context.Context, path string, textures bool) (*modelfile.Model, error) {
		loads.Add(1)
		return modelfile.Load(ctx, path, modelfile.Decoders{}, textures)
	}
	a.modelTextures = func(ctx context.Context, m *modelfile.Model) (*modelfile.Model, error) {
		return m.WithTextures(ctx, modelfile.Decoders{})
	}
	a.modelPicture = func(ctx context.Context, file modelfile.File, tex int) (*render3d.Texture, error) {
		return modelfile.Picture(ctx, file, modelfile.Decoders{}, tex)
	}
	p = &pane{kind: viewer.KindModelFile, file: file, div: 2}
	a.panes, a.kind, a.info = []*pane{p}, viewer.KindModelFile, viewer.ContentInfo{Kind: viewer.KindModelFile, File: file}
	a.loadPane(p)
	runPost(t, a)
	if p.status != "ready" {
		t.Fatalf("the first load: status %s, error %q", p.status, p.err)
	}
	return a, p, loads
}

// putFile writes the bytes of the file from (or data) at file.
func putFile(t *testing.T, file, from string, data []byte) {
	t.Helper()
	if from != "" {
		var err error
		if data, err = os.ReadFile(from); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// touch gives file a later modification time, its bytes unchanged.
func touch(t *testing.T, file string) {
	t.Helper()
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatal(err)
	}
}

// withoutMaterials is the GLB at file with its materials taken out.
func withoutMaterials(t *testing.T, file string) []byte {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	n := int(binary.LittleEndian.Uint32(b[12:]))
	var doc map[string]any
	if err := json.Unmarshal(b[20:20+n], &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc, "materials")
	for _, m := range doc["meshes"].([]any) {
		for _, pr := range m.(map[string]any)["primitives"].([]any) {
			delete(pr.(map[string]any), "material")
		}
	}
	bin := b[20+n:] // the BIN chunk with its header
	js, _ := json.Marshal(doc)
	for len(js)%4 != 0 {
		js = append(js, ' ')
	}
	var out bytes.Buffer
	out.WriteString("glTF")
	binary.Write(&out, binary.LittleEndian, []uint32{2, uint32(12 + 8 + len(js) + len(bin)), uint32(len(js))})
	out.WriteString("JSON")
	out.Write(js)
	out.Write(bin)
	return out.Bytes()
}

// reloadStarted checks that the pane started reading its file again: it is
// loading, says why, holds nothing of the old model, and the revision
// stayed.
func reloadStarted(t *testing.T, a *App, p *pane, rev int64) {
	t.Helper()
	ps := a.snapshot().Panes[0]
	if p.status != "loading" || ps.Status != "loading" || ps.Note != reloadedNote || ps.Error != "" {
		t.Fatalf("reload: status %s, note %q, error %q", ps.Status, ps.Note, ps.Error)
	}
	if p.model != nil || p.mesh != nil || p.hasShown || p.mapsWanted || len(a.textures) != 0 {
		t.Fatal("the reload kept the old model, picture or textures")
	}
	if a.rev != rev {
		t.Fatalf("the reload bumped the revision: %d, was %d", a.rev, rev)
	}
}

// TestModelFileReload (notes/73 A): a load of a model file's pictures that
// finds the file changed on disk reads the file again, as a show would: the
// new geometry, materials, modes and textures; the mode and the camera stay;
// the note says so; the revision stays. One reload is one read; a later
// change reloads again when a load needs it.
func TestModelFileReload(t *testing.T) {
	file := filepath.Join(t.TempDir(), "out.glb")
	putFile(t, file, testdataFile("cube_png.glb"), nil)
	a, p, loads := modelFileApp(t, file, "classic")
	cam := camState{Az: 10, El: 5, Zoom: 1.5}
	a.cam, a.viewName = cam, "custom"
	rev := a.rev

	// Rewritten with another model; the textured mode's maps find it.
	putFile(t, file, testdataFile("cube_multi.glb"), nil)
	a.setMode("pbr")
	tick(a, p)
	runPost(t, a)
	reloadStarted(t, a, p, rev)
	if j := tick(a, p); j != nil {
		t.Fatal("a job for the pane while it reloads")
	}
	runPost(t, a)
	ps := a.snapshot().Panes[0]
	if p.status != "ready" || len(p.mesh.Mats) != 2 || a.mode != "pbr" || p.model.Pending {
		t.Fatalf("after the reload: status %s, %d materials, mode %s, maps pending %v", p.status, len(p.mesh.Mats), a.mode, p.model.Pending)
	}
	if ps.Note != reloadedNote+"; drawn without KHR_materials_clearcoat" || ps.Error != "" {
		t.Fatalf("after the reload: note %q, error %q", ps.Note, ps.Error)
	}
	if names := strings.Join(a.textureNames(), " "); names != "first/base_color first/metallic_roughness" {
		t.Fatalf("textures after the reload: %s", names)
	}
	if a.cam != cam || a.viewName != "custom" || a.rev != rev || loads.Load() != 2 {
		t.Fatalf("camera %v %s, revision %d (was %d), %d reads", a.cam, a.viewName, a.rev, rev, loads.Load())
	}
	if j := tick(a, p); j == nil || j.mesh.Mats[0].Base == nil {
		t.Fatal("pbr does not draw the new model with its maps")
	}
	// No loop: nothing more is read while the file stays as it is.
	for range 3 {
		tick(a, p)
	}
	if len(a.loaded) != 0 || loads.Load() != 2 || p.mapsWanted {
		t.Fatalf("more loads after the reload: %d posted, %d reads", len(a.loaded), loads.Load())
	}

	// Touched only (same bytes, newer time): read again too, the next time
	// a load needs it.
	touch(t, file)
	a.setMode("classic")
	if len(a.loaded) != 0 || loads.Load() != 2 {
		t.Fatal("an untextured mode read the file")
	}
	a.setMode("pbr")
	tick(a, p)
	runPost(t, a)
	reloadStarted(t, a, p, rev)
	runPost(t, a)
	if p.status != "ready" || loads.Load() != 3 || !strings.HasPrefix(a.snapshot().Panes[0].Note, reloadedNote) {
		t.Fatalf("touched file: status %s, %d reads, note %q", p.status, loads.Load(), p.note)
	}
	if j := tick(a, p); j == nil || j.mesh.Mats[0].Base == nil {
		t.Fatal("pbr does not draw after the touched file's reload")
	}

	// A show of the file again drops the note.
	if rep := a.applyShow(&prepared{kind: viewer.KindModelFile, file: file}, viewer.ShowParams{}); rep.Error != nil {
		t.Fatal(rep.Error)
	}
	runPost(t, a)
	if p := a.panes[0]; p.reloaded || strings.Contains(p.note, reloadedNote) {
		t.Fatalf("a new show kept the reload note: %q", p.note)
	}
}

// TestModelFileReloadFromTextures: the textures panel's picture finds the
// file changed (touched only) and the pane reloads; the picture selected by
// name stays selected.
func TestModelFileReloadFromTextures(t *testing.T) {
	file := filepath.Join(t.TempDir(), "out.glb")
	putFile(t, file, testdataFile("cube_png.glb"), nil)
	a, p, loads := modelFileApp(t, file, "classic")
	layout, sel := viewer.LayoutTextures, "normal"
	if rep := a.applyControl(viewer.Control{Layout: &layout, Select: &viewer.Select{TextureMap: &sel}}); rep.Error != nil {
		t.Fatal(rep.Error)
	}
	rev := a.rev
	touch(t, file)
	shown := a.shownTexture()
	a.loadTexture(shown)
	runPost(t, a)
	reloadStarted(t, a, p, rev)
	if st := a.snapshot(); st.Layout != viewer.LayoutTextures || !slices.Contains(st.Layouts, st.Layout) {
		t.Fatalf("while reloading: layout %s of %v", st.Layout, st.Layouts)
	}
	runPost(t, a)
	if p.status != "ready" || loads.Load() != 2 || shown.err != "" {
		t.Fatalf("after the reload: status %s, %d reads, old thumb error %q", p.status, loads.Load(), shown.err)
	}
	if a.layout != viewer.LayoutTextures || a.selected.TextureMap != "normal" || a.textures[a.texSel].name != "normal" || slices.Contains(a.textures, shown) {
		t.Fatalf("textures panel after the reload: layout %s, selected %q, shows %q", a.layout, a.selected.TextureMap, a.textures[a.texSel].name)
	}

	// A picture of the old list that ends after the reload changes nothing.
	touch(t, file)
	a.loadTexture(shown)
	runPost(t, a)
	if p.status != "ready" || loads.Load() != 2 {
		t.Fatalf("a load of a picture no longer listed reloaded: status %s, %d reads", p.status, loads.Load())
	}
}

// TestModelFileReloadChanges: a reload of a file that lost its materials
// shows the new model's default mode with the usual note and leaves the
// textures layout; one of a file that is gone shows the read's error.
func TestModelFileReloadChanges(t *testing.T) {
	file := filepath.Join(t.TempDir(), "out.glb")
	putFile(t, file, testdataFile("cube_png.glb"), nil)
	a, p, loads := modelFileApp(t, file, "pbr")
	layout := viewer.LayoutTextures
	if rep := a.applyControl(viewer.Control{Layout: &layout}); rep.Error != nil {
		t.Fatal(rep.Error)
	}
	putFile(t, file, "", withoutMaterials(t, file))
	a.loadTexture(a.shownTexture())
	runPost(t, a)
	runPost(t, a)
	want := reloadedNote + "; mode pbr is not available for this model, so it shows " + defaultMode(p.modes)
	if p.status != "ready" || a.mode != defaultMode(p.modes) || slices.Contains(p.modes, "pbr") || p.note != want {
		t.Fatalf("no materials: status %s, mode %s of %v, note %q", p.status, a.mode, p.modes, p.note)
	}
	if len(a.textures) != 0 || a.layout != viewer.LayoutSingle || !slices.Contains(a.snapshot().Layouts, a.layout) {
		t.Fatalf("no materials: %d textures, layout %s", len(a.textures), a.layout)
	}

	// The file is gone: the pane shows the read's error, whether the maps
	// or the textures panel found it gone.
	for _, from := range []string{"maps", "textures panel"} {
		putFile(t, file, testdataFile("cube_png.glb"), nil)
		a.setMode("pbr")
		a.loadPane(p) // as a show of it: back to a textured model
		runPost(t, a)
		if p.status != "ready" || len(a.textures) == 0 {
			t.Fatalf("%s setup: status %s", from, p.status)
		}
		reads := loads.Load()
		a.setMode("classic")
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
		if from == "maps" {
			a.setMode("pbr")
			tick(a, p)
		} else {
			a.layout = viewer.LayoutTextures
			a.loadTexture(a.shownTexture())
		}
		runPost(t, a)
		runPost(t, a)
		ps := a.snapshot().Panes[0]
		if ps.Status != "error" || !strings.Contains(ps.Error, "out.glb") || ps.Note != reloadedNote || p.mesh != nil {
			t.Fatalf("%s, file gone: status %s, error %q, note %q", from, ps.Status, ps.Error, ps.Note)
		}
		if n := loads.Load(); n != reads+1 {
			t.Fatalf("%s: %d reads, want %d (one per load)", from, n, reads+1)
		}
		if a.layout != viewer.LayoutSingle {
			t.Fatalf("%s: layout %s after the failed reload", from, a.layout)
		}
		if j := tick(a, p); j != nil || len(a.loaded) != 0 {
			t.Fatalf("%s: the failed pane loads or draws again", from)
		}
	}
}
