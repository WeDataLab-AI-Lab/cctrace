package store

import (
	"context"
	"testing"
	"time"
)

// The rollup's whole contract is that it answers the same question as the raw
// view. Every other property -- size, speed, surviving retention -- is worthless
// if the numbers differ, so the golden diff is the first test and the one that
// gates switching reads over.
func TestUsageHourlyRollupMatchesRaw(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	// Two hours, two models, two users, two agents: enough for the GROUP BY to
	// have something to get wrong, and small enough to read in a failure message.
	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	var events []*OtelEvent
	for i := range 8 {
		events = append(events, &OtelEvent{
			Ts:        base.Add(time.Duration(i) * 20 * time.Minute),
			EventName: "api_request",
			SessionID: "sess-rollup",
			UserID:    []string{"u1", "u2"}[i%2],
			// Crossed on purpose: profile and login differ on every row, so the
			// rollup cannot pass by writing one column into the other's slot.
			ProfileEmail:    []string{"a@x.test", "b@x.test"}[i%2],
			LoginEmail:      []string{"b@x.test", "a@x.test"}[i%2],
			UserTeam:        "team-a",
			Model:           []string{"opus 5", "haiku 4.5"}[i%2],
			Agent:           []string{"claude", "gjc"}[i%2],
			BillingProvider: "anthropic",
			InputTokens:     ptrInt(100 + i),
			OutputTokens:    ptrInt(10 + i),
			CostUSD:         ptrFloat(float64(i) * 0.5),
		})
	}
	if err := s.InsertEvents(ctx, events); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	if err := s.RefreshUsageHourlyRollups(ctx, nil); err != nil {
		t.Fatalf("RefreshUsageHourlyRollups: %v", err)
	}

	// Compared on the full key, not on a grand total: a grand total passes even
	// when rows are grouped under the wrong bucket or the wrong model.
	const diff = `
	WITH raw AS (
		SELECT date_trunc('hour', ts) AS bucket, COALESCE(model,'') AS model,
		       COALESCE(user_id,'') AS user_id, COALESCE(profile_email,'') AS profile_email,
		       COALESCE(login_email,'') AS login_email, COALESCE(user_team,'') AS user_team,
		       COALESCE(agent,'') AS agent, COALESCE(billing_provider,'') AS billing_provider,
		       sum(COALESCE(input_tokens,0))::bigint AS input_tokens,
		       sum(COALESCE(output_tokens,0))::bigint AS output_tokens,
		       round(sum(COALESCE(cost_usd,0))::numeric, 9) AS cost_usd,
		       count(*)::bigint AS event_count
		FROM visible_events
		GROUP BY 1,2,3,4,5,6,7,8
	), rolled AS (
		SELECT bucket, model, user_id, profile_email, login_email, user_team, agent,
		       billing_provider, input_tokens, output_tokens,
		       round(cost_usd::numeric, 9) AS cost_usd, event_count
		FROM usage_hourly_rollups
	)
	SELECT count(*) FROM (
		(TABLE raw EXCEPT ALL TABLE rolled) UNION ALL (TABLE rolled EXCEPT ALL TABLE raw)
	) d`

	var mismatches int
	if err := s.pool.QueryRow(ctx, diff).Scan(&mismatches); err != nil {
		t.Fatalf("golden diff: %v", err)
	}
	if mismatches != 0 {
		t.Errorf("rollup and raw disagree on %d rows", mismatches)
	}
}

// A refresh must be idempotent. The periodic tick reruns it over an overlapping
// window forever, so a refresh that adds instead of replacing doubles every
// number on the second tick -- and the totals stay plausible while it happens.
func TestUsageHourlyRollupRefreshIsIdempotent(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	base := time.Date(2026, 3, 2, 8, 0, 0, 0, time.UTC)
	if err := s.InsertEvents(ctx, []*OtelEvent{{
		Ts: base, EventName: "api_request", SessionID: "sess-idem", UserID: "u1",
		LoginEmail: "a@x.test", Agent: "claude", BillingProvider: "anthropic",
		InputTokens: ptrInt(500), OutputTokens: ptrInt(50), CostUSD: ptrFloat(1.25),
	}}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	for range 3 {
		if err := s.RefreshUsageHourlyRollups(ctx, nil); err != nil {
			t.Fatalf("RefreshUsageHourlyRollups: %v", err)
		}
	}

	var rows int
	var input int64
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*), COALESCE(sum(input_tokens),0) FROM usage_hourly_rollups`,
	).Scan(&rows, &input); err != nil {
		t.Fatalf("read rollup: %v", err)
	}
	if rows != 1 || input != 500 {
		t.Errorf("after 3 refreshes: rows=%d input=%d, want rows=1 input=500", rows, input)
	}
}

// A windowed refresh must not touch buckets outside its window. The trailing
// window exists so the periodic tick stays cheap; if it rebuilt everything the
// window would be decoration, and if it deleted outside the window it would
// erase the history the rollup exists to keep.
func TestUsageHourlyRollupWindowLeavesOlderBucketsAlone(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	old := time.Date(2026, 3, 3, 1, 0, 0, 0, time.UTC)
	recent := old.Add(48 * time.Hour)
	if err := s.InsertEvents(ctx, []*OtelEvent{{
		Ts: old, EventName: "api_request", SessionID: "s-old", UserID: "u1",
		Agent: "claude", BillingProvider: "anthropic", InputTokens: ptrInt(7),
	}, {
		Ts: recent, EventName: "api_request", SessionID: "s-new", UserID: "u1",
		Agent: "claude", BillingProvider: "anthropic", InputTokens: ptrInt(9),
	}}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if err := s.RefreshUsageHourlyRollups(ctx, nil); err != nil {
		t.Fatalf("full refresh: %v", err)
	}

	// Delete the old event, then refresh only the recent window. The old rollup
	// row must survive: that is exactly the case where the source is gone and the
	// rollup is the only remaining record.
	if _, err := s.pool.Exec(ctx, `DELETE FROM otel_events WHERE session_id = 's-old'`); err != nil {
		t.Fatalf("delete source: %v", err)
	}
	from := recent.Add(-time.Hour)
	if err := s.RefreshUsageHourlyRollups(ctx, &from); err != nil {
		t.Fatalf("windowed refresh: %v", err)
	}

	var kept int64
	if err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(sum(input_tokens),0) FROM usage_hourly_rollups WHERE bucket = $1`, old,
	).Scan(&kept); err != nil {
		t.Fatalf("read old bucket: %v", err)
	}
	if kept != 7 {
		t.Errorf("old bucket input_tokens = %d, want 7 (windowed refresh must not touch it)", kept)
	}
}

