package app

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sairaph/rodin3d-mcp/internal/render3d"
	"github.com/sairaph/rodin3d-mcp/internal/service"
	"github.com/sairaph/rodin3d-mcp/internal/viewer"
)

// TestFrameSize covers every bound of a pane picture: small full frames
// supersample, large ones do not, sides stop at 4096, quick frames never
// supersample, and tracer pictures at rest stop at tracePixels.
func TestFrameSize(t *testing.T) {
	for _, tc := range []struct {
		name          string
		w, h          int
		interactive   bool
		mode          string
		ww, wh, wss   int
		maxPixelsOnly bool
	}{
		{"small full", 1728, 972, false, "pbr", 1728, 972, 2, false},
		{"large full drops SS", 3840, 2000, false, "pbr", 3840, 2000, 1, false},
		{"wide pane capped", 8192, 2000, false, "pbr", 4096, 1000, 1, false},
		{"quick", 864, 486, true, "pbr", 864, 486, 1, false},
		{"tracer at rest capped", 1728, 972, false, "tracer", 0, 0, 1, true},
		{"tracer small kept", 600, 400, false, "tracer", 600, 400, 2, false},
		{"tracer quick not capped", 864, 486, true, "tracer", 864, 486, 1, false},
	} {
		w, h, ss := frameSize(tc.w, tc.h, tc.interactive, tc.mode)
		if tc.maxPixelsOnly {
			if w*h > tracePixels || w*h < tracePixels*9/10 || float64(w)/float64(h) < 1.7 || float64(w)/float64(h) > 1.8 {
				t.Errorf("%s: %dx%d, want about %d pixels at the pane's aspect", tc.name, w, h, tracePixels)
			}
			continue
		}
		if w != tc.ww || h != tc.wh || ss != tc.wss {
			t.Errorf("%s: %dx%d ss %d, want %dx%d ss %d", tc.name, w, h, ss, tc.ww, tc.wh, tc.wss)
		}
	}
	_ = render3d.ModeTracer
}

