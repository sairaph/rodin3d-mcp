package app

import (
	"errors"
	"image"
	"log"
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
const (
	settleDelay     = 150 * time.Millisecond
	simplifyTarget  = 200000
	quickSlow       = 45 * time.Millisecond
	quickFast       = 20 * time.Millisecond
	maxQuickDivisor = 4
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
	if j.key.interactive && r.running != nil && !r.running.key.interactive && r.cancel != nil {
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
			if errors.Is(res.err, render3d.ErrCanceled) {
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
	o := render3d.Options{Mode: render3d.Mode(k.mode), Width: k.w, Height: k.h, Camera: &cam, Frame: &j.frame, Draft: j.draft,
		Background: backgrounds[k.bg], WireOverlay: k.overlay, Cancel: cancel, SS: 2}
	if k.interactive {
		o.SS, o.Interactive = 1, true
	}
	t0 := time.Now()
	res.img, _, res.err = render3d.Render(j.mesh, j.maps, o)
	res.took = time.Since(t0)
	return res
}