// A session is not guaranteed one project hash. When it carries several, a join
// on session_id alone yields one partner per hash and multiplies every event of
// that session -- silently, because the inflated totals stay plausible.
//
// Measured on a copy of prod: 337 sessions carry more than one hash, and the
// rows whose session_id is empty carry thirty between them, which inflated
// event_count by 29,605 against the raw view. That is what this pins.
func TestUsageHourlyRollupDoesNotMultiplyOnRepeatedProjectHash(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	base := time.Date(2026, 3, 4, 5, 0, 0, 0, time.UTC)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: base, SessionID: "s-multi", ProjectHash: "hash-old", RecordType: "user"},
		{Ts: base.Add(time.Minute), SessionID: "s-multi", ProjectHash: "hash-new", RecordType: "user"},
		// Not a session, so it can have no project. Left in the join it becomes a
		// partner for every event whose session_id is empty.
		{Ts: base, SessionID: "", ProjectHash: "hash-orphan", RecordType: "user"},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.InsertEvents(ctx, []*OtelEvent{{
		Ts: base.Add(2 * time.Minute), EventName: "api_request", SessionID: "s-multi",
		UserID: "u1", Agent: "claude", BillingProvider: "anthropic",
		InputTokens: ptrInt(100), CostUSD: ptrFloat(1),
	}}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if err := s.RefreshUsageHourlyRollups(ctx, nil); err != nil {
		t.Fatalf("RefreshUsageHourlyRollups: %v", err)
	}

	var rows int
	var events int64
	var input int64
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*), COALESCE(sum(event_count),0), COALESCE(sum(input_tokens),0) FROM usage_hourly_rollups`,
	).Scan(&rows, &events, &input); err != nil {
		t.Fatalf("read rollup: %v", err)
	}
	if rows != 1 || events != 1 || input != 100 {
		t.Errorf("rows=%d events=%d input=%d, want 1/1/100 (one event must stay one)", rows, events, input)
	}
}

// The frequent tick rebuilds a tail; the slow pass rebuilds a wide window that
// catches late arrivals. If the tail ever grew past the wide one, the cheap tick
// would be doing the expensive work every minute -- and the constant that says
// how far back late data is accepted would silently be the tail's.
func TestRollupFreshWindowsAreTailsOfTheWideOnes(t *testing.T) {
	if UsageRollupFreshWindow >= UsageRollupWindow {
		t.Errorf("usage fresh window %v is not a tail of %v", UsageRollupFreshWindow, UsageRollupWindow)
	}
	if CoverageMinutesFreshWindow >= CoverageMinutesWindow {
		t.Errorf("coverage fresh window %v is not a tail of %v", CoverageMinutesFreshWindow, CoverageMinutesWindow)
	}
}

// Trend reads split a window at UTC hours (Go's Truncate) and read the partial
// ends raw, so a rollup bucket must be a UTC hour too. date_trunc on a
// timestamptz follows the session TimeZone; under a half-hour offset it would cut
// buckets at :30 UTC and the whole-hour and raw parts of a window would overlap.
func TestUsageHourlyRollupBucketsAreUTCHours(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	ts := time.Date(2026, 3, 1, 10, 45, 0, 0, time.UTC)
	if err := s.InsertEvents(ctx, []*OtelEvent{{
		Ts: ts, EventName: "api_request", SessionID: "sess-utc", UserID: "u1",
		Model: "opus 5", Agent: "claude", BillingProvider: "anthropic", CostUSD: ptrFloat(1),
	}}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // test cleanup
	if _, err := tx.Exec(ctx, `SET LOCAL TimeZone = 'Asia/Kolkata'`); err != nil {
		t.Fatalf("set timezone: %v", err)
	}
	if err := refreshUsageHourlyRollups(ctx, tx, nil); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	var bucket time.Time
	if err := tx.QueryRow(ctx, `SELECT bucket FROM usage_hourly_rollups`).Scan(&bucket); err != nil {
		t.Fatalf("read bucket: %v", err)
	}
	if want := ts.Truncate(time.Hour); !bucket.Equal(want) {
		t.Errorf("bucket = %s, want the UTC hour %s", bucket.UTC(), want)
	}
}
