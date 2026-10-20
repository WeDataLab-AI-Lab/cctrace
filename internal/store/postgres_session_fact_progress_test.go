package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSessionFactProgressCancelsIdleWork(t *testing.T) {
	t.Parallel()
	ctx, _, stop := sessionFactProgress(context.Background(), 0)
	defer stop()
	select {
	case <-ctx.Done():
		if !errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
			t.Fatalf("idle cause=%v", context.Cause(ctx))
		}
	case <-time.After(time.Second):
		t.Fatal("idle computation did not stop")
	}
}

func TestSessionFactProgressReleasesParentAndCompletedWork(t *testing.T) {
	t.Parallel()
	for _, completed := range []bool{false, true} {
		parent, cancel := context.WithCancel(context.Background())
		ctx, progress, stop := sessionFactProgress(parent, time.Hour)
		progress()
		if completed {
			stop()
		} else {
			cancel()
		}
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Errorf("completed=%v context=%v", completed, ctx.Err())
		}
		stop()
		cancel()
	}
}
