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
	"sync/atomic"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/sairaph/mcp-wizard/daemon/socket"
	"github.com/sairaph/mcp-wizard/render"

	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/service"
	"github.com/sairaph/rodin3d-mcp/internal/viewer"
)

// cmdTimeout bounds how long a call waits for the game loop to take it
// (plan 2.11: 2 s). A variable so tests can shorten it.
var cmdTimeout = viewer.TakeWait

var errBusy = errors.New("the window did not take the call within 2 s (is it frozen or minimised?)")

// States of a call posted to the game loop. The loop runs a call only after
// moving it from pending to taken; a caller that stops waiting moves it from
// pending to abandoned, so a call reported as failed is never applied later.
const (
	callPending int32 = iota
	callTaken
	callAbandoned
)

// Run is the viewer subcommand: it opens the window and serves the socket
// until the window closes. Exit codes: 0 closed (or another window is
// already open), 1 failure, 3 no display.
func Run(ctx context.Context, svc *service.Service, version string) int {
	guardStderr()
	defer flushStderr()
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
	if err := openSocket(srv); err != nil {
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
	// Serve releases the lock when its context ends; the stop signal must
	// not do that, as the window lives on while it closes and saves. The
	// lock is held until Run returns (srv.Close) or the process exits, so
	// close_viewer sees the window end and no second window starts beside
	// it.
	go func() {
		if err := srv.Serve(context.WithoutCancel(ctx)); err != nil {
			log.Printf("control socket: %v", err)
		}
	}()
	// A stop signal (close_viewer stopping a window that does not answer)
	// closes the window through its loop. A loop that is stuck never does,
	// so the process ends on its own after viewer.StopGrace; a loop that
	// closed leaves Run its input-set writes (viewer.SaveWait) and 2 s more.
	loopDone := make(chan struct{})
	go func() {
		<-ctx.Done()
		select {
		case <-loopDone:
			time.Sleep(viewer.SaveWait + 2*time.Second)
			log.Printf("the window closed but did not end within %s; exiting", viewer.SaveWait+2*time.Second)
		case <-time.After(viewer.StopGrace):
			log.Printf("the window did not close within %s of the stop signal; exiting", viewer.StopGrace)
		}
		flushStderr()
		os.Exit(1)
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
	close(loopDone)
	a.rend.stop()
	a.saveCameras()
	if !a.saver.flush(viewer.SaveWait) {
		log.Print("an input set was still being saved when the window closed")
	}
	if err != nil && !errors.Is(err, ebiten.Termination) {
		log.Printf("window: %v", err)
		return 1
	}
	log.Print("closed")
	return 0
}

// openSocket opens the control socket. A process that only checks whether
// a window runs takes the lock for a moment; a window starting just then
// tries again for 300 ms before it concludes that another one is open.
func openSocket(srv *socket.Server) error {
	deadline := time.Now().Add(300 * time.Millisecond)
	for {
		err := srv.Open()
		if err == nil || !strings.Contains(err.Error(), "already running") || time.Now().After(deadline) {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
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

// do runs fn on the game loop and returns what it returns. When the loop
// has not taken the call within cmdTimeout (or ctx ends first), the call is
// abandoned and never runs; once taken, its answer is awaited, as fn is
// already running on the loop.
func (a *App) do(ctx context.Context, fn func() any) (any, error) {
	var state atomic.Int32
	done := make(chan any, 1)
	run := func() {
		if state.CompareAndSwap(callPending, callTaken) {
			done <- fn()
		}
	}
	timer := time.NewTimer(cmdTimeout)
	defer timer.Stop()
	select {
	case a.cmds <- run:
	case <-timer.C:
		return nil, errBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case v := <-done:
		return v, nil
	case <-timer.C:
	case <-ctx.Done():
	}
	if state.CompareAndSwap(callPending, callAbandoned) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, errBusy
	}
	return <-done, nil
}

// saveWait bounds how long a show or control call waits for the input set
// it changed to be stored. A variable so tests can shorten it.
var saveWait = viewer.SaveWait

// doSaving runs fn on the game loop like do, then waits for the input set
// write fn queued, if any, so that generate_model reads the set the reply
// describes.
func (a *App) doSaving(ctx context.Context, fn func() viewer.StateReply) (any, error) {
	var saved <-chan error
	v, err := a.do(ctx, func() any {
		a.pendingSave = nil
		rep := fn()
		saved, a.pendingSave = a.pendingSave, nil
		return rep
	})
	if err != nil || saved == nil {
		return v, err
	}
	rep := v.(viewer.StateReply)
	select {
	case werr := <-saved:
		if werr != nil {
			e := errorOf(werr)
			e.Message = "The window shows the change, but the input set was not saved: " + e.Message
			rep.Error = e
		}
	case <-time.After(saveWait):
		rep.Error = &viewer.Error{Code: render.CodeUnavailable, Message: "The window shows the change, but the input set is still being saved (the database is busy).",
			Hint: "Call get_viewer_state in a moment; generate_model reads the set as stored."}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return rep, nil
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
		return a.doSaving(ctx, func() viewer.StateReply { return a.applyShow(prep, p) })
	})
	srv.Handle(viewer.MethodControl, func(ctx context.Context, params json.RawMessage) (any, error) {
		var c viewer.Control
		if err := decode(params, &c); err != nil {
			return nil, err
		}
		return a.doSaving(ctx, func() viewer.StateReply { return a.applyControl(c) })
	})
	srv.Handle(viewer.MethodState, func(ctx context.Context, params json.RawMessage) (any, error) {
		var p viewer.StateParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		return a.do(ctx, func() any {
			rep := viewer.StateReply{State: a.snapshot(), PID: os.Getpid()}
			rep.Actions, rep.ActionsTruncated = a.actionsSince(p.SinceRevision)
			return rep
		})
	})
	srv.Handle(viewer.MethodCapture, func(ctx context.Context, params json.RawMessage) (any, error) {
		var p viewer.CaptureParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		wait := time.Duration(max(p.WaitMS, 0)) * time.Millisecond
		req := &captureReq{start: time.Now(), deadline: time.Now().Add(wait), since: p.SinceRevision, done: make(chan viewer.CaptureReply, 1)}
		if _, err := a.do(ctx, func() any { a.captures = append(a.captures, req); a.dirty(); return nil }); err != nil {
			return nil, err
		}
		select {
		case r, ok := <-req.done:
			if !ok {
				// The window closed before the screenshot was taken.
				return viewer.CaptureReply{PID: os.Getpid(), Error: &viewer.Error{Code: render.CodeUnavailable,
					Message: "The viewer window closed before the screenshot was taken.", Hint: "Call open_viewer when the user wants to see something again."}}, nil
			}
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
