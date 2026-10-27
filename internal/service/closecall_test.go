package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sairaph/rodin3d-mcp/internal/viewer"
)

// L7 (N19): the close call answers through, is abandoned at its bound
// even when it ignores its context (a blocking connect), and a caller's
// cancel ends the wait at once; the call's context ends with the wait.
func TestBoundedCall(t *testing.T) {
	const bound = 200 * time.Millisecond

	t.Run("answer", func(t *testing.T) {
		rep, err := boundedCall(context.Background(), bound, func(_ context.Context, r *viewer.StateReply) error {
			r.State.Revision = 7
			return nil
		})
		if err != nil || rep.State.Revision != 7 {
			t.Fatalf("rep %+v err %v", rep.State, err)
		}
	})
	t.Run("call error", func(t *testing.T) {
		want := errors.New("refused")
		if _, err := boundedCall(context.Background(), bound, func(context.Context, *viewer.StateReply) error { return want }); !errors.Is(err, want) {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("abandoned at the bound", func(t *testing.T) {
		release := make(chan struct{})
		defer close(release)
		callCtx := make(chan context.Context, 1)
		start := time.Now()
		_, err := boundedCall(context.Background(), bound, func(ctx context.Context, _ *viewer.StateReply) error {
			callCtx <- ctx
			<-release // ignores ctx, like a Dial that takes none
			return nil
		})
		took := time.Since(start)
		if !errors.Is(err, context.DeadlineExceeded) || took < bound || took > bound+500*time.Millisecond {
			t.Fatalf("err %v after %v", err, took)
		}
		if ctx := <-callCtx; ctx.Err() == nil {
			t.Fatal("the abandoned call's context is still live")
		}
	})
	t.Run("caller cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		release := make(chan struct{})
		defer close(release)
		go func() { time.Sleep(50 * time.Millisecond); cancel() }()
		start := time.Now()
		_, err := boundedCall(ctx, 5*time.Second, func(context.Context, *viewer.StateReply) error { <-release; return nil })
		if !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
			t.Fatalf("err %v after %v", err, time.Since(start))
		}
	})
}
