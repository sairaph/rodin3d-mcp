package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sairaph/rodin3d-mcp/internal/render3d"
	"github.com/sairaph/rodin3d-mcp/internal/service"
	"github.com/sairaph/rodin3d-mcp/internal/viewer"
)

// Tests of a task pane's texture maps (notes 41, fix rounds 3 and 4): a
// pane loads the maps its mode samples when the mode is shown, holds only
// those, draws nothing in a mode while one of its maps is missing, and a map
// that fails to load stops only the modes that sample it.

// fakeMaps is a viewMap that hands out one texture per kind, fails the
// kinds in fail, and holds every read while gate is set.
type fakeMaps struct {
	mu    sync.Mutex
	reads []string
	fail  map[string]bool
	gate  chan struct{}
	tex   map[string]*render3d.Texture
}

func newFakeMaps() *fakeMaps {
	f := &fakeMaps{fail: map[string]bool{}, tex: map[string]*render3d.Texture{}}
	for _, k := range render3d.MapKinds {
		f.tex[k] = &render3d.Texture{W: 1, H: 1, Pix: []uint8{128, 128, 255, 255}}
	}
	return f
}

func (f *fakeMaps) viewMap(_ context.Context, _ string, _ service.ViewStage, kind string) (*render3d.Texture, error) {
	f.mu.Lock()
	gate, fail := f.gate, f.fail[kind]
	f.reads = append(f.reads, kind)
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if fail {
		return nil, errors.New("disk read failed")
	}
	return f.tex[kind], nil
}

func (f *fakeMaps) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reads)
}

// mapsApp is a window with one ready task pane of a stage that has the
// given maps, showing mode; its renderer only queues jobs.
func mapsApp(f *fakeMaps, mode string, kinds ...string) (*App, *pane) {
	st := service.ViewStage{Name: "material", Modes: []string{"pbr", "tracer", "shaded", "toon", "ao", "normal"}}
	for _, k := range kinds {
		st.Maps = append(st.Maps, service.ViewAsset{Mode: "texture_" + k})
	}
	m, _ := traceQuad()
	p := &pane{kind: viewer.KindTask, vt: &service.ViewTask{UUID: "0123456789abcdef"}, stage: st, modes: st.Modes,
		status: "ready", mesh: m, simple: m, frame: render3d.FrameOf(m), div: 2}
	a := &App{ctx: context.Background(), loaded: make(chan func(), 16), ui: newUI(), viewMap: f.viewMap,
		rend: &renderer{wake: make(chan struct{}, 1), out: make(chan renderDone, 8), quit: make(chan struct{})},
		kind: viewer.KindTask, panes: []*pane{p}, layout: viewer.LayoutSingle, mode: mode, cam: defaultCam(), viewName: "three_quarter",
		flat: -1, background: "default", sw: 800, sh: 600, scale: 1}
	a.ui.layout(a)
	return a, p
}

// runPost runs the next result a load posts to the game loop.
func runPost(t *testing.T, a *App) {
	t.Helper()
	select {
	case fn := <-a.loaded:
		fn()
	case <-time.After(5 * time.Second):
		t.Fatal("no load result within 5 s")
	}
}

// tick schedules renders as one Update does and returns the job queued for
// p, if any (taken off the queue).
func tick(a *App, p *pane) *renderJob {
	p.hasQueued = false
	a.scheduleRenders(time.Now().Add(time.Hour))
	a.rend.mu.Lock()
	defer a.rend.mu.Unlock()
	for i, j := range a.rend.queue {
		if j.p == p {
			a.rend.queue = slices.Delete(a.rend.queue, i, i+1)
			return j
		}
	}
	return nil
}

func held(m *render3d.Maps) string {
	var out []string
	for _, k := range render3d.MapKinds {
		if m.Map(k) != nil {
			out = append(out, k)
		}
	}
	return strings.Join(out, " ")
}

