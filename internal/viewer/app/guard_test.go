package app

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sairaph/mcp-wizard/render"

	"github.com/sairaph/rodin3d-mcp/internal/modelfile"
	"github.com/sairaph/rodin3d-mcp/internal/render3d"
	"github.com/sairaph/rodin3d-mcp/internal/service"
	"github.com/sairaph/rodin3d-mcp/internal/viewer"
)

// Tests of the crash guards on the viewer's background work (notes/73 B):
// a reader that panics on a file's content fails that load, where the load
// shows its failures, and the window goes on.

const panicked = "the reader failed on this content: bad content"

// panicPictures makes readPicture panic for the rest of the test.
func panicPictures(t *testing.T) {
	t.Helper()
	saved := readPicture
	readPicture = func(string, int) (image.Image, int, int, error) { panic("bad content") }
	t.Cleanup(func() { readPicture = saved })
}

// TestGuardedMapLoads: a panic while a task's map or a model file's maps
// decode fails those maps (the mode says which); the pane stays ready and
// the other maps load.
func TestGuardedMapLoads(t *testing.T) {
	f := newFakeMaps()
	a, p := mapsApp(f, "pbr", "diffuse", "normal", "pbr")
	a.viewMap = func(ctx context.Context, uuid string, st service.ViewStage, kind string) (*render3d.Texture, error) {
		if kind == "normal" {
			panic("bad content")
		}
		return f.viewMap(ctx, uuid, st, kind)
	}
	tick(a, p)
	runPost(t, a)
	if p.status != "ready" || held(p.maps) != "diffuse pbr" || !strings.Contains(p.mapsFailed["normal"], "The reader failed on this content: bad content") {
		t.Fatalf("task maps: status %s, holds %q, failed %v", p.status, held(p.maps), p.mapsFailed)
	}
	if e := mapsError(p, "pbr"); !strings.Contains(e, "normal map did not load") {
		t.Fatalf("task maps: error %q", e)
	}

	file := filepath.Join(t.TempDir(), "out.glb")
	putFile(t, file, testdataFile("cube_png.glb"), nil)
	a, p, _ = modelFileApp(t, file, "classic")
	a.modelTextures = func(context.Context, *modelfile.Model) (*modelfile.Model, error) { panic("bad content") }
	a.setMode("pbr")
	tick(a, p)
	runPost(t, a)
	if p.status != "ready" || !strings.Contains(p.mapsFailed[modelMaps], "The reader failed on this content: bad content") {
		t.Fatalf("model maps: status %s, failed %v", p.status, p.mapsFailed)
	}
	if e := a.snapshot().Panes[0].Error; !strings.Contains(e, "the textures did not load") {
		t.Fatalf("model maps: pane error %q", e)
	}
	if j := tick(a, p); j != nil || p.mapsWanted {
		t.Fatal("the failed maps were read again without a mode change")
	}
	a.setMode("classic")
	if j := tick(a, p); j == nil {
		t.Fatal("an untextured mode does not draw after the failed maps")
	}
}

// TestGuardedPictureLoads: a panic in a textures panel picture, a strip
// download, a thumbnail or a flat picture fails that picture only.
func TestGuardedPictureLoads(t *testing.T) {
	a := &App{ctx: context.Background(), loaded: make(chan func(), 4), layout: viewer.LayoutTextures}

	tex := &thumb{kind: "map", name: "diffuse", src: func() (image.Image, error) { panic("bad content") }}
	a.textures = []*thumb{tex}
	a.loadTexture(tex)
	runPost(t, a)
	if tex.loading || tex.img != nil || !strings.Contains(tex.err, "The reader failed on this content: bad content") {
		t.Fatalf("texture: loading %v, error %q", tex.loading, tex.err)
	}

	strip := &thumb{kind: "poster", name: "poster"}
	a.strip = []*thumb{strip}
	a.fetchThumb(a.contentGen, strip, func() (string, error) { panic("bad content") })
	runPost(t, a)
	if !strings.Contains(strip.err, "The reader failed on this content: bad content") {
		t.Fatalf("download: error %q", strip.err)
	}

	panicPictures(t)
	strip.err = ""
	a.decodeThumb(a.contentGen, strip, "poster.png")
	runPost(t, a)
	if strip.img != nil || strip.err != panicked {
		t.Fatalf("thumbnail: error %q", strip.err)
	}

	strip.file = "poster.png"
	a.loadFlat(strip)
	runPost(t, a)
	if strip.fullLoading || strip.full != nil {
		t.Fatalf("flat picture: loading %v", strip.fullLoading)
	}
}

// TestGuardedCapture: a screenshot whose encoding panics (here: fewer
// pixels than its size) answers with an error instead of a picture; a good
// one answers the PNG.
func TestGuardedCapture(t *testing.T) {
	c := &captureReq{done: make(chan viewer.CaptureReply, 1)}
	encodeCapture(c, viewer.CaptureReply{Width: 4, Height: 4}, make([]byte, 8), 4, 4)
	rep := <-c.done
	if rep.Error == nil || rep.Error.Code != render.CodeInternal || !strings.Contains(rep.Error.Message, "the reader failed on this content") || rep.PNG != nil {
		t.Fatalf("broken capture: %+v", rep.Error)
	}
	encodeCapture(c, viewer.CaptureReply{Width: 4, Height: 4}, make([]byte, 4*4*4), 4, 4)
	rep = <-c.done
	if rep.Error != nil {
		t.Fatalf("good capture: %+v", rep.Error)
	}
	img, err := png.Decode(bytes.NewReader(rep.PNG))
	if err != nil || img.Bounds().Dx() != 4 {
		t.Fatalf("good capture: %v", err)
	}
}
