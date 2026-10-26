package app

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sairaph/rodin3d-mcp/internal/viewer"
)

// loopOnce runs every call queued on a so far, as one Update would.
func loopOnce(a *App) {
	for {
		select {
		case fn := <-a.cmds:
			fn()
		default:
			return
		}
	}
}

func withTimeout(t *testing.T, d time.Duration) {
	old := cmdTimeout
	cmdTimeout = d
	t.Cleanup(func() { cmdTimeout = old })
}

// TestDoStates covers every way a posted call ends: answered, never taken
// (timeout, cancelled context), taken but slow, and a full queue.
func TestDoStates(t *testing.T) {
	withTimeout(t, 50*time.Millisecond)

	t.Run("answered", func(t *testing.T) {
		a := &App{cmds: make(chan func(), 16)}
		go func() {
			time.Sleep(5 * time.Millisecond)
			loopOnce(a)
		}()
		v, err := a.do(context.Background(), func() any { return 7 })
		if err != nil || v != 7 {
			t.Fatalf("got %v, %v; want 7, nil", v, err)
		}
	})

	t.Run("not taken in time is never applied", func(t *testing.T) {
		a := &App{cmds: make(chan func(), 16)}
		var ran atomic.Bool
		_, err := a.do(context.Background(), func() any { ran.Store(true); return nil })
		if !errors.Is(err, errBusy) {
			t.Fatalf("err = %v, want errBusy", err)
		}
		loopOnce(a) // the loop wakes up later
		if ran.Load() {
			t.Fatal("a call reported as failed ran afterwards")
		}
	})

	t.Run("cancelled before taken is never applied", func(t *testing.T) {
		a := &App{cmds: make(chan func(), 16)}
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(5 * time.Millisecond)
			cancel()
		}()
		var ran atomic.Bool
		_, err := a.do(ctx, func() any { ran.Store(true); return nil })
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		loopOnce(a)
		if ran.Load() {
			t.Fatal("a cancelled call ran afterwards")
		}
	})

	t.Run("taken but slow returns its answer", func(t *testing.T) {
		a := &App{cmds: make(chan func(), 16)}
		go func() {
			time.Sleep(5 * time.Millisecond)
			loopOnce(a)
		}()
		v, err := a.do(context.Background(), func() any {
			time.Sleep(4 * cmdTimeout)
			return "late"
		})
		if err != nil || v != "late" {
			t.Fatalf("got %v, %v; want late, nil", v, err)
		}
	})

	t.Run("full queue", func(t *testing.T) {
		a := &App{cmds: make(chan func())} // no room, no loop
		_, err := a.do(context.Background(), func() any { return nil })
		if !errors.Is(err, errBusy) {
			t.Fatalf("err = %v, want errBusy", err)
		}
	})
}

// TestDoSaving covers every way a call that changes an input set ends: no
// write queued, the write stored, the write failed, the write still
// running after saveWait, the caller gone while it waits, and the call
// itself not taken (then there is nothing to wait for).
func TestDoSaving(t *testing.T) {
	withTimeout(t, 50*time.Millisecond)
	old := saveWait
	saveWait = 100 * time.Millisecond
	t.Cleanup(func() { saveWait = old })

	// call runs a doSaving call whose fn queues write (nil: none) and
	// returns state revision 3; the loop takes it unless notTaken.
	call := func(ctx context.Context, write <-chan error, notTaken bool) (any, error) {
		a := &App{cmds: make(chan func(), 16)}
		if !notTaken {
			go func() {
				time.Sleep(5 * time.Millisecond)
				loopOnce(a)
			}()
		}
		return a.doSaving(ctx, func() viewer.StateReply {
			a.pendingSave = write
			return viewer.StateReply{State: viewer.State{Revision: 3}}
		})
	}
	result := func(err error) <-chan error {
		ch := make(chan error, 1)
		ch <- err
		return ch
	}

	t.Run("no write", func(t *testing.T) {
		v, err := call(context.Background(), nil, false)
		if rep, ok := v.(viewer.StateReply); err != nil || !ok || rep.Error != nil || rep.State.Revision != 3 {
			t.Fatalf("got %+v, %v", v, err)
		}
	})
	t.Run("write stored", func(t *testing.T) {
		v, err := call(context.Background(), result(nil), false)
		if rep, ok := v.(viewer.StateReply); err != nil || !ok || rep.Error != nil || rep.State.Revision != 3 {
			t.Fatalf("got %+v, %v", v, err)
		}
	})
	t.Run("write failed", func(t *testing.T) {
		v, err := call(context.Background(), result(errors.New("disk full")), false)
		rep, ok := v.(viewer.StateReply)
		if err != nil || !ok || rep.Error == nil || !strings.HasPrefix(rep.Error.Message, "The window shows the change, but the input set was not saved: ") || rep.State.Revision != 3 {
			t.Fatalf("got %+v, %v", v, err)
		}
	})
	t.Run("write still running", func(t *testing.T) {
		start := time.Now()
		v, err := call(context.Background(), make(chan error), false)
		rep, ok := v.(viewer.StateReply)
		if err != nil || !ok || rep.Error == nil || rep.Error.Code != "unavailable" || rep.State.Revision != 3 {
			t.Fatalf("got %+v, %v", v, err)
		}
		if d := time.Since(start); d < saveWait {
			t.Fatalf("answered after %v, before saveWait", d)
		}
	})
	t.Run("caller gone while waiting", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(30*time.Millisecond, cancel)
		if _, err := call(ctx, make(chan error), false); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})
	t.Run("not taken", func(t *testing.T) {
		if _, err := call(context.Background(), result(nil), true); !errors.Is(err, errBusy) {
			t.Fatalf("err = %v, want errBusy", err)
		}
	})
}
