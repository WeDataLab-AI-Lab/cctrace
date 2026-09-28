package main

import (
	"context"
	"testing"
	"time"
)

func TestRunDeletedSessionVerifierInvokesOnBootAndEachTick(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time)
	calls := make(chan struct{}, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runDeletedSessionVerifier(ctx, ticks, func(context.Context) (int64, error) {
			calls <- struct{}{}
			return 0, nil
		}, func(int64, error) {})
	}()

	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()
	select {
	case <-calls: // boot invocation, before any tick
	case <-timeout.C:
		t.Fatal("verifier did not run on boot")
	}
	ticks <- time.Time{}
	select {
	case <-calls:
	case <-timeout.C:
		t.Fatal("verifier did not run on tick")
	}
	cancel()
	<-done
}
