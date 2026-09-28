package main

import (
	"testing"
	"time"
)

// The server's Retry-After overrides the backoff only when it asks for longer.
// A shorter one must not pull an already-scheduled retry closer.
func TestHonourRetryAfter(t *testing.T) {
	var bo backoffState
	bo.recordFailure() // ~5s
	bo.honourRetryAfter(10 * time.Minute)
	if got := time.Until(bo.nextRetry); got < 9*time.Minute {
		t.Errorf("next retry in %s, want about 10m", got)
	}

	bo.honourRetryAfter(time.Second)
	if got := time.Until(bo.nextRetry); got < 9*time.Minute {
		t.Errorf("a shorter Retry-After pulled the retry in to %s", got)
	}

	var idle backoffState
	idle.recordFailure()
	idle.honourRetryAfter(0)
	if got := time.Until(idle.nextRetry); got > 10*time.Second {
		t.Errorf("no Retry-After changed the schedule to %s", got)
	}
}
