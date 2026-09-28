package main

import (
	"math/rand/v2"
	"time"
)

type backoffState struct {
	consecutive int
	nextRetry   time.Time
}

func (b *backoffState) recordFailure() {
	b.consecutive++
	base := time.Duration(b.consecutive*b.consecutive) * 5 * time.Second
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
