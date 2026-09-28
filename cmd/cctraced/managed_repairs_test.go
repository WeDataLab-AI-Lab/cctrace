package main

import (
	"context"
	"testing"
	"time"
)

func TestStartManagedRepairsReturnsWhileRepairIsBlockedAndJoinsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	repair := func(ctx context.Context) {
		close(started)
		<-ctx.Done()
	}

	done := startManagedRepairs(ctx, repair)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("managed repair did not start")
	}
	select {
	case <-done:
		t.Fatal("startup waited for the blocked managed repair")
	default:
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("managed repair did not join after cancellation")
	}
}
