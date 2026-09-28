package store

import (
	"context"
	"os"
	"testing"
	"time"
)

// BenchmarkCoverageGapStats measures the loader against a real database.
//
// Gated on CCTRACE_BENCH_DSN and skipped without it, so CI is unaffected. The
// shared test container is deliberately NOT used: it holds a handful of seeded
// rows, and the thing worth measuring here is what 28 days of a real hypertable
// costs. Point this at a copy of prod (scripts/dev-local-test.sh, then the
// local-web-dev copy) and record the numbers before changing anything.
//
// Measured on prod by hand before this benchmark existed, as the baseline the
// sub-benchmarks should reproduce:
//
//	readings query (28d)      26 ms
//	measured query (28d)     966 ms
//	measured query (7d)      352 ms
//	measured query (1d)       61 ms
//	measured query (3h)     22.5 ms
//
// The 28-day window is unconditional today (postgres_stats_coverage.go), so the
// short-range cases only become reachable once the fit stops being recomputed
// per request.
func BenchmarkCoverageGapStats(b *testing.B) {
	dsn := os.Getenv("CCTRACE_BENCH_DSN")
	if dsn == "" {
		b.Skip("set CCTRACE_BENCH_DSN to a copy of prod to run this benchmark")
	}
	ctx := context.Background()
	s, err := NewPgStore(ctx, dsn)
	if err != nil {
		b.Fatalf("connect: %v", err)
	}
	defer s.Close()

	until := time.Now()
	cases := []struct {
		name        string
		since       time.Time
		granularity string
	}{
		// The headline on the default view: the chart range is under a day, so
		// coverage widens to the trailing week.
		{"headline-7d", until.Add(-7 * 24 * time.Hour), ""},
		{"headline-28d", until.Add(-28 * 24 * time.Hour), ""},
		// The Unknown overlay on the default view, and on a wide one.
		{"buckets-3h-minute", until.Add(-3 * time.Hour), "minute"},
		{"buckets-28d-day", until.Add(-28 * 24 * time.Hour), "day"},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			f := CoverageGapFilter{Since: c.since, Until: until, Granularity: c.granularity, Timezone: "UTC"}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := s.CoverageGapStats(ctx, f); err != nil {
					b.Fatalf("CoverageGapStats: %v", err)
				}
			}
		})
	}
}
