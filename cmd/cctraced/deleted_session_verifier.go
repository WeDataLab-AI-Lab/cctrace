package main

import (
	"context"
	"time"
)

// runDeletedSessionVerifier performs the boot verification before waiting for
// daily ticks. Accepting the tick channel and callback is the deterministic boot
// seam: tests can prove the first call without clocks or sleeps.
func runDeletedSessionVerifier(
	ctx context.Context,
	ticks <-chan time.Time,
	verify func(context.Context) (int64, error),
	report func(int64, error),
) {
	run := func() {
		n, err := verify(ctx)
		report(n, err)
	}
	run()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticks:
			if !ok {
				return
			}
			run()
		}
	}
}
