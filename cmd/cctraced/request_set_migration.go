package main

import (
	"context"
	"time"

	"cctrace/internal/store"
)

// requestSetRetryFirst is the wait after a step's first failure in a row; each
// further failure doubles it, up to requestSetRetryMax. The shape of
// usageRollupRebuildWait, counted from the first failure: a database that is
// back is noticed within five minutes, and a step that keeps failing does not
// flood the log.
const (
	requestSetRetryFirst = 5 * time.Second
	requestSetRetryMax   = 5 * time.Minute
)

// requestSetRetryWait is the wait after `failures` failures of one step in a row.
func requestSetRetryWait(failures int) time.Duration {
	wait := requestSetRetryFirst
	for i := 1; i < failures && wait < requestSetRetryMax; i++ {
		wait *= 2
	}
	return min(wait, requestSetRetryMax)
}

// runRequestSetMigration runs the P1t transition (p1t-design §4): the steps in
// order, a step whose marker exists returning at once. A step that fails is
// retried alone after a wait, and no later step runs before it succeeds.
// Restarting is free: the markers and the batch steps' cursors are in the
// database, so a new process resumes at the first step without its marker, and
// a batch step at its cursor.
//
// A step that committed part of its work before it failed starts its failure
// count over. The message_id walk commits over a thousand batches (eight hundred
// windows, a thousand rows at most to a batch), and backs off with 55P03
// whenever a user-facing writer holds one of its rows; counted as one streak,
// those scattered collisions would climb to the five-minute wait.
//
// A failure is logged when its message differs from the previous one, and a
// recovery once, as the codex-imputed incremental loop does. A step that did its
// work here gets one line when it completes, with the time since this process
// first ran it -- the step timings P1t-8 reads (p1t-design §4.2); a restart
// starts that clock again. A step that only found its marker gets none, so a
// boot after the transition logs nothing.
//
// It returns as soon as ctx is cancelled, mid-wait too: shutdown joins the
// managed repairs. now and after are the test seams for the clock.
func runRequestSetMigration(
	ctx context.Context,
	steps []store.RequestSetMigrationStep,
	now func() time.Time,
	after func(time.Duration) <-chan time.Time,
	logf func(format string, args ...any),
) {
	for _, step := range steps {
		started := now()
		failures, lastErr := 0, ""
		for {
			progressed, err := step.Run(ctx)
			if ctx.Err() != nil {
				return
			}
			if err == nil {
				if lastErr != "" {
					logf("[request-set] %s recovered", step.Marker)
				}
				if progressed {
					logf("[request-set] %s done in %s since this process first ran it",
						step.Marker, now().Sub(started).Round(time.Millisecond))
				}
				break
			}
			if progressed {
				failures = 0
			}
			failures++
			wait := requestSetRetryWait(failures)
			if msg := err.Error(); msg != lastErr {
				logf("[request-set] %s failed, next try in %s: %v (repeats are suppressed until this changes)",
					step.Marker, wait, err)
				lastErr = msg
			}
			select {
			case <-ctx.Done():
				return
			case <-after(wait):
			}
		}
	}
}
