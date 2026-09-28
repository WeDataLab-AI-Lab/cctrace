package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// seedCoverageAccount writes n five-minute quota readings for one Claude account,
// each burning 2%, and one measured api_request of `tokens` inside every
// interval, priced at one dollar per thousand input tokens.
//
// The price matters since #539: the factor and both sides of the ratio are
// cost_usd, so an event with tokens but no cost_usd leaves the account with
// nothing to fit on and it is reported as unfitted.
func seedCoverageAccount(t *testing.T, s *PgStore, email, account string, n int, tokens int, start time.Time) {
	t.Helper()
	ctx := context.Background()
	resets := start.Add(5 * time.Hour)
	var samples []*QuotaSample
	var events []*OtelEvent
	for i := 0; i <= n; i++ {
		at := start.Add(time.Duration(i) * 5 * time.Minute)
		q := sampleAt(0, account, "session")
		q.SampledAt, q.UsedPct, q.LoginEmail, q.ResetsAt = at, float64(i)*2, email, &resets
		samples = append(samples, q)
		if i > 0 {
			events = append(events, &OtelEvent{
				Ts: at.Add(-2 * time.Minute), EventName: "api_request", SessionID: account + "-s",
				UserID: "u-" + account, LoginEmail: email, Agent: "claude", BillingProvider: "anthropic",
				InputTokens: ptrInt(tokens), OutputTokens: ptrInt(0), CostUSD: ptrFloat(float64(tokens) / 1000),
			})
		}
	}
	if _, err := s.InsertQuotaSamples(ctx, samples); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
	if err := s.InsertEvents(ctx, events); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
}

// The loader defines the population both sides are computed on: Claude accounts
// with observed readings, minus every exclusion the quota chart applies. An
// excluded account must leave both sides -- keeping its measured usage while
// dropping its burn would push the ratio up and read as better coverage.
func TestCoverageGapStats_excludedAccountLeavesBothSides(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	start := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

	seedCoverageAccount(t, s, "team@x.test", "acct-team", 40, 1000, start)
	seedCoverageAccount(t, s, "personal@x.test", "acct-personal", 40, 1000, start)
	// A compatible-provider request burns no subscription and must not count as measured.
	if err := s.InsertEvents(ctx, []*OtelEvent{{
		Ts: start.Add(time.Minute), EventName: "api_request", SessionID: "proxy", UserID: "u-acct-team",
		LoginEmail: "team@x.test", Agent: "claude", BillingProvider: "other", InputTokens: ptrInt(500000), CostUSD: ptrFloat(500),
	}}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if _, err := s.ExcludeAccount(ctx, "personal@x.test", "personal login", "tester"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}

	gap, err := s.CoverageGapStats(ctx, CoverageGapFilter{Since: start, Until: start.Add(40 * 5 * time.Minute)})
	if err != nil {
		t.Fatalf("CoverageGapStats: %v", err)
	}
	if !gap.Eligible {
		t.Fatalf("not eligible: %s (%+v)", gap.Reason, gap.Accounts)
	}
	if len(gap.Accounts) != 1 || gap.Accounts[0].LoginEmail != "team@x.test" {
		t.Fatalf("accounts = %+v, want only the team account", gap.Accounts)
	}
	// 40 events of 1,000 input tokens at $1 per thousand. The proxy request's
	// $500 is compatible-provider traffic and must not appear here.
	if gap.MeasuredUSD != 40 {
		t.Errorf("measured = %v, want 40 (the proxy request must not count)", gap.MeasuredUSD)
	}
	if gap.CoverageRatio < 0.999 || gap.CoverageRatio > 1.001 {
		t.Errorf("ratio = %v, want 1", gap.CoverageRatio)
	}
}

func TestCoverageGapStats_scopesToOneLoginEmail(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	start := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

	seedCoverageAccount(t, s, "a@x.test", "acct-a", 40, 1000, start)
	seedCoverageAccount(t, s, "b@x.test", "acct-b", 40, 1000, start)

	gap, err := s.CoverageGapStats(ctx, CoverageGapFilter{
		Since: start, Until: start.Add(40 * 5 * time.Minute), LoginEmail: "B@x.test",
	})
	if err != nil {
		t.Fatalf("CoverageGapStats: %v", err)
	}
	if len(gap.Accounts) != 1 || gap.Accounts[0].LoginEmail != "b@x.test" {
		t.Fatalf("accounts = %+v, want only b (matched case-insensitively)", gap.Accounts)
	}
}

// The bucket keys the coverage series carries must be byte-identical to the
// ones the trend queries produce, or the band renders as all-zero with no
// error. Postgres is the reference; Go is checked against it.
func TestCoverageGapStats_bucketKeysMatchPostgres(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	loc, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		t.Fatal(err)
	}
	fmts := map[string]string{
		"minute": `YYYY-MM-DD"T"HH24:MI:SS`, "hour": `YYYY-MM-DD"T"HH24:MI:SS`,
		"day": `YYYY-MM-DD`, "week": `YYYY-MM-DD`, "month": `YYYY-MM-DD`,
	}
	for _, at := range []time.Time{
		time.Date(2026, 9, 2, 16, 47, 30, 0, time.UTC),  // crosses midnight in Seoul
		time.Date(2026, 8, 30, 23, 59, 59, 0, time.UTC), // Sunday UTC, Monday KST
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	} {
		for g, f := range fmts {
			var want string
			q := "SELECT to_char(date_trunc('" + g + "', $1::timestamptz AT TIME ZONE 'Asia/Seoul'), '" + f + "')"
			if err := s.pool.QueryRow(ctx, q, at).Scan(&want); err != nil {
				t.Fatalf("postgres: %v", err)
			}
			if got := bucketKey(at, &coverageBucketing{granularity: g, loc: loc}); got != want {
				t.Errorf("%s %s: go=%q postgres=%q", at.Format(time.RFC3339), g, got, want)
			}
		}
	}
}

