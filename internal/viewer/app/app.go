// Package app is the viewer window: the `rodin3d-mcp viewer` process (plan
// 2.11). An Ebitengine game loop owns the window and the view state; tool
// processes change it over the daemon/socket JSON-RPC methods of package
// viewer, the user changes it with the mouse and keys, and both see the same
// state and revision. 3D is drawn on the CPU by render3d on a worker
// goroutine (quick frames while the view moves, a full frame when it rests);
// every frame is composed into one offscreen image of window size, which is
// what the screen shows and what a screenshot reads.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/sairaph/mcp-wizard/daemon/socket"

	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/service"
	"github.com/sairaph/rodin3d-mcp/internal/viewer"
)

// cmdTimeout bounds how long a call waits for the game loop to take and
// answer it (plan 2.11: 2 s).
const cmdTimeout = 2 * time.Second

var errBusy = errors.New("the window did not take the call within 2 s (is it frozen or minimised?)")

// Run is the viewer subcommand: it opens the window and serves the socket
// until the window closes. Exit codes: 0 closed (or another window is
// already open), 1 failure, 3 no display.
func Run(ctx context.Context, svc *service.Service, version string) int {
	guardStderr()
	log.SetFlags(log.LstdFlags | log.LUTC)
	log.SetPrefix("viewer: ")
	if ok, why := viewer.DisplayAvailable(); !ok {
		log.Print(why)
		return 3
	}
	paths := svc.Paths()
	a := newApp(ctx, svc, version)
	srv := socket.New(paths.Viewer, viewer.SocketName)
	a.register(srv)
	if err := srv.Open(); err != nil {
		if strings.Contains(err.Error(), "already running") {
			log.Print("a viewer window is already open")
			return 0
		}
		log.Printf("open the control socket: %v", err)
		return 1
	}
	defer srv.Close()
	if err := os.WriteFile(paths.ViewerPID, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err == nil {
		defer os.Remove(paths.ViewerPID)
	}
	go func() {
		if err := srv.Serve(ctx); err != nil {
			log.Printf("control socket: %v", err)
		}
	}()
	log.Printf("started (pid %d, %s)", os.Getpid(), version)

	w, h := defaultWindowSize()
	ebiten.SetWindowSize(w, h)
	ebiten.SetWindowSizeLimits(640, 400, -1, -1)
	ebiten.SetWindowTitle("Rodin viewer")
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	ebiten.SetWindowClosingHandled(true)
	ebiten.SetRunnableOnUnfocused(true)
	ebiten.SetScreenClearedEveryFrame(false)
	ebiten.SetVsyncEnabled(true)
	err := ebiten.RunGame(a)
	a.rend.stop()
	a.saveCameras()
	if err != nil && !errors.Is(err, ebiten.Termination) {
		log.Printf("window: %v", err)
		return 1
	}
	log.Print("closed")
	return 0
}

// defaultWindowSize is 1280x800 points, or 90 % of a smaller screen.
func defaultWindowSize() (int, int) {
	w, h := 1280, 800
	if m := ebiten.Monitor(); m != nil {
		mw, mh := m.Size()
		if mw > 0 && mh > 0 {
			w, h = min(w, mw*9/10), min(h, mh*9/10)
		}
	}
	return w, h
}

// do runs fn on the game loop and returns what it returns.
func (a *App) do(ctx context.Context, fn func() any) (any, error) {
	done := make(chan any, 1)
	select {
	case a.cmds <- func() { done <- fn() }:
	case <-time.After(cmdTimeout):
		return nil, errBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case v := <-done:
		return v, nil
	case <-time.After(cmdTimeout):
		return nil, errBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func decode(params json.RawMessage, v any) error {
	if len(params) == 0 || string(params) == "null" {
		return nil
	}
	if err := json.Unmarshal(params, v); err != nil {
		return fmt.Errorf("bad parameters: %w", err)
	}
	return nil
}

// refusal is a reply that refuses a call with the unchanged state.
func (a *App) refusal(e *viewer.Error) viewer.StateReply {
	return viewer.StateReply{State: a.snapshot(), PID: os.Getpid(), Error: e}
}

func errorOf(err error) *viewer.Error {
	e := service.Classify(err)
	return &viewer.Error{Code: e.Code, Message: e.Message, Hint: e.Hint, Fields: e.Fields}
}

func (a *App) register(srv *socket.Server) {
	srv.Handle(viewer.MethodPing, func(ctx context.Context, _ json.RawMessage) (any, error) {
		return a.do(ctx, func() any {
			return viewer.PingReply{PID: os.Getpid(), Version: a.version, Revision: a.rev}
		})
	})
	srv.Handle(viewer.MethodShow, func(ctx context.Context, params json.RawMessage) (any, error) {
		var p viewer.ShowParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		prep, err := a.prepare(ctx, p.Content)
		if err != nil {
			return viewer.StateReply{PID: os.Getpid(), Error: errorOf(err)}, nil
		}
		return a.do(ctx, func() any { return a.applyShow(prep, p) })
	})
	srv.Handle(viewer.MethodControl, func(ctx context.Context, params json.RawMessage) (any, error) {
		var c viewer.Control
		if err := decode(params, &c); err != nil {
			return nil, err
		}
		return a.do(ctx, func() any { return a.applyControl(c) })
	})
	srv.Handle(viewer.MethodState, func(ctx context.Context, params json.RawMessage) (any, error) {
		var p viewer.StateParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		return a.do(ctx, func() any {
			return viewer.StateReply{State: a.snapshot(), Actions: a.actionsSince(p.SinceRevision), PID: os.Getpid()}
		})
	})
	srv.Handle(viewer.MethodCapture, func(ctx context.Context, params json.RawMessage) (any, error) {
		var p viewer.CaptureParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		wait := time.Duration(max(p.WaitMS, 0)) * time.Millisecond
		req := &captureReq{start: time.Now(), deadline: time.Now().Add(wait), done: make(chan viewer.CaptureReply, 1)}
		if _, err := a.do(ctx, func() any { a.captures = append(a.captures, req); a.dirty(); return nil }); err != nil {
			return nil, err
		}
		select {
		case r := <-req.done:
			return r, nil
		case <-time.After(wait + 5*time.Second):
			return nil, errBusy
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	srv.Handle(viewer.MethodRaise, func(ctx context.Context, _ json.RawMessage) (any, error) {
		return a.do(ctx, func() any {
			a.raise()
			return viewer.StateReply{State: a.snapshot(), PID: os.Getpid()}
		})
	})
	srv.Handle(viewer.MethodClose, func(ctx context.Context, _ json.RawMessage) (any, error) {
		return a.do(ctx, func() any {
			a.closing = true
			return viewer.StateReply{State: a.snapshot(), PID: os.Getpid()}
		})
	})
	registerTestMethods(a, srv)
}

// paths is the data folder of the service.
func (a *App) paths() domain.Paths { return a.svc.Paths() }
