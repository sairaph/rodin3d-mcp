//go:build viewertest

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/sairaph/mcp-wizard/daemon/socket"

	"github.com/sairaph/rodin3d-mcp/internal/viewer"
)

// Test-only input injection (plan P2v: inject_input, build tag viewertest):
// scripted mouse and key events replace the real input for the ticks they
// last, so a live test drives the window as a user would.

type testHooks struct {
	queue   []frameInput
	held    [3]bool
	x, y    int
	drained chan struct{} // closed when the queue runs empty
}

func (a *App) injected() (frameInput, bool) {
	t := &a.test
	if len(t.queue) == 0 {
		if t.drained != nil {
			close(t.drained)
			t.drained = nil
		}
		return frameInput{}, false
	}
	f := t.queue[0]
	t.queue = t.queue[1:]
	return f, true
}

var buttons = map[string]int{"": 0, "left": 0, "right": 1, "middle": 2}

var keys = map[string]ebiten.Key{"escape": ebiten.KeyEscape, "r": ebiten.KeyR, "t": ebiten.KeyT, "w": ebiten.KeyW, "f": ebiten.KeyF}

// point resolves an event position: a widget's or region's centre, or x, y.
func (a *App) point(target string, x, y float64) (int, int, error) {
	if target == "" {
		return int(x), int(y), nil
	}
	r, ok := a.ui.export()[target]
	if !ok {
		return 0, 0, fmt.Errorf("no widget %q on screen", target)
	}
	return r[0] + r[2]/2, r[1] + r[3]/2, nil
}

// expand turns one event into ticks of input (game loop).
func (a *App) expand(e viewer.InjectEvent) error {
	t := &a.test
	b, ok := buttons[e.Button]
	if !ok {
		return fmt.Errorf("unknown button %q", e.Button)
	}
	frame := func(x, y int, held [3]bool) frameInput {
		return frameInput{x: x, y: y, down: held, shift: e.Key == "shift"}
	}
	push := func(f frameInput) { t.queue = append(t.queue, f); t.x, t.y = f.x, f.y }
	switch e.Type {
	case "move", "down", "up", "click", "wheel":
		x, y, err := a.point(e.Target, e.X, e.Y)
		if err != nil {
			return err
		}
		switch e.Type {
		case "move":
			push(frame(x, y, t.held))
		case "down":
			push(frame(x, y, t.held))
			t.held[b] = true
			push(frame(x, y, t.held))
		case "up":
			t.held[b] = false
			push(frame(x, y, t.held))
		case "click":
			push(frame(x, y, t.held))
			h := t.held
			h[b] = true
			push(frame(x, y, h))
			push(frame(x, y, t.held))
		case "wheel":
			push(frame(x, y, t.held))
			f := frame(x, y, t.held)
			f.wheel = e.DY
			push(f)
		}
	case "drag":
		x0, y0, err := a.point(e.Target, e.X, e.Y)
		if err != nil {
			return err
		}
		x1, y1, err := a.point(e.To, e.ToX, e.ToY)
		if err != nil {
			return err
		}
		n := e.Frames
		if n <= 0 {
			n = 12
		}
		push(frame(x0, y0, t.held))
		h := t.held
		h[b] = true
		push(frame(x0, y0, h))
		for k := 1; k <= n; k++ {
			f := float64(k) / float64(n)
			push(frame(x0+int(float64(x1-x0)*f), y0+int(float64(y1-y0)*f), h))
		}
		push(frame(x1, y1, t.held))
	case "key":
		k, ok := keys[strings.ToLower(e.Key)]
		if !ok {
			return fmt.Errorf("unknown key %q", e.Key)
		}
		f := frame(t.x, t.y, t.held)
		f.keys = []ebiten.Key{k}
		push(f)
	case "wait":
		n := max(e.Frames, 1)
		for k := 0; k < n; k++ {
			push(frame(t.x, t.y, t.held))
		}
	default:
		return fmt.Errorf("unknown event type %q", e.Type)
	}
	return nil
}

func registerTestMethods(a *App, srv *socket.Server) {
	srv.Handle(viewer.MethodInject, func(ctx context.Context, params json.RawMessage) (any, error) {
		var p viewer.InjectParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		for i, e := range p.Events {
			v, err := a.do(ctx, func() any {
				if err := a.expand(e); err != nil {
					return err
				}
				d := make(chan struct{})
				a.test.drained = d
				return d
			})
			if err != nil {
				return nil, err
			}
			if err, ok := v.(error); ok {
				return viewer.InjectReply{Error: &viewer.Error{Code: "invalid_input", Message: fmt.Sprintf("event %d: %v", i, err)}}, nil
			}
			select {
			case <-v.(chan struct{}):
			case <-time.After(10 * time.Second):
				return nil, fmt.Errorf("event %d did not finish within 10 s", i)
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		v, err := a.do(ctx, func() any { return viewer.InjectReply{Widgets: a.ui.export(), State: a.snapshot()} })
		if err != nil {
			return nil, err
		}
		return v, nil
	})
}
