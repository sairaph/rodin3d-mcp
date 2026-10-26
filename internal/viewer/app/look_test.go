package app

import (
	"strings"
	"testing"

	"github.com/hajimehoshi/ebiten/v2"
)

// N17: the draw rule of a pane, without a GPU: a mode whose maps did not
// load shows no picture of another mode and says why; every other case of
// the bar.
func TestPaneLook(t *testing.T) {
	f := newFakeMaps()
	f.fail["normal"] = true
	a, p := mapsApp(f, "pbr", "diffuse", "normal", "pbr", "shaded")
	tick(a, p)
	runPost(t, a)
	p.img = &ebiten.Image{} // a finished frame of an earlier mode; only its presence is read

	l := lookOf(p, "pbr")
	if l.picture || !l.failed || l.loading || !strings.HasPrefix(l.msg, "Cannot draw mode pbr: the normal map did not load") {
		t.Fatalf("pbr without its normal map: %+v", l)
	}
	for _, m := range []string{"toon", "ao", "normal"} { // modes that do not sample the normal map
		if l := lookOf(p, m); !l.picture || l.msg != "" || l.failed {
			t.Fatalf("%s: %+v", m, l)
		}
	}

	p.img = nil
	if l := lookOf(p, "ao"); l.picture {
		t.Fatalf("no frame yet: %+v", l)
	}

	p.mapsFailed = nil
	p.mapsWanted = true
	if l := lookOf(p, "pbr"); l.msg != "Loading the textures" || !l.loading || l.failed {
		t.Fatalf("maps loading in a textured mode: %+v", l)
	}
	if l := lookOf(p, "ao"); l.msg != "" {
		t.Fatalf("maps loading in an untextured mode: %+v", l)
	}
	p.mapsWanted = false

	p.renderErr = "mode pbr: no texture coordinates"
	if l := lookOf(p, "pbr"); l.msg != "Cannot draw mode pbr: no texture coordinates" || !l.failed {
		t.Fatalf("render error: %+v", l)
	}
	p.renderErr = ""

	p.status, p.what = "loading", "mesh"
	if l := lookOf(p, "pbr"); l.msg != "Loading the mesh" || !l.loading || l.failed {
		t.Fatalf("loading: %+v", l)
	}
	p.status, p.err = "error", "the file is not a model"
	if l := lookOf(p, "pbr"); l.msg != "Cannot show this: the file is not a model" || !l.failed {
		t.Fatalf("error: %+v", l)
	}
	p.status = "ready"
	if l := lookOf(p, "pbr"); l.msg != "" || l.failed || l.loading {
		t.Fatalf("ready with nothing to say: %+v", l)
	}
}