// One request carrying both scopes must answer exactly what two requests
// answered. That equivalence is the whole claim: the second scope is computed
// from the already-loaded slices, so if it drifted from the separate answer the
// saving would have been bought with a wrong number.
func TestCoverageGapStatsMergedScopeMatchesSeparateRequests(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	start := time.Now().UTC().Add(-10 * 24 * time.Hour).Truncate(time.Hour)
	seedCoverageAccount(t, s, "a@x.test", "acct-merged", 40, 900_000, start)

	bucketSince := start.Add(30 * time.Hour)
	bucketUntil := start.Add(33 * time.Hour)
	headlineSince := start
	headlineUntil := bucketUntil

	separateBuckets, err := s.CoverageGapStats(ctx, CoverageGapFilter{
		Since: bucketSince, Until: bucketUntil, Granularity: "hour", Timezone: "UTC",
	})
	if err != nil {
		t.Fatalf("buckets request: %v", err)
	}
	separateHeadline, err := s.CoverageGapStats(ctx, CoverageGapFilter{
		Since: headlineSince, Until: headlineUntil,
	})
	if err != nil {
		t.Fatalf("headline request: %v", err)
	}

	merged, err := s.CoverageGapStats(ctx, CoverageGapFilter{
		Since: bucketSince, Until: bucketUntil, Granularity: "hour", Timezone: "UTC",
		HeadlineSince: headlineSince, HeadlineUntil: headlineUntil,
	})
	if err != nil {
		t.Fatalf("merged request: %v", err)
	}

	if merged.Headline == nil {
		t.Fatal("merged response carries no headline scope")
	}
	// Compared as JSON so every field counts, including the ones a future change
	// adds without remembering this test.
	mergedTop := *merged
	mergedTop.Headline = nil
	assertSameCoverage(t, "bucket scope", separateBuckets, &mergedTop)
	assertSameCoverage(t, "headline scope", separateHeadline, merged.Headline)
}