// TestPaneMapsLoad: a textured mode draws nothing until its maps are in,
// loads each missing map once, and then draws with them; maps the pane
// already holds for the mode are kept by the merge.
func TestPaneMapsLoad(t *testing.T) {
	f := newFakeMaps()
	a, p := mapsApp(f, "pbr", "diffuse", "normal", "pbr", "shaded")
	if j := tick(a, p); j != nil {
		t.Fatal("rendered pbr before its maps were loaded")
	}
	if !p.mapsWanted {
		t.Fatal("no map load started")
	}
	if j := tick(a, p); j != nil {
		t.Fatal("rendered while the maps load")
	}
	if q := a.quality(); q != viewer.QualityInteractive {
		t.Fatalf("quality %s while the maps load", q)
	}
	runPost(t, a)
	if p.mapsWanted || held(p.maps) != "diffuse normal pbr" {
		t.Fatalf("after the load: wanted %v, holds %q", p.mapsWanted, held(p.maps))
	}
	if n := f.readCount(); n != 3 {
		t.Fatalf("%d map reads, want 3 (one per missing map, one load)", n)
	}
	j := tick(a, p)
	if j == nil || j.maps != p.maps || j.key.maps != 3 || j.key.mode != "pbr" {
		t.Fatalf("pbr job after the load: %+v", j)
	}

	// Toon keeps the diffuse map; pbr then loads only normal and pbr, and
	// the merge keeps the diffuse map it already had.
	diffuse := p.maps.Diffuse
	a.setMode("toon")
	if held(p.maps) != "diffuse" || p.maps.Diffuse != diffuse {
		t.Fatalf("toon holds %q", held(p.maps))
	}
	if j := tick(a, p); j == nil || j.key.mode != "toon" {
		t.Fatal("toon did not draw with the diffuse map it kept")
	}
	a.setMode("pbr")
	if j := tick(a, p); j != nil {
		t.Fatal("rendered pbr with missing maps")
	}
	runPost(t, a)
	if held(p.maps) != "diffuse normal pbr" || p.maps.Diffuse != diffuse {
		t.Fatalf("merge holds %q (diffuse kept: %v)", held(p.maps), p.maps.Diffuse == diffuse)
	}
	if n := f.readCount(); n != 5 {
		t.Fatalf("%d map reads, want 5 (normal and pbr read again, diffuse kept)", n)
	}
}

// TestPaneMapsPerMode: a mode switch leaves the pane only the maps the new
// mode samples (N12); a render job keeps the set it was given; a model
// file keeps the maps that came with it.
func TestPaneMapsPerMode(t *testing.T) {
	f := newFakeMaps()
	all := func(kinds ...string) *render3d.Maps {
		m := &render3d.Maps{}
		for _, k := range kinds {
			m.SetMap(k, f.tex[k])
		}
		return m
	}
	for _, tc := range []struct {
		name   string
		stage  []string
		from   []string
		mode   string
		want   string
		samePt bool // the pane keeps its very set (no new render key)
	}{
		{"pbr to tracer", []string{"diffuse", "normal", "pbr", "shaded"}, []string{"diffuse", "normal", "pbr"}, "tracer", "diffuse normal pbr", true},
		{"pbr to toon", []string{"diffuse", "normal", "pbr", "shaded"}, []string{"diffuse", "normal", "pbr"}, "toon", "diffuse", false},
		{"pbr to shaded", []string{"diffuse", "normal", "pbr", "shaded"}, []string{"diffuse", "normal", "pbr"}, "shaded", "", false},
		{"pbr to shaded without a shaded map", []string{"diffuse", "normal", "pbr"}, []string{"diffuse", "normal", "pbr"}, "shaded", "diffuse", false},
		{"pbr to ao", []string{"diffuse", "normal", "pbr", "shaded"}, []string{"diffuse", "normal", "pbr"}, "ao", "", false},
		{"shaded to pbr", []string{"diffuse", "normal", "pbr", "shaded"}, []string{"shaded"}, "pbr", "", false},
		{"shaded to toon", []string{"diffuse", "normal", "pbr", "shaded"}, []string{"shaded"}, "toon", "", false},
		{"toon to shaded without a shaded map", []string{"diffuse", "pbr"}, []string{"diffuse"}, "shaded", "diffuse", true},
		{"nothing held", []string{"diffuse"}, nil, "normal", "", true},
	} {
		a, p := mapsApp(f, "pbr", tc.stage...)
		if tc.from != nil {
			p.maps = all(tc.from...)
		}
		before, beforeHeld := p.maps, held(p.maps)
		a.setMode(tc.mode)
		if got := held(p.maps); got != tc.want {
			t.Errorf("%s: holds %q, want %q", tc.name, got, tc.want)
		}
		if (p.maps == before) != tc.samePt {
			t.Errorf("%s: kept the same set %v, want %v", tc.name, p.maps == before, tc.samePt)
		}
		if tc.want == "" && p.maps != nil {
			t.Errorf("%s: an empty set is not nil", tc.name)
		}
		if held(before) != beforeHeld {
			t.Errorf("%s: the old set was changed in place (a job may hold it)", tc.name)
		}
	}

	// Two panes (compare): both keep only the shown mode's maps, the hidden
	// one too.
	a, p := mapsApp(f, "pbr", "diffuse", "normal", "pbr")
	q := &pane{index: 1, kind: viewer.KindTask, vt: p.vt, stage: p.stage, modes: p.modes, status: "ready", maps: all("diffuse", "normal", "pbr")}
	p.maps = all("diffuse", "normal", "pbr")
	a.panes = append(a.panes, q)
	a.setMode("toon")
	if held(p.maps) != "diffuse" || held(q.maps) != "diffuse" {
		t.Fatalf("compare in toon holds %q and %q", held(p.maps), held(q.maps))
	}

	// A model file's maps came with it and stay.
	m := all("diffuse", "normal", "pbr", "shaded")
	a.panes = []*pane{{kind: viewer.KindModelFile, maps: m, status: "ready"}}
	a.setMode("ao")
	if a.panes[0].maps != m {
		t.Fatal("a model file's maps were dropped")
	}
}

