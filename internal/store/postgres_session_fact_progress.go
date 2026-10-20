package store

import (
	"context"
	"sync"
	"time"
)

// sessionFactProgress permits arbitrarily long computations that keep making
// progress, while cancellation still bounds a stuck read or compute phase.
func sessionFactProgress(parent context.Context, idle time.Duration) (context.Context, func(), func()) {
	ctx, cancel := context.WithCancelCause(parent)
	var mu sync.Mutex
	last := time.Now()
	stopped := false
	var timer *time.Timer
	mu.Lock()
	timer = time.AfterFunc(idle, func() {
		mu.Lock()
		defer mu.Unlock()
		if stopped {
			return
		}
		if remaining := idle - time.Since(last); remaining > 0 {
			timer.Reset(remaining)
			return
		}
		cancel(context.DeadlineExceeded)
	})
	mu.Unlock()
	progress := func() {
		mu.Lock()
		defer mu.Unlock()
		if !stopped {
			last = time.Now()
			timer.Reset(idle)
		}
	}
	stop := func() {
		mu.Lock()
		stopped = true
		timer.Stop()
		mu.Unlock()
		cancel(context.Canceled)
	}
	return ctx, progress, stop
}
