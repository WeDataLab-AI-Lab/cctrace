package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// A rebuild queued before a restart has to run without waiting for the next
// exclusion change, under the worker's own context, and again after each wait.
func TestUsageRollupRebuildWorkerRunsOnBootAndAfterEachWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := make(chan context.Context, 2)
	wake := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runUsageRollupRebuildWorker(ctx, func(time.Duration) <-chan time.Time { return wake },
			func(runCtx context.Context) (bool, error) {
				calls <- runCtx
				return true, nil
			}, func(string, ...any) {})
	}()

	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()
	select {
	case runCtx := <-calls:
		if runCtx != ctx {
			t.Error("the rebuild did not run under the worker's own context")
		}
	case <-timeout.C:
		t.Fatal("pending rebuild did not run on boot")
	}
	wake <- time.Time{}
	select {
	case <-calls:
	case <-timeout.C:
		t.Fatal("pending rebuild did not run after the wait")
	}
	cancel()
	<-done
}

// A rebuild that keeps failing -- the database is down, or the rebuild itself
// errors -- must not be retried and logged every five seconds for as long as it
// fails. The wait doubles with each consecutive failure up to a cap, is back to
// the poll interval after a success, and only some failures are logged.
func TestUsageRollupRebuildWorkerBacksOffOnFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	failures := 12
	results := make([]error, 0, failures+1)
	for i := 0; i < failures; i++ {
		results = append(results, errors.New("rebuild failed"))
	}
	results = append(results, nil)

	var waits []time.Duration
	var logs []string
	calls := 0
	runUsageRollupRebuildWorker(ctx,
		func(d time.Duration) <-chan time.Time {
			waits = append(waits, d)
			if len(waits) == len(results) {
				cancel()
			}
			ch := make(chan time.Time, 1)
			ch <- time.Time{}
			return ch
		},
		func(context.Context) (bool, error) {
			err := results[calls%len(results)]
			calls++
			return err == nil, err
		},
		func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) })

	want := []time.Duration{
		10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second, 160 * time.Second,
		5 * time.Minute, 5 * time.Minute, 5 * time.Minute, 5 * time.Minute, 5 * time.Minute, 5 * time.Minute, 5 * time.Minute,
		5 * time.Second,
	}
	if len(waits) < len(want) {
		t.Fatalf("waits = %v, want at least %d", waits, len(want))
	}
	for i, w := range want {
		if waits[i] != w {
			t.Errorf("wait after run %d = %s, want %s (all: %v)", i+1, waits[i], w, waits[:len(want)])
		}
	}

	var failed, recovered int
	for _, l := range logs {
		switch {
		case strings.Contains(l, "failed"):
			failed++
		case strings.Contains(l, "recovered"):
			recovered++
		}
	}
	if failed == 0 || failed >= failures {
		t.Errorf("logged %d of %d consecutive failures, want some but not every one: %q", failed, failures, logs)
	}
	if recovered != 1 {
		t.Errorf("logged recovery %d times, want once: %q", recovered, logs)
	}
}
