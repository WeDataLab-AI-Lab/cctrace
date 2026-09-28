package store

import (
	"context"
	"testing"
	"time"
)

func sampleAt(min int, account, window string) *QuotaSample {
	ts := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC).Add(time.Duration(min) * time.Minute)
	wm := 300
	return &QuotaSample{
		BillingProvider: "anthropic",
		AccountID:       account,
		WindowKey:       window,
		SampledAt:       ts,
		UsedPct:         float64(min),
		WindowMinutes:   &wm,
		Plan:            "max",
		Attribution:     AttributionObserved,
	}
}

// Several profiles poll the same billing account at their own moments. Those
// reports are a union, not a conflict: each adds a point the others did not
// have, so the series gets denser the more clients there are.
func TestInsertQuotaSamples_multipleReportersDensifyOneSeries(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	a := sampleAt(0, "acct-1", "session")
	a.ProfileEmail = "one@example.test"
	b := sampleAt(2, "acct-1", "session")
	b.ProfileEmail = "two@example.test"
	c := sampleAt(4, "acct-1", "session")
	c.ProfileEmail = "three@example.test"

	n, err := s.InsertQuotaSamples(ctx, []*QuotaSample{a, b, c})
	if err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
	if n != 3 {
		t.Fatalf("inserted %d, want 3", n)
	}

	got, err := s.ListQuotaSamples(ctx, QuotaSampleFilter{})
	if err != nil {
		t.Fatalf("ListQuotaSamples: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("%d rows, want 3 — reports from different profiles must not collapse", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].SampledAt.Before(got[i-1].SampledAt) {
			t.Fatalf("rows are not in time order: %v before %v", got[i-1].SampledAt, got[i].SampledAt)
		}
	}
}

// The five-minute poll and the five-minute response cache mean the same reading
// is offered repeatedly. It carries the same fetch time, so the key drops it.
// This is also what makes the Codex backfill safe to re-run.
func TestInsertQuotaSamples_repeatedReadingIsIdempotent(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	first := sampleAt(0, "acct-1", "session")
	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{first}); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	// Same account, window and instant; a different reporter re-sending the
	// cached response.
	again := sampleAt(0, "acct-1", "session")
	again.ProfileEmail = "someone-else@example.test"
	n, err := s.InsertQuotaSamples(ctx, []*QuotaSample{again})
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if n != 0 {
		t.Errorf("inserted %d on a repeat, want 0", n)
	}

	got, _ := s.ListQuotaSamples(ctx, QuotaSampleFilter{})
	if len(got) != 1 {
		t.Fatalf("%d rows after a repeat, want 1", len(got))
	}
}

// Windows are rows. Two windows read at the same instant are two rows, not a
// key collision — which is why window_key is part of the primary key.
func TestInsertQuotaSamples_windowsAreRowsNotColumns(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	session := sampleAt(0, "acct-1", "session")
	weekly := sampleAt(0, "acct-1", "weekly_all")
	weekly.UsedPct = 61
	scoped := sampleAt(0, "acct-1", "weekly_scoped")
	scoped.ScopeLabel = "Fable"

	n, err := s.InsertQuotaSamples(ctx, []*QuotaSample{session, weekly, scoped})
	if err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
	if n != 3 {
		t.Fatalf("inserted %d windows at one instant, want 3", n)
	}

	got, _ := s.ListQuotaSamples(ctx, QuotaSampleFilter{})
	var label string
	for _, r := range got {
		if r.WindowKey == "weekly_scoped" {
			label = r.ScopeLabel
		}
	}
	// Not stored as a Sonnet column: the observed scoped model was "Fable", so
	// that column name would have been a lie.
	if label != "Fable" {
		t.Errorf("scope label = %q, want Fable", label)
	}
}

// The same account read through two providers is two series. Codex being
// blocked does not free Claude quota, so they must never merge.
func TestInsertQuotaSamples_providersAreSeparateSeries(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	anthropic := sampleAt(0, "shared-id", "session")
	openai := sampleAt(0, "shared-id", "session")
	openai.BillingProvider = "openai"

	n, err := s.InsertQuotaSamples(ctx, []*QuotaSample{anthropic, openai})
	if err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
	if n != 2 {
		t.Fatalf("inserted %d, want 2 — providers must not share a series", n)
	}
}

