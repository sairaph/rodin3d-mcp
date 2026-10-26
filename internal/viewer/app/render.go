package app

import (
	"errors"
	"image"
	"log"
	"math"
	"sync"
	"time"

	"github.com/sairaph/rodin3d-mcp/internal/render3d"
)

// Progressive rendering (plan 2.11, notes 41 decision V1): while the view
// moves (a drag, a wheel, the turntable, any change in the last 150 ms) a
// pane gets quick frames at 1/div of its size from the simplified mesh,
// SS 1, no SAO; when it rests, one full frame at its size from the full
// mesh, SS 2. div starts at 2 and steps up to 4 while quick frames take more
// than 45 ms, back down under 20 ms.
//
// The tracer mode's quick frames are one sample per pixel; at rest it is
// traced progressively (plan 2.11 Modes, M15): at most tracePixels pixels
// (scaled into the pane), shown after 1, 2, 4, ... samples per pixel, full
// quality at traceFull, then refined up to traceMax while no other pane
// waits for the renderer.
const (
	settleDelay     = 150 * time.Millisecond
	simplifyTarget  = 200000
	quickSlow       = 45 * time.Millisecond
	quickFast       = 20 * time.Millisecond
	maxQuickDivisor = 4

	tracePixels = 1 << 19
	traceFull   = 32
	traceMax    = 128
)

// renderKey is everything a pane's picture depends on.
type renderKey struct {
	mode        string
	cam         render3d.Camera
	w, h        int
	bg          string
	overlay     bool
	interactive bool
	gen         uint64
	maps        bool
}

type renderJob struct {
	p     *pane
	key   renderKey
	mesh  *render3d.Mesh
	maps  *render3d.Maps
	frame render3d.Frame
	draft bool
}

type renderDone struct {
	job  *renderJob
	img  *image.NRGBA
	took time.Duration
	err  error
	// full: the picture has its full quality (a tracer picture of fewer
	// than traceFull samples does not).
	full bool
}

// renderer runs one render at a time on its own goroutine; render3d uses
// every core for it. Each pane has at most one queued job (the newest); a
// quick job is taken before full ones and cancels a full render in flight.
type renderer struct {
	mu      sync.Mutex
	queue   []*renderJob
	running *renderJob
	cancel  chan struct{}
	wake    chan struct{}
	out     chan renderDone
	quit    chan struct{}
}

func newRenderer() *renderer {
	r := &renderer{wake: make(chan struct{}, 1), out: make(chan renderDone, 8), quit: make(chan struct{})}
	go r.loop()
	return r
}

func (r *renderer) poke() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// submit queues j, replacing the pane's queued job.
func (r *renderer) submit(j *renderJob) {
	r.mu.Lock()
	kept := r.queue[:0]
	for _, q := range r.queue {
		if q.p != j.p {
			kept = append(kept, q)
		}
	}
	r.queue = append(kept, j)
	// A full render in flight gives way to any quick job (the user moves
	// the view) and to a newer job of its own pane (its picture is no
	// longer wanted).
	if r.running != nil && !r.running.key.interactive && r.cancel != nil && (j.key.interactive || r.running.p == j.p) {
		close(r.cancel)
		r.cancel = nil
	}
	r.mu.Unlock()
	r.poke()
}

// drop forgets the pane's queued job and cancels its render in flight.
func (r *renderer) drop(p *pane) {
	r.mu.Lock()
	kept := r.queue[:0]
	for _, q := range r.queue {
		if q.p != p {
			kept = append(kept, q)
		}
	}
	r.queue = kept
	if r.running != nil && r.running.p == p && r.cancel != nil {
		close(r.cancel)
		r.cancel = nil
	}
	r.mu.Unlock()
}

// dropAll forgets every job (new content).
func (r *renderer) dropAll() {
	r.mu.Lock()
	r.queue = nil
	if r.running != nil && r.cancel != nil {
		close(r.cancel)
		r.cancel = nil
	}
	r.mu.Unlock()
}

func (r *renderer) stop() {
	r.dropAll()
	close(r.quit)
}

