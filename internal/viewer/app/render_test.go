package app

import (
	"errors"
	"testing"
	"time"

	"github.com/sairaph/rodin3d-mcp/internal/render3d"
)

// traceQuad is a textured quad: a tracer pass over 400x400 pixels takes
// long enough (tens of milliseconds per sample) for a job to be submitted
// while it runs.
func traceQuad() (*render3d.Mesh, *render3d.Maps) {
	m := &render3d.Mesh{
		Pos: [][3]float32{{-1, -1, 0}, {1, -1, 0}, {1, 1, 0}, {-1, 1, 0}},
		Nrm: [][3]float32{{0, 0, 1}, {0, 0, 1}, {0, 0, 1}, {0, 0, 1}},
		UV:  [][2]float32{{0, 1}, {1, 1}, {1, 0}, {0, 0}},
		Idx: []uint32{0, 1, 2, 0, 2, 3},
	}
	maps := &render3d.Maps{
		Diffuse: &render3d.Texture{W: 1, H: 1, Pix: []uint8{200, 120, 60, 255}, SRGB: true},
		PBR:     &render3d.Texture{W: 1, H: 1, Pix: []uint8{0, 128, 0, 255}},
	}
	return m, maps
}

func job(p *pane, mode string, interactive bool) *renderJob {
	m, maps := traceQuad()
	cam, _ := render3d.ViewFront.Camera()
	return &renderJob{p: p, mesh: m, maps: maps, frame: render3d.FrameOf(m),
		key: renderKey{mode: mode, cam: cam, w: 400, h: 400, interactive: interactive, maps: mapCount(maps)}}
}

func recv(t *testing.T, r *renderer) renderDone {
	t.Helper()
	select {
	case d := <-r.out:
		return d
	case <-time.After(60 * time.Second):
		t.Fatal("no render result within 60 s")
		return renderDone{}
	}
}

// TestRendererCancels covers what may cancel a tracer job at rest: before
// its full quality only a quick job or a newer job of its own pane does
// (another pane's full job waits until the trace is full); once it refines
// past full quality, any submitted job does, and the pane keeps the full
// picture it was sent.
func TestRendererCancels(t *testing.T) {
	for _, tc := range []struct {
		name         string
		afterFull    bool   // submit once the full picture came, else after the first pass
		samePane     bool   // the new job is for the tracing pane
		mode         string // of the new job
		interactive  bool
		wantCanceled bool
	}{
		{"other pane, full job, before full", false, false, "normal", false, false},
		{"other pane, quick job, before full", false, false, "normal", true, true},
		{"same pane, newer full job, before full", false, true, "tracer", false, true},
		{"other pane, full job, refining", true, false, "normal", false, true},
		{"other pane, quick job, refining", true, false, "normal", true, true},
		{"same pane, newer full job, refining", true, true, "tracer", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRenderer()
			defer r.stop()
			tracing, other := &pane{}, &pane{}
			r.submit(job(tracing, "tracer", false))
			for {
				d := recv(t, r)
				if d.err != nil {
					t.Fatalf("trace failed before the new job: %v", d.err)
				}
				if !tc.afterFull || d.full {
					break
				}
			}
			np := other
			if tc.samePane {
				np = tracing
			}
			nj := job(np, tc.mode, tc.interactive)
			nj.key.gen = 1 // a newer key than the running job's
			r.submit(nj)
			// Results until the new job's own: the trace's last one shows
			// whether it was cancelled.
			var last renderDone
			for {
				d := recv(t, r)
				if d.job == nj {
					break
				}
				last = d
			}
			canceled := errors.Is(last.err, render3d.ErrCanceled)
			if canceled != tc.wantCanceled {
				t.Fatalf("trace cancelled %v, want %v (last result: full %v, err %v)", canceled, tc.wantCanceled, last.full, last.err)
			}
			if !canceled && (!last.full || last.err != nil) {
				t.Fatalf("the trace ended without its full picture: full %v, err %v", last.full, last.err)
			}
		})
	}
}
