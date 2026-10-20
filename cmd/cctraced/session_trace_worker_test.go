package main

import (
	"context"
	"testing"
	"time"
)

func TestTraceWorkerIdleWakeAndCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wake := make(chan struct{}, 1)
	calls := make(chan int, 4)
	idle := make(chan struct{}, 1)
	done := make(chan struct{})
	// A metadata recovery pass is allowed at boot; after it the worker must wait
	// on demand, with no periodic connection or source-query churn.
	go func() {
		defer close(done)
		n := 0
		runTraceWorker(ctx, func(context.Context) (bool, error) { n++; calls <- n; return false, nil }, wake, func(time.Duration) <-chan time.Time { idle <- struct{}{}; return make(chan time.Time) }, func(string, ...any) {})
	}()
	select {
	case n := <-calls:
		if n != 1 {
			t.Fatal(n)
		}
	case <-time.After(time.Second):
		t.Fatal("startup recovery blocked")
	}
	select {
	case <-idle:
		t.Fatal("idle worker scheduled periodic polling")
	case n := <-calls:
		t.Fatalf("idle second run=%d", n)
	default:
	}
	wake <- struct{}{}
	select {
	case n := <-calls:
		if n != 2 {
			t.Fatal(n)
		}
	case <-time.After(time.Second):
		t.Fatal("demand wake ignored")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel did not release worker")
	}
}