func (r *renderer) next() (*renderJob, chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.queue) == 0 {
		return nil, nil
	}
	pick := 0
	for i, q := range r.queue {
		if q.key.interactive {
			pick = i
			break
		}
	}
	j := r.queue[pick]
	r.queue = append(r.queue[:pick], r.queue[pick+1:]...)
	r.running = j
	r.cancel = make(chan struct{})
	return j, r.cancel
}

func (r *renderer) loop() {
	for {
		select {
		case <-r.quit:
			return
		case <-r.wake:
		}
		for {
			j, cancel := r.next()
			if j == nil {
				break
			}
			res := r.run(j, cancel)
			r.mu.Lock()
			r.running = nil
			r.mu.Unlock()
			// A cancelled render is dropped; a cancelled trace is reported, as
			// its pane may show a partial picture that must be traced again.
			if errors.Is(res.err, render3d.ErrCanceled) && (j.key.interactive || j.key.mode != string(render3d.ModeTracer)) {
				continue
			}
			select {
			case r.out <- res:
			case <-r.quit:
				return
			}
		}
	}
}

var backgrounds = map[string]*[3]uint8{"light": {226, 229, 234}, "dark": {22, 24, 28}}

// Frame bounds: render3d draws at most maxFrameSide pixels per side, and a
// full frame takes supersampling 2 only up to maxFullSamples samples (a
// sample costs 32 bytes), so a large screen does not need gigabytes per
// frame. A smaller picture is scaled into the pane when drawn.
const (
	maxFrameSide   = 4096
	maxFullSamples = 8 << 20
)

// frameSize is the render size and supersampling of a w x h pane picture
// in mode.
func frameSize(w, h int, interactive bool, mode string) (int, int, int) {
	if m := max(w, h); m > maxFrameSide {
		w, h = max(w*maxFrameSide/m, 16), max(h*maxFrameSide/m, 16)
	}
	if mode == string(render3d.ModeTracer) && !interactive && w*h > tracePixels {
		k := math.Sqrt(float64(tracePixels) / float64(w*h))
		w, h = max(int(float64(w)*k), 16), max(int(float64(h)*k), 16)
	}
	ss := 2
	if interactive || w*h*ss*ss > maxFullSamples {
		ss = 1
	}
	return w, h, ss
}

func (r *renderer) run(j *renderJob, cancel chan struct{}) (res renderDone) {
	res.job = j
	defer func() {
		if v := recover(); v != nil {
			log.Printf("render panicked: %v", v)
			res.err = errors.New("the renderer failed")
		}
	}()
	k := j.key
	cam := k.cam
	w, h, ss := frameSize(k.w, k.h, k.interactive, k.mode)
	o := render3d.Options{Mode: render3d.Mode(k.mode), Width: w, Height: h, Camera: &cam, Frame: &j.frame, Draft: j.draft,
		Background: backgrounds[k.bg], WireOverlay: k.overlay, Cancel: cancel, SS: ss, Interactive: k.interactive}
	t0 := time.Now()
	if o.Mode == render3d.ModeTracer && !k.interactive {
		return r.trace(j, o)
	}
	res.img, _, res.err = render3d.Render(j.mesh, j.maps, o)
	res.took = time.Since(t0)
	res.full = true
	return res
}

// trace draws a tracer picture progressively: each pass doubles the
// samples per pixel and is shown; the picture is full at traceFull and is
// refined up to traceMax unless another pane waits for the renderer. The
// last picture is returned; a pass the caller cancels ends it.
func (r *renderer) trace(j *renderJob, o render3d.Options) renderDone {
	t, err := render3d.NewTrace(j.mesh, j.maps, o)
	if err != nil {
		return renderDone{job: j, err: err}
	}
	for n := 1; ; n *= 2 {
		if err := t.Add(n - t.Samples()); err != nil {
			return renderDone{job: j, err: err}
		}
		res := renderDone{job: j, img: t.Image(), full: t.Samples() >= traceFull}
		if t.Samples() >= traceMax || (res.full && r.othersWait()) {
			return res
		}
		select {
		case r.out <- res:
		case <-r.quit:
			return renderDone{job: j, err: render3d.ErrCanceled}
		}
	}
}

// othersWait reports queued jobs.
func (r *renderer) othersWait() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.queue) > 0
}