// TestPaneMapsLateLoad: a load that ends after the mode changed keeps only
// what the mode shown then samples, and the next tick loads the rest; a
// load that ends after a stage or content change is dropped and leaves no
// load marked as running.
func TestPaneMapsLateLoad(t *testing.T) {
	f := newFakeMaps()
	a, p := mapsApp(f, "pbr", "diffuse", "normal", "pbr", "shaded")
	f.gate = make(chan struct{})
	tick(a, p)
	a.setMode("toon")
	close(f.gate)
	runPost(t, a)
	if held(p.maps) != "diffuse" || p.mapsWanted {
		t.Fatalf("pbr load ended in toon: holds %q, wanted %v", held(p.maps), p.mapsWanted)
	}
	if j := tick(a, p); j == nil || j.key.mode != "toon" {
		t.Fatal("toon did not draw after the late load")
	}

	f.gate = make(chan struct{})
	a.setMode("pbr")
	tick(a, p) // loads normal, pbr
	a.setMode("shaded")
	close(f.gate)
	runPost(t, a)
	if p.maps != nil {
		t.Fatalf("pbr load ended in shaded: holds %q, want nothing", held(p.maps))
	}
	f.gate = nil
	if j := tick(a, p); j != nil || !p.mapsWanted {
		t.Fatal("shaded did not load its map after the late load")
	}
	runPost(t, a)
	if held(p.maps) != "shaded" {
		t.Fatalf("shaded holds %q", held(p.maps))
	}

	// A stage change while a load runs: the load's result is dropped and
	// no load stays marked as running.
	f.gate = make(chan struct{})
	a.setMode("pbr")
	tick(a, p)
	if !p.mapsWanted {
		t.Fatal("no load started")
	}
	st := p.stage
	st.Name = "geometry" // a stage without a mesh: its load fails at once, without the service
	a.setStage(st)
	if p.mapsWanted || p.maps != nil || p.mapsFailed != nil {
		t.Fatalf("after setStage: wanted %v, holds %q, failed %v", p.mapsWanted, held(p.maps), p.mapsFailed)
	}
	close(f.gate)
	runPost(t, a) // the mesh load and the map load end, in either order
	runPost(t, a)
	if p.maps != nil || p.mapsWanted {
		t.Fatalf("the old stage's maps were taken: holds %q, wanted %v", held(p.maps), p.mapsWanted)
	}
	m, _ := traceQuad()
	p.status, p.mesh = "ready", m // the new stage's mesh is in
	f.gate = nil
	if tick(a, p); !p.mapsWanted {
		t.Fatal("no map load after the stage change (a load stayed marked as running)")
	}
	runPost(t, a)

	// New content while a load runs: dropped too.
	a.setMode("toon")
	f.gate = make(chan struct{})
	a.setMode("pbr")
	tick(a, p)
	a.contentGen++
	maps := p.maps
	close(f.gate)
	runPost(t, a)
	if p.maps != maps {
		t.Fatal("a load of content the view left was taken")
	}
}

