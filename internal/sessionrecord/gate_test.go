package sessionrecord

import (
	"context"
	"sync/atomic"
	"testing"
)

func TestTransmissionGateMutationWaitsForBodyThenCancelsResponse(t *testing.T) {
	var gate TransmissionGate
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var dbReleased atomic.Bool
	lease, err := gate.Acquire(ctx, cancel, func(context.Context) (func(), error) {
		return func() { dbReleased.Store(true) }, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	mutated := false
	done := make(chan error, 1)
	go func() {
		done <- gate.Mutate(func() error {
			mutated = true
			return nil
		})
	}()
	select {
	case err := <-done:
		t.Fatalf("mutation crossed a held request-body gate early: %v", err)
	default:
	}
	lease.ReleaseBody()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !mutated || !dbReleased.Load() {
		t.Fatalf("mutated=%v dbReleased=%v", mutated, dbReleased.Load())
	}
	if ctx.Err() == nil {
		t.Fatal("policy mutation did not cancel the in-flight response")
	}
	lease.Finish()
}
