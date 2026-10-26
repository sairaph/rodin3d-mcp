package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
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