// TestPaneMapsFailure (N11): a map that fails keeps the pane ready; the
// modes that sample it say which map failed and draw nothing, without
// reading it again and without holding screenshots; every other mode draws;
// choosing a mode again (the same one too) reads the map again.
func TestPaneMapsFailure(t *testing.T) {
	f := newFakeMaps()
	f.fail["normal"] = true
	a, p := mapsApp(f, "pbr", "diffuse", "normal", "pbr", "shaded")
	tick(a, p)
	runPost(t, a)
	if p.status != "ready" || p.mapsWanted {
		t.Fatalf("after a failed map: status %s, wanted %v", p.status, p.mapsWanted)
	}
	if held(p.maps) != "diffuse pbr" {
		t.Fatalf("holds %q, want the maps that loaded", held(p.maps))
	}
	e := mapsError(p, "pbr")
	if !strings.Contains(e, "mode pbr") || !strings.Contains(e, "normal map did not load") || !strings.Contains(e, "Disk read failed.") {
		t.Fatalf("error %q does not say which map failed", e)
	}
	st := a.snapshot()
	if ps := st.Panes[0]; ps.Status != "ready" || ps.Error != e {
		t.Fatalf("state pane: status %s, error %q", ps.Status, ps.Error)
	}
	if st.Quality != viewer.QualityFull {
		t.Fatalf("quality %s: a screenshot would wait for a mode that cannot draw", st.Quality)
	}
	reads := f.readCount()
	for range 3 {
		if j := tick(a, p); j != nil {
			t.Fatal("pbr drew without its normal map")
		}
	}
	if f.readCount() != reads || p.mapsWanted {
		t.Fatal("the failed map was read again without a mode change")
	}
	if mapsError(p, "toon") != "" || mapsError(p, "ao") != "" {
		t.Fatal("a mode that does not sample the normal map reports it")
	}

	// Untextured and diffuse-only modes draw; the mesh is usable.
	for _, m := range []string{"toon", "ao", "normal"} { // toon draws with the diffuse map pbr loaded
		a.setMode(m)
		if j := tick(a, p); j == nil || j.key.mode != m {
			t.Fatalf("mode %s did not draw after the failed map", m)
		}
	}
	if f.readCount() != reads {
		t.Fatal("a mode that does not sample the failed map read maps again")
	}

	// Back to pbr: read again; the map loads now.
	f.mu.Lock()
	f.fail["normal"] = false
	f.mu.Unlock()
	a.setMode("pbr")
	tick(a, p)
	if !p.mapsWanted {
		t.Fatal("pbr chosen again did not retry")
	}
	runPost(t, a)
	if j := tick(a, p); j == nil || held(j.maps) != "diffuse normal pbr" {
		t.Fatal("pbr did not draw after the retry")
	}

	// The same mode chosen again retries, by the user's mode bar and by a
	// control call.
	for _, choose := range []func(){
		func() { a.userMode("pbr") },
		func() { m := "pbr"; a.applyControl(viewer.Control{Mode: &m}) },
	} {
		p.maps, p.mapsFailed = nil, nil
		f.mu.Lock()
		f.fail["normal"] = true
		f.mu.Unlock()
		tick(a, p)
		runPost(t, a)
		if mapsError(p, "pbr") == "" {
			t.Fatal("the failure was not recorded")
		}
		f.mu.Lock()
		f.fail["normal"] = false
		f.mu.Unlock()
		choose()
		if mapsError(p, "pbr") != "" || a.snapshot().Panes[0].Error != "" {
			t.Fatal("choosing pbr again did not clear the failure")
		}
		if tick(a, p); !p.mapsWanted {
			t.Fatal("choosing pbr again did not retry")
		}
		runPost(t, a)
		if mapsError(p, "pbr") != "" || held(p.maps) != "diffuse normal pbr" {
			t.Fatalf("retry: holds %q, error %q", held(p.maps), mapsError(p, "pbr"))
		}
	}

	// A failure of a load that ran while the mode was chosen again is not
	// recorded: that choice asked for a new try.
	p.maps = nil
	f.mu.Lock()
	f.fail["normal"] = true
	f.mu.Unlock()
	f.gate = make(chan struct{})
	tick(a, p)
	a.setMode("pbr")
	close(f.gate)
	runPost(t, a)
	f.gate = nil
	if p.mapsFailed != nil {
		t.Fatal("a failure of a load older than the mode choice was recorded")
	}
	if tick(a, p); !p.mapsWanted {
		t.Fatal("no new try after the mode was chosen again")
	}
	runPost(t, a)
	if mapsError(p, "pbr") == "" {
		t.Fatal("the new try's failure was not recorded")
	}
}

// TestModelMapThumbs: a model file's textures panel lists each picture
// once, named by map, under its material when there are several (a
// material without a unique name by its number).
func TestModelMapThumbs(t *testing.T) {
	tex := func() *render3d.Texture { return &render3d.Texture{W: 1, H: 1, Pix: []uint8{1, 2, 3, 255}} }
	orm, base := tex(), tex()
	one := render3d.DefaultMaterial()
	one.Base, one.MetalRough, one.Occlusion = &render3d.TexRef{Tex: base}, &render3d.TexRef{Tex: orm}, &render3d.TexRef{Tex: orm}
	names := func(ts []*thumb) []string {
		var out []string
		for _, th := range ts {
			out = append(out, th.name)
		}
		return out
	}
	if got := names(modelMapThumbs([]*render3d.Material{one})); !slices.Equal(got, []string{"base_color", "metallic_roughness"}) {
		t.Errorf("one material: %v", got)
	}
	one.Name = "body"
	two := render3d.DefaultMaterial()
	two.Base, two.Emission = &render3d.TexRef{Tex: base}, &render3d.TexRef{Tex: tex()}
	three := render3d.DefaultMaterial()
	three.Name, three.Normal = "body", &render3d.TexRef{Tex: tex()}
	got := names(modelMapThumbs([]*render3d.Material{one, two, three}))
	if want := []string{"material 0/base_color", "material 0/metallic_roughness", "material 1/emissive", "material 2/normal"}; !slices.Equal(got, want) {
		t.Errorf("three materials: %v, want %v", got, want)
	}
}
