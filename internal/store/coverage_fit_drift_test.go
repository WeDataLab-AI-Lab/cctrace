package store

import (
	"context"
	"math"
	"os"
	"sort"
	"testing"
	"time"
)

// TestCoverageFitDriftReplay is the experiment that decides whether the fitted
// constant may be cached at all.
//
// The question is not "is a cache faster" -- it obviously is -- but "does k move
// enough within a TTL to change what the reader is told". The file comment on
// computeCoverageGap exists because a FIXED factor once drifted by -2.5% to -20%
// within half a day, and a cache is a fixed factor with a timer on it. So the
// number that decides this is drift, not milliseconds.
//
// Method: walk the last 14 days hour by hour. At each hour t, derive k as of t
// and as of t-TTL from the same loaded readings, apply both to the same reported
// range, and record the difference in coverage_ratio. The frontend rounds to
// whole percent, so the criterion is stated in percentage points.
//
// Ship criteria, both required:
//   - p99 of |delta coverage_ratio| <= 1 percentage point
//   - no account flips fitted <-> unfitted across the TTL, ever. That changes the
//     response's SHAPE, not just a number, and one occurrence is disqualifying.
//
// Gated on CCTRACE_BENCH_DSN because it needs real readings; a seeded container
// would only replay the seed's own smoothness back at us.
func TestCoverageFitDriftReplay(t *testing.T) {
	dsn := os.Getenv("CCTRACE_BENCH_DSN")
	if dsn == "" {
		t.Skip("set CCTRACE_BENCH_DSN to a copy of prod to run the fit-drift replay")
	}
	ttl := time.Hour
	if v := os.Getenv("CCTRACE_FIT_TTL"); v != "" {
		parsed, err := time.ParseDuration(v)
		if err != nil {
			t.Fatalf("CCTRACE_FIT_TTL: %v", err)
		}
		ttl = parsed
	}

	ctx := context.Background()
	s, err := NewPgStore(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer s.Close()

	end := time.Now().UTC().Truncate(time.Hour)
	start := end.Add(-14 * 24 * time.Hour)
	// One load covering every hour the replay visits, plus the fit window behind
	// the earliest one. Reloading per hour would make the replay cost 336 times
	// what it needs to.
	samples, measured, err := s.loadCoverageInputs(ctx, "", start.Add(-coverageFitWindow).Add(-ttl), end, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(samples) == 0 {
		t.Skip("no quota readings in the replay window")
	}

	byAccount := map[string][]coverageSample{}
	for _, c := range samples {
		byAccount[c.LoginEmail] = append(byAccount[c.LoginEmail], c)
	}

	var deltas []float64
	flips := 0
	hours := 0
	for at := start; !at.After(end); at = at.Add(time.Hour) {
		reportSince := at.Add(-24 * time.Hour)
		for email, accountSamples := range byAccount {
			minutes := append([]coverageMinute(nil), measured[email]...)
			fresh := fitAccountK(accountSamples, minutes, at)
			stale := fitAccountK(accountSamples, append([]coverageMinute(nil), measured[email]...), at.Add(-ttl))

			if (fresh.Unfitted == "") != (stale.Unfitted == "") {
				flips++
				t.Errorf("%s at %s flips fitted<->unfitted across the TTL (fresh %q, stale %q)",
					email, at.Format(time.RFC3339), fresh.Unfitted, stale.Unfitted)
				continue
			}
			if fresh.Unfitted != "" {
				continue
			}
			freshAcct, _ := applyFit(fresh, accountSamples, minutes, reportSince, at, nil)
			staleAcct, _ := applyFit(stale, accountSamples, minutes, reportSince, at, nil)
			if freshAcct.ImpliedUSD <= 0 || staleAcct.ImpliedUSD <= 0 {
				continue
			}
			deltas = append(deltas, math.Abs(
				freshAcct.MeasuredUSD/freshAcct.ImpliedUSD-
					staleAcct.MeasuredUSD/staleAcct.ImpliedUSD))
		}
		hours++
	}

	if len(deltas) == 0 {
		t.Skip("no fitted account-hours in the replay window")
	}
	sort.Float64s(deltas)
	p50 := deltas[len(deltas)*50/100]
	p99 := deltas[min(len(deltas)*99/100, len(deltas)-1)]
	worst := deltas[len(deltas)-1]
	t.Logf("fit drift over %d hours, %d account-hours, TTL %v: p50 %.3f%%p, p99 %.3f%%p, max %.3f%%p, flips %d",
		hours, len(deltas), ttl, p50*100, p99*100, worst*100, flips)

	if p99*100 > 1 {
		t.Errorf("p99 drift %.3f%%p exceeds 1%%p: the cached constant would change the reported percentage", p99*100)
	}
}
