package main

import (
	"math/rand/v2"
	"time"
)

// backoffBaseUnit is the first retry delay; attempt n waits n^2 of it. It is a
// var only so tests that have to watch a real backoff elapse can shorten it --
// TestWatchReleasesTheLockAfterASustainedStall needs a second failing pass, and
// at 5s that was one of the two slowest tests in cmd/cctrace. Production never
// assigns to it.
var backoffBaseUnit = 5 * time.Second

type backoffState struct {
	consecutive int
	nextRetry   time.Time
}

func (b *backoffState) recordFailure() {
	b.consecutive++
	base := time.Duration(b.consecutive*b.consecutive) * backoffBaseUnit
	if base > 5*time.Minute {
		base = 5 * time.Minute
	}
	// ±20% jitter
	jitter := time.Duration(float64(base) * (0.8 + 0.4*rand.Float64()))
	b.nextRetry = time.Now().Add(jitter)
}

func (b *backoffState) recordSuccess() {
	b.consecutive = 0
	b.nextRetry = time.Time{}
}

func (b *backoffState) shouldSkip() bool {
	return !b.nextRetry.IsZero() && time.Now().Before(b.nextRetry)
}

// honourRetryAfter pushes the next attempt out to what the server asked for,
// when that is later than the backoff's own schedule. A shorter request never
// pulls a scheduled retry closer.
func (b *backoffState) honourRetryAfter(d time.Duration) {
	if d > time.Until(b.nextRetry) {
		b.nextRetry = time.Now().Add(d)
	}
}