func TestListQuotaSamples_filters(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	old := sampleAt(0, "acct-1", "session")
	recent := sampleAt(60, "acct-1", "session")
	weekly := sampleAt(60, "acct-1", "weekly_all")
	wm := 10080
	weekly.WindowMinutes = &wm
	codex := sampleAt(60, "acct-2", "300")
	codex.BillingProvider = "openai"

	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{old, recent, weekly, codex}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}

	from := old.SampledAt.Add(time.Minute)
	if got, _ := s.ListQuotaSamples(ctx, QuotaSampleFilter{From: from}); len(got) != 3 {
		t.Errorf("From filter returned %d rows, want 3", len(got))
	}
	if got, _ := s.ListQuotaSamples(ctx, QuotaSampleFilter{BillingProvider: "openai"}); len(got) != 1 {
		t.Errorf("provider filter returned %d rows, want 1", len(got))
	}
	// 5h and 7d are different time scales; mixing them names no state at all,
	// so the chart asks for one window at a time.
	if got, _ := s.ListQuotaSamples(ctx, QuotaSampleFilter{WindowMinutes: 10080}); len(got) != 1 {
		t.Errorf("window filter returned %d rows, want 1", len(got))
	}
}

// A backfilled sample credited across a gap between observations is present but
// not certain. The chart draws that differently, so the distinction has to
// survive the round trip.
func TestInsertQuotaSamples_keepsAttribution(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	inferred := sampleAt(0, "acct-1", "session")
	inferred.Attribution = AttributionInferred
	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{inferred}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}

	got, _ := s.ListQuotaSamples(ctx, QuotaSampleFilter{})
	if len(got) != 1 || got[0].Attribution != AttributionInferred {
		t.Fatalf("attribution = %+v, want inferred", got)
	}
}

// An account with no account_id cannot be keyed, and guessing one is the
// mis-attribution this whole design avoids. Dropping the row is the honest
// outcome, and it must not take the rest of the batch with it.
func TestInsertQuotaSamples_skipsRowsWithoutAnAccount(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	good := sampleAt(0, "acct-1", "session")
	orphan := sampleAt(1, "", "session")

	n, err := s.InsertQuotaSamples(ctx, []*QuotaSample{orphan, good})
	if err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
	if n != 1 {
		t.Fatalf("inserted %d, want 1 (the unattributed row dropped)", n)
	}
	got, _ := s.ListQuotaSamples(ctx, QuotaSampleFilter{})
	if len(got) != 1 || got[0].AccountID != "acct-1" {
		t.Fatalf("rows = %+v, want only the attributed one", got)
	}
}

// An excluded account's readings must not reach the chart. This table was added
// after the read-time exclusion rule was established and did not inherit it, so
// the account was drawn -- and its subscription price stayed in the denominator
// of the price-weighted average, making the headline percentage wrong rather
// than merely cluttered.
//
// Matching is on login_email because that is what excluded_accounts keys on.
// profile_email is deliberately not a fallback key: one billing account carries
// many profiles, so matching on it would exclude some of an account's rows and
// keep others.
func TestListQuotaSamples_excludesExcludedAccounts(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	excluded := sampleAt(0, "acct-1", "session")
	excluded.LoginEmail = "gone@example.test"
	excluded.ProfileEmail = "shared@example.test"

	kept := sampleAt(60, "acct-2", "session")
	kept.LoginEmail = "stays@example.test"
	// The same profile reports for both accounts. Were the filter to match on
	// profile_email this row would vanish with the other one.
	kept.ProfileEmail = "shared@example.test"

	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{excluded, kept}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
	if _, err := s.ExcludeAccount(ctx, "gone@example.test", "test", "test"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}

	got, err := s.ListQuotaSamples(ctx, QuotaSampleFilter{})
	if err != nil {
		t.Fatalf("ListQuotaSamples: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("returned %d rows, want 1 (the excluded account's readings must not reach the chart)", len(got))
	}
	if got[0].LoginEmail != "stays@example.test" {
		t.Errorf("kept row login_email = %q, want the non-excluded account", got[0].LoginEmail)
	}
}

// bucketSeconds decides whether a range is downsampled at all.
func TestBucketSeconds(t *testing.T) {
	base := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)

	// 26 weeks over a thousand points is a bucket of a few hours. This is the
	// range that returned 109,212 raw rows and timed out mid-encode.
	if got := bucketSeconds(base, base.Add(26*7*24*time.Hour)); got != 15724 {
		t.Errorf("26w bucket = %ds, want 15724", got)
	}
	// A range shorter than one second per point is returned raw: bucketing there
	// could only lose resolution without saving anything.
	if got := bucketSeconds(base, base.Add(10*time.Minute)); got != 0 {
		t.Errorf("10m bucket = %d, want 0 (raw)", got)
	}
	// An unbounded range has nothing to divide, and bucketing on a guess would
	// change what an unfiltered read means.
	for _, tc := range []struct {
		name     string
		from, to time.Time
	}{
		{"no from", time.Time{}, base},
		{"no to", base, time.Time{}},
		{"inverted", base.Add(time.Hour), base},
	} {
		if got := bucketSeconds(tc.from, tc.to); got != 0 {
			t.Errorf("%s bucket = %d, want 0", tc.name, got)
		}
	}
}

// A downsampled range returns one row per bucket per series, carrying the
// bucket's closing reading. Utilization is a gauge that climbs until its window
// resets, so the last value is what the step line between two points asserts.
func TestListQuotaSamples_downsamplesWideRanges(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	// Four readings a minute apart. Over a range wide enough to bucket by hours,
	// all four land in one bucket.
	var rows []*QuotaSample
	for i := range 4 {
		rows = append(rows, sampleAt(i, "acct-1", "session"))
	}
	if _, err := s.InsertQuotaSamples(ctx, rows); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}

	from := rows[0].SampledAt.Add(-time.Hour)
	got, err := s.ListQuotaSamples(ctx, QuotaSampleFilter{From: from, To: from.Add(26 * 7 * 24 * time.Hour)})
	if err != nil {
		t.Fatalf("ListQuotaSamples: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("returned %d rows, want 1 -- the four readings share a bucket", len(got))
	}
	// sampleAt sets UsedPct to the minute offset, so the last reading is 3.
	if got[0].UsedPct != 3 {
		t.Errorf("used_pct = %v, want 3 (the bucket's closing reading)", got[0].UsedPct)
	}
}

// Downsampling must not merge separate accounts or separate windows: each is its
// own line, and a bucket belongs to one of them.
func TestListQuotaSamples_downsampleKeepsSeriesApart(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	a := sampleAt(0, "acct-1", "session")
	b := sampleAt(1, "acct-2", "session")
	weekly := sampleAt(2, "acct-1", "weekly_all")
	wm := 10080
	weekly.WindowMinutes = &wm
	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{a, b, weekly}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}

	from := a.SampledAt.Add(-time.Hour)
	got, _ := s.ListQuotaSamples(ctx, QuotaSampleFilter{From: from, To: from.Add(26 * 7 * 24 * time.Hour)})
	if len(got) != 3 {
		t.Fatalf("returned %d rows, want 3 -- two accounts and two windows are three series", len(got))
	}
}

