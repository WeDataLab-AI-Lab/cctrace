package main

import "context"

// claudeImputedRefresher wraps the Claude imputed-cost refresh for its two call
// sites, the boot refresh and the 30-minute one, which share one goroutine.
//
// It skips the refresh until step 3 of the P1t transition has rewritten
// claude_imputed_cost one row per request (ready: the
// claude_imputed_requests_v1 marker, p1t-design §4.1). Run before the message_id
// backfill, the refresh would read every old row's request key out of raw; and
// the imputed cost is to change once, at step 3. New offline sessions show no
// cost until then.
//
// A marker it cannot read skips the refresh too -- the refresh would read the
// same database -- and is logged each time. Skipping for a missing marker is
// logged once when it starts, not every 30 minutes.
func claudeImputedRefresher(
	ready func(context.Context) (bool, error),
	refresh func(context.Context) error,
	logf func(format string, args ...any),
) func(context.Context) {
	skipping := false
	return func(ctx context.Context) {
		ok, err := ready(ctx)
		switch {
		case err != nil:
			logf("[imputed-cost] refresh skipped: %v", err)
			return
		case !ok:
			if !skipping {
				logf("[imputed-cost] refresh skipped until the P1t transition rewrites claude_imputed_cost (step 3)")
				skipping = true
			}
			return
		}
		skipping = false
		if err := refresh(ctx); err != nil {
			logf("[imputed-cost] refresh: %v", err)
		}
	}
}
