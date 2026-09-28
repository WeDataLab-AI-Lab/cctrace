package main

import (
	"context"
	"time"
)

// usageRollupRebuildPoll is how soon a queued usage rebuild starts. The exclusion
// change that queued it has already answered; this is how long the charts wait
// before catching up, and the check is one primary-key read of a one-row table.
const usageRollupRebuildPoll = 5 * time.Second

// usageRollupRebuildMaxWait caps the backoff after failures: a database that is
// back is noticed within five minutes.
const usageRollupRebuildMaxWait = 5 * time.Minute

// usageRollupRebuildWait is the wait after `failures` consecutive failed runs:
// the poll interval, doubled per failure up to the cap. Retrying a rebuild that
// fails every five seconds only repeats the failure and floods the log.
func usageRollupRebuildWait(failures int) time.Duration {
	wait := usageRollupRebuildPoll
	for i := 0; i < failures && wait < usageRollupRebuildMaxWait; i++ {
		wait *= 2
	}
	return min(wait, usageRollupRebuildMaxWait)
}

// runUsageRollupRebuildWorker runs the usage rebuild exclusion changes queue
// (store.RunPendingUsageRollupRebuild) once at boot, so a request a restart
// interrupted resumes, and then after each wait. It runs under ctx, the server's
// lifetime: under the request's, a client that disconnected cancelled a rebuild
// whose exclusion had already committed. after is the test seam for the clock.
//
// Failures are logged at the first and then at every power of two in a row, and
// a recovery once, so a rebuild that fails for an hour leaves a handful of
// lines rather than one per attempt.
func runUsageRollupRebuildWorker(
	ctx context.Context,
	after func(time.Duration) <-chan time.Time,
	run func(context.Context) (bool, error),
	logf func(format string, args ...any),
) {
	failures := 0
	for {
		ran, err := run(ctx)
		switch {
		case err != nil:
			failures++
			if failures&(failures-1) == 0 {
				logf("[usage-rollup] queued rebuild failed (%d in a row, next try in %s): %v",
					failures, usageRollupRebuildWait(failures), err)
			}
		case failures > 0:
			logf("[usage-rollup] queued rebuild recovered after %d failures", failures)
			failures = 0
		case ran:
			logf("[usage-rollup] queued rebuild done")
		}
		select {
		case <-ctx.Done():
			return
		case <-after(usageRollupRebuildWait(failures)):
		}
	}
}