// A bucket holding any inferred reading is inferred. Reporting only the
// surviving row's attribution would let a mixed bucket pass as observed, which
// is the one claim the flag exists to keep honest.
func TestListQuotaSamples_downsampleKeepsInferred(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	early := sampleAt(0, "acct-1", "session")
	early.Attribution = AttributionInferred
	late := sampleAt(1, "acct-1", "session")
	late.Attribution = AttributionObserved
	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{early, late}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}

	from := early.SampledAt.Add(-time.Hour)
	got, _ := s.ListQuotaSamples(ctx, QuotaSampleFilter{From: from, To: from.Add(26 * 7 * 24 * time.Hour)})
	if len(got) != 1 {
		t.Fatalf("returned %d rows, want 1", len(got))
	}
	if got[0].Attribution != AttributionInferred {
		t.Errorf("attribution = %q, want inferred -- the bucket mixes both", got[0].Attribution)
	}
}

// A narrow range keeps every reading: the chart's short views are exact.
func TestListQuotaSamples_narrowRangeIsNotDownsampled(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	var rows []*QuotaSample
	for i := range 4 {
		rows = append(rows, sampleAt(i, "acct-1", "session"))
	}
	if _, err := s.InsertQuotaSamples(ctx, rows); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}

	from := rows[0].SampledAt.Add(-time.Minute)
	got, _ := s.ListQuotaSamples(ctx, QuotaSampleFilter{From: from, To: from.Add(10 * time.Minute)})
	if len(got) != 4 {
		t.Errorf("returned %d rows, want all 4 kept", len(got))
	}
}

// The source survives the round trip, which is the whole point of the column:
// a reading whose account was inferred can be checked against the session log
// that produced it.
func TestInsertQuotaSamples_keepsSourceSession(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	const sessionID = "00000000-0000-4000-8000-000000000001"
	row := sampleAt(0, "acct-1", "session")
	row.Attribution = AttributionInferred
	row.SourceSessionID = sessionID
	live := sampleAt(60, "acct-2", "session")

	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{row, live}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}

	got, err := s.ListQuotaSamples(ctx, QuotaSampleFilter{})
	if err != nil {
		t.Fatalf("ListQuotaSamples: %v", err)
	}
	by := map[string]string{}
	for _, r := range got {
		by[r.AccountID] = r.SourceSessionID
	}
	if by["acct-1"] != sessionID {
		t.Errorf("source_session_id = %q, want %q", by["acct-1"], sessionID)
	}
	// A live read has no session log behind it and was never inferred, so empty
	// means "nothing to check against", not "source lost".
	if by["acct-2"] != "" {
		t.Errorf("live reading carried source %q, want empty", by["acct-2"])
	}
}