// The two scopes coinciding is not a second scope. Reporting one anyway would
// double the payload and invite a reader to compare a number with itself.
func TestCoverageGapStatsOmitsHeadlineWhenScopesCoincide(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	start := time.Now().UTC().Add(-10 * 24 * time.Hour).Truncate(time.Hour)
	seedCoverageAccount(t, s, "a@x.test", "acct-same", 40, 900_000, start)

	until := start.Add(33 * time.Hour)
	got, err := s.CoverageGapStats(ctx, CoverageGapFilter{
		Since: start, Until: until, HeadlineSince: start, HeadlineUntil: until,
	})
	if err != nil {
		t.Fatalf("CoverageGapStats: %v", err)
	}
	if got.Headline != nil {
		t.Error("headline scope reported although it equals the bucket scope")
	}
}

func assertSameCoverage(t *testing.T, what string, want, got *CoverageGap) {
	t.Helper()
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}
	if string(wantJSON) != string(gotJSON) {
		t.Errorf("%s differs between one request and two\n separate: %s\n merged:   %s", what, wantJSON, gotJSON)
	}
}

// The pre-aggregated measured side must answer exactly what aggregating raw
// events answered. It is a speed change, and a speed change that moves the
// number is not a speed change.
func TestCoverageGapStatsMeasuredRollupMatchesRawAggregation(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	start := time.Now().UTC().Add(-10 * 24 * time.Hour).Truncate(time.Hour)
	seedCoverageAccount(t, s, "a@x.test", "acct-rollup", 40, 900_000, start)

	f := CoverageGapFilter{Since: start, Until: start.Add(33 * time.Hour), Granularity: "hour", Timezone: "UTC"}

	// Before the backfill marker exists the loader aggregates raw events.
	fromRaw, err := s.CoverageGapStats(ctx, f)
	if err != nil {
		t.Fatalf("raw path: %v", err)
	}

	if err := s.BackfillCoverageMeasuredMinutes(ctx); err != nil {
		t.Fatalf("BackfillCoverageMeasuredMinutes: %v", err)
	}
	fromRollup, err := s.CoverageGapStats(ctx, f)
	if err != nil {
		t.Fatalf("rollup path: %v", err)
	}
	assertSameCoverage(t, "measured side", fromRaw, fromRollup)
}

// An account excluded after the aggregate was built must leave the measured side
// immediately, because it leaves the readings side immediately. An estimate
// whose numerator and denominator disagree about who counts is worse than a slow
// one, so the account-level exclusions are applied at read time rather than
// baked into the table.
func TestCoverageMeasuredMinutesAppliesExclusionsAtReadTime(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	start := time.Now().UTC().Add(-10 * 24 * time.Hour).Truncate(time.Hour)
	seedCoverageAccount(t, s, "a@x.test", "acct-keep", 40, 900_000, start)
	seedCoverageAccount(t, s, "b@x.test", "acct-drop", 40, 900_000, start)
	if err := s.BackfillCoverageMeasuredMinutes(ctx); err != nil {
		t.Fatalf("BackfillCoverageMeasuredMinutes: %v", err)
	}

	f := CoverageGapFilter{Since: start, Until: start.Add(33 * time.Hour)}
	before, err := s.CoverageGapStats(ctx, f)
	if err != nil {
		t.Fatalf("before exclusion: %v", err)
	}
	if len(before.Accounts) != 2 {
		t.Fatalf("expected both accounts before exclusion, got %+v", before.Accounts)
	}

	// No refresh in between: the point is that the stored rows are untouched and
	// the reader still drops the account.
	if _, err := s.ExcludeAccount(ctx, "b@x.test", "drift test", "test"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}
	after, err := s.CoverageGapStats(ctx, f)
	if err != nil {
		t.Fatalf("after exclusion: %v", err)
	}
	for _, a := range after.Accounts {
		if a.LoginEmail == "b@x.test" {
			t.Error("excluded account still contributes to the measured side")
		}
	}
	if len(after.Accounts) != 1 || after.Accounts[0].LoginEmail != "a@x.test" {
		t.Errorf("accounts = %+v, want only the kept account", after.Accounts)
	}
	if after.MeasuredUSD >= before.MeasuredUSD {
		t.Errorf("measured usage did not fall after excluding an account: %v -> %v", before.MeasuredUSD, after.MeasuredUSD)
	}
}