// TestSetSaver: the newest content of a set wins, every waiter gets the
// write's result, a failed write is reported, flush waits for the queue.
func TestSetSaver(t *testing.T) {
	var mu sync.Mutex
	var written []string
	block := make(chan struct{})
	failID := "is-bad"
	var reported []string
	s := newSetSaver(func(set service.InputSet) error {
		<-block
		mu.Lock()
		written = append(written, set.ID+":"+set.Images[0].View)
		mu.Unlock()
		if set.ID == failID {
			return errors.New("disk full")
		}
		return nil
	}, func(id string, err error) {
		mu.Lock()
		reported = append(reported, id)
		mu.Unlock()
	})
	set := func(id, view string) service.InputSet {
		return service.InputSet{ID: id, Images: []viewer.InputImage{{Path: "x", View: view}}}
	}
	first := s.save(set("is-a", "F")) // taken by the loop and blocked in write
	time.Sleep(20 * time.Millisecond)
	second := s.save(set("is-a", "B"))
	third := s.save(set("is-a", "L")) // replaces B before its write starts
	bad := s.save(set(failID, "R"))
	if s.flush(10 * time.Millisecond) {
		t.Fatal("flush reported done while writes were blocked")
	}
	close(block)
	for i, ch := range []<-chan error{first, second, third} {
		if err := <-ch; err != nil {
			t.Fatalf("waiter %d: %v", i, err)
		}
	}
	if err := <-bad; err == nil {
		t.Fatal("the failed write reported no error")
	}
	if !s.flush(time.Second) {
		t.Fatal("flush timed out with nothing queued")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(written) != 3 || written[0] != "is-a:F" || written[1] != "is-a:L" || written[2] != "is-bad:R" {
		t.Fatalf("writes %v, want is-a:F, is-a:L, is-bad:R", written)
	}
	if len(reported) != 1 || reported[0] != failID {
		t.Fatalf("reported %v", reported)
	}
}

// TestActionsTruncated: past maxActions the oldest actions are dropped and
// a read from before them says so.
func TestActionsTruncated(t *testing.T) {
	a := &App{}
	for i := 0; i < maxActions+5; i++ {
		a.userAction("mode", "pbr")
	}
	acts, trunc := a.actionsSince(0)
	if len(acts) != maxActions || !trunc {
		t.Fatalf("since 0: %d actions, truncated %v; want %d, true", len(acts), trunc, maxActions)
	}
	if _, trunc := a.actionsSince(a.droppedRev); trunc {
		t.Fatal("since the last dropped revision: want not truncated")
	}
	if acts, trunc := a.actionsSince(a.rev); len(acts) != 0 || trunc {
		t.Fatalf("since now: %d actions, truncated %v", len(acts), trunc)
	}
	b := &App{}
	b.userAction("mode", "pbr")
	if _, trunc := b.actionsSince(0); trunc {
		t.Fatal("nothing dropped: want not truncated")
	}
}

// testInputs is an editable set of n images without weights, saved by a
// saver that records each write.
func testInputs(n int) (*App, *[]service.InputSet) {
	var writes []service.InputSet
	var mu sync.Mutex
	a := &App{inputs: &inputModel{id: "is-t", editable: true}}
	a.saver = newSetSaver(func(s service.InputSet) error {
		mu.Lock()
		writes = append(writes, s)
		mu.Unlock()
		return nil
	}, nil)
	for i := 0; i < n; i++ {
		a.inputs.items = append(a.inputs.items, &thumb{kind: "input", file: "f", view: "?"})
	}
	return a, &writes
}

// TestWeightEdits: the first edit of a set without weights gives every
// image its even share, the edited one moves by the step, and the -
// button stops at minWeight; the stored set has a weight on every image.
func TestWeightEdits(t *testing.T) {
	a, writes := testInputs(3)
	a.userWeight(1, 0.05)
	want := []float64{0.33, 0.4, 0.33}
	for i, it := range a.inputs.items {
		if it.weight == nil || *it.weight != want[i] {
			t.Fatalf("after +: image %d weight %v, want %v", i, it.weight, want[i])
		}
	}
	for i := 0; i < 20; i++ {
		a.userWeight(0, -0.05)
	}
	if w := *a.inputs.items[0].weight; w != minWeight {
		t.Fatalf("after many -: weight %v, want %v", w, minWeight)
	}
	if !a.saver.flush(time.Second) {
		t.Fatal("saves did not finish")
	}
	last := (*writes)[len(*writes)-1]
	if err := service.CheckInputSetWeights(last.Images); err != nil {
		t.Fatalf("the stored set is not one generate_model takes: %v", err)
	}
}

// TestSetTagWeight: an agent weight on a set without weights fills the
// others with the even share; a weight of 0 or above 1 is refused and
// changes nothing.
func TestSetTagWeight(t *testing.T) {
	for _, bad := range []float64{0, -0.1, 1.5} {
		a, _ := testInputs(2)
		w := bad
		rep := a.applyControl(viewer.Control{SetTag: &viewer.SetTag{ImageIndex: 0, View: "F", Weight: &w}})
		if rep.Error == nil || rep.Error.Code != "invalid_input" {
			t.Fatalf("weight %v: reply error %+v, want invalid_input", bad, rep.Error)
		}
		if a.inputs.items[0].weight != nil || a.inputs.items[0].view != "?" {
			t.Fatalf("weight %v: the refused call changed the set", bad)
		}
	}
	a, writes := testInputs(4)
	w := 0.5
	rep := a.applyControl(viewer.Control{SetTag: &viewer.SetTag{ImageIndex: 2, View: "B", Weight: &w}})
	if rep.Error != nil {
		t.Fatalf("set_tag: %+v", rep.Error)
	}
	want := []float64{0.25, 0.25, 0.5, 0.25}
	for i, it := range a.inputs.items {
		if it.weight == nil || *it.weight != want[i] {
			t.Fatalf("image %d weight %v, want %v", i, it.weight, want[i])
		}
	}
	if !a.saver.flush(time.Second) || len(*writes) != 1 {
		t.Fatalf("writes %d, want 1", len(*writes))
	}
	if err := service.CheckInputSetWeights((*writes)[0].Images); err != nil {
		t.Fatalf("stored set: %v", err)
	}
	// Without a weight, set_tag leaves the weights alone.
	b, _ := testInputs(2)
	if rep := b.applyControl(viewer.Control{SetTag: &viewer.SetTag{ImageIndex: 0, View: "F"}}); rep.Error != nil || b.inputs.items[1].weight != nil {
		t.Fatalf("retag only: %+v, weight %v", rep.Error, b.inputs.items[1].weight)
	}
}

// TestStaleRevisionFields: a call against an older revision is refused
// with the revision, the user's actions since it only when there are any
// (never null), and the truncation flag only when actions were dropped; a
// call at the current revision is not refused.
func TestStaleRevisionFields(t *testing.T) {
	a := &App{}
	a.bump()
	a.bump() // changes by tool calls: no user action
	at := func(rev int64) (map[string]any, bool) {
		rep, stale := a.staleRevision(&rev)
		if !stale {
			return nil, false
		}
		if rep.Error == nil || rep.Error.Code != "conflict" {
			t.Fatalf("since %d: refusal %+v, want conflict", rev, rep.Error)
		}
		b, err := json.Marshal(rep.Error.Fields)
		if err != nil || strings.Contains(string(b), "null") {
			t.Fatalf("since %d: fields %s (%v)", rev, b, err)
		}
		return rep.Error.Fields, true
	}
	if _, stale := at(a.rev); stale {
		t.Fatal("current revision: refused")
	}
	f, _ := at(0)
	if _, ok := f["user_actions"]; ok || f["revision"] != a.rev {
		t.Fatalf("no user actions: fields %v", f)
	}
	a.userAction("mode", "pbr")
	f, _ = at(0)
	if acts, ok := f["user_actions"].([]viewer.UserAction); !ok || len(acts) != 1 || acts[0].Action != "mode" {
		t.Fatalf("one user action: fields %v", f)
	}
	if _, ok := f["user_actions_truncated"]; ok {
		t.Fatalf("nothing dropped: fields %v", f)
	}
	for i := 0; i < maxActions; i++ {
		a.userAction("mode", "pbr")
	}
	if f, _ = at(0); f["user_actions_truncated"] != true {
		t.Fatalf("actions dropped: fields %v", f)
	}
	if f, _ = at(a.rev - 1); len(f["user_actions"].([]viewer.UserAction)) != 1 {
		t.Fatalf("since the last but one revision: fields %v", f)
	}
}

// TestViewNamed: a camera that is exactly a named view (direction, zoom 1,
// no pan) is reported by that name, whether a control call or the user set
// it; any other camera is custom.
func TestViewNamed(t *testing.T) {
	for _, v := range viewer.Views {
		c, _ := namedCam(v)
		if got := viewNamed(c); got != v {
			t.Errorf("camera of %s named %s", v, got)
		}
	}
	for _, tc := range []struct {
		c    camState
		want string
	}{
		{camState{Az: 45, El: 20, Zoom: 1}, "three_quarter"},
		{camState{Az: 0, El: 0, Zoom: 1}, "front"},
		{camState{Az: 180, El: 0, Zoom: 1}, "back"},
		{camState{Az: -180, El: 0, Zoom: 1}, "back"},
		{camState{Az: 90, El: 0, Zoom: 1}, "right"},
		{camState{Az: -90, El: 0, Zoom: 1}, "left"},
		{camState{Az: 0, El: 90, Zoom: 1}, "top"},
		{camState{Az: 45.01, El: 20, Zoom: 1}, "custom"},
		{camState{Az: 45, El: 20, Zoom: 1.5}, "custom"},
		{camState{Az: 45, El: 20, Zoom: 1, Pan: [2]float64{0.1, 0}}, "custom"},
	} {
		if got := viewNamed(tc.c); got != tc.want {
			t.Errorf("%v named %s, want %s", tc.c, got, tc.want)
		}
	}

	a := &App{panes: []*pane{{}}, cam: defaultCam(), viewName: "three_quarter"}
	deg := func(x float64) *float64 { return &x }
	for _, tc := range []struct {
		c    viewer.Control
		want string
	}{
		{viewer.Control{Orbit: &viewer.Orbit{AzimuthDeg: deg(10), ElevationDeg: deg(5)}}, "custom"},
		{viewer.Control{Orbit: &viewer.Orbit{AzimuthDeg: deg(45), ElevationDeg: deg(20)}}, "three_quarter"},
		{viewer.Control{Zoom: deg(2)}, "custom"},
		{viewer.Control{Zoom: deg(1)}, "three_quarter"},
		{viewer.Control{Orbit: &viewer.Orbit{AzimuthDeg: deg(0), ElevationDeg: deg(0)}}, "front"},
		{viewer.Control{Pan: &viewer.Pan{X: 0.5}}, "custom"},
	} {
		rep := a.applyControl(tc.c)
		if rep.Error != nil || rep.State.View != tc.want {
			t.Fatalf("control %+v: view %q (error %+v), want %s", tc.c, rep.State.View, rep.Error, tc.want)
		}
	}
	// The user: a drag that ends on the front camera.
	a.cam, a.viewName = camState{Az: 0, El: 0, Zoom: 1}, "custom"
	a.userAction("camera", "orbit")
	if a.viewName != "front" {
		t.Fatalf("user drag to the front camera: view %s", a.viewName)
	}
}

// TestMissingMaps: a task pane loads the maps its mode samples and has not
// loaded yet (PBR and Tracer: diffuse, normal, pbr; Shaded: shaded, or
// diffuse without one; Toon: diffuse; other modes: none); a model file's
// maps came with it.
func TestMissingMaps(t *testing.T) {
	stage := func(kinds ...string) service.ViewStage {
		st := service.ViewStage{Name: "material"}
		for _, k := range kinds {
			st.Maps = append(st.Maps, service.ViewAsset{Mode: "texture_" + k})
		}
		return st
	}
	tex := &render3d.Texture{W: 1, H: 1, Pix: []uint8{0, 0, 0, 255}}
	all := stage("diffuse", "normal", "pbr", "shaded")
	for _, tc := range []struct {
		name string
		p    *pane
		mode string
		want string
	}{
		{"pbr", &pane{kind: viewer.KindTask, stage: all}, "pbr", "diffuse normal pbr"},
		{"tracer", &pane{kind: viewer.KindTask, stage: all}, "tracer", "diffuse normal pbr"},
		{"shaded", &pane{kind: viewer.KindTask, stage: all}, "shaded", "shaded"},
		{"shaded without a shaded map", &pane{kind: viewer.KindTask, stage: stage("diffuse", "normal", "pbr")}, "shaded", "diffuse"},
		{"toon", &pane{kind: viewer.KindTask, stage: all}, "toon", "diffuse"},
		{"untextured mode", &pane{kind: viewer.KindTask, stage: all}, "ao", ""},
		{"stage without a normal map", &pane{kind: viewer.KindTask, stage: stage("diffuse", "pbr")}, "pbr", "diffuse pbr"},
		{"loaded for pbr, now shaded", &pane{kind: viewer.KindTask, stage: all, maps: &render3d.Maps{Diffuse: tex, Normal: tex, PBR: tex}}, "shaded", "shaded"},
		{"loaded for shaded, now pbr", &pane{kind: viewer.KindTask, stage: all, maps: &render3d.Maps{Shaded: tex}}, "pbr", "diffuse normal pbr"},
		{"loaded for pbr, now toon", &pane{kind: viewer.KindTask, stage: all, maps: &render3d.Maps{Diffuse: tex, Normal: tex, PBR: tex}}, "toon", ""},
		{"model file", &pane{kind: viewer.KindModelFile}, "pbr", ""},
	} {
		if got := strings.Join(missingMaps(tc.p, tc.mode), " "); got != tc.want {
			t.Errorf("%s: missing %q, want %q", tc.name, got, tc.want)
		}
	}
	if n := mapCount(&render3d.Maps{Diffuse: tex, Shaded: tex}); n != 2 {
		t.Errorf("mapCount %d, want 2", n)
	}
}

// TestShowCamera (N14): a show without a view uses the task's remembered
// camera and names it as it is: the named view it equals, else custom; a
// show with a view uses that view.
func TestShowCamera(t *testing.T) {
	front, _ := namedCam("front")
	orbited := camState{Az: 10, El: 5, Zoom: 1.4}
	for _, tc := range []struct {
		name     string
		kind     string
		cameras  map[string]camState
		cam      *camState
		view     string
		wantCam  camState
		wantName string
	}{
		{"remembered custom camera", viewer.KindTask, map[string]camState{"t1": orbited}, nil, "", orbited, "custom"},
		{"remembered named camera", viewer.KindTask, map[string]camState{"t1": front}, nil, "", front, "front"},
		{"nothing remembered", viewer.KindTask, map[string]camState{}, nil, "", defaultCam(), "three_quarter"},
		{"a model file", viewer.KindModelFile, map[string]camState{"t1": orbited}, nil, "", defaultCam(), "three_quarter"},
		{"a view asked for", viewer.KindTask, map[string]camState{"t1": orbited}, &front, "front", front, "front"},
	} {
		p := &pane{kind: tc.kind}
		if tc.kind == viewer.KindTask {
			p.vt = &service.ViewTask{UUID: "t1"}
		}
		a := &App{kind: tc.kind, panes: []*pane{p}, cameras: tc.cameras, cam: orbited, viewName: "custom"}
		a.showCamera(tc.cam, tc.view)
		if a.cam != tc.wantCam || a.viewName != tc.wantName {
			t.Errorf("%s: camera %v named %s, want %v named %s", tc.name, a.cam, a.viewName, tc.wantCam, tc.wantName)
		}
	}
}
