package main

import (
	"testing"
	"time"
)

func TestBackoff_RecordFailure(t *testing.T) {
	var bo backoffState

	bo.recordFailure()
	if bo.consecutive != 1 {
		t.Fatalf("expected consecutive=1, got %d", bo.consecutive)
	}
	if bo.nextRetry.IsZero() {
		t.Fatal("expected nextRetry to be set")
	}
	// First failure: base = 1*1*5s = 5s, with ±20% jitter → 4s..6s
	delay := time.Until(bo.nextRetry)
	if delay < 3*time.Second || delay > 7*time.Second {
		t.Fatalf("expected delay ~5s (±jitter), got %v", delay)
	}
}

func TestBackoff_Capped(t *testing.T) {
	var bo backoffState
	// Simulate many failures to hit cap
	for i := 0; i < 20; i++ {
		bo.recordFailure()
	}
	delay := time.Until(bo.nextRetry)
	// Cap is 5min with ±20% jitter → 4min..6min
	if delay > 6*time.Minute+10*time.Second {
		t.Fatalf("expected delay capped near 5min, got %v", delay)
	}
}

func TestBackoff_RecordSuccess(t *testing.T) {
	var bo backoffState
	bo.recordFailure()
	bo.recordFailure()
	bo.recordSuccess()
	if bo.consecutive != 0 {
		t.Fatalf("expected consecutive=0 after success, got %d", bo.consecutive)
	}
	if !bo.nextRetry.IsZero() {
		t.Fatal("expected nextRetry to be zero after success")
	}
}

func TestBackoff_ShouldSkip(t *testing.T) {
	var bo backoffState
	if bo.shouldSkip() {
		t.Fatal("should not skip with zero state")
	}
	bo.recordFailure()
	if !bo.shouldSkip() {
		t.Fatal("should skip right after failure")
	}
	bo.recordSuccess()
	if bo.shouldSkip() {
		t.Fatal("should not skip after success")
	}
}
