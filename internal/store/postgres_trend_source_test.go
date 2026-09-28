package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// seedTrendEvents writes events spread across hours, models, users and agents,
// so a grouping mistake has somewhere to show up.
func seedTrendEvents(t *testing.T, s *PgStore, base time.Time) {
	t.Helper()
	var events []*OtelEvent
	for i := range 24 {
		events = append(events, &OtelEvent{
			Ts:              base.Add(time.Duration(i) * 90 * time.Minute),
			EventName:       "api_request",
			SessionID:       "sess-trend",
			UserID:          []string{"u1", "u2"}[i%2],
			ProfileEmail:    []string{"a@x.test", "b@x.test"}[i%2],
			LoginEmail:      []string{"b@x.test", "a@x.test"}[i%2],
			UserTeam:        "team-a",
			Model:           []string{"opus 5", "haiku 4.5", "gpt-5.6-sol"}[i%3],
			Agent:           []string{"claude", "gjc"}[i%2],
			BillingProvider: []string{"anthropic", "openai"}[i%2],
			InputTokens:     ptrInt(100 + i),
			OutputTokens:    ptrInt(10 + i),
			CostUSD:         ptrFloat(float64(i) * 0.25),
		})
	}
	if err := s.InsertEvents(context.Background(), events); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
}

// The rollup path is a speed change. A speed change that moves a number is not a
// speed change, so both paths are compared over every granularity the rollup is
// allowed to answer, in a timezone whose offset is not zero -- the buckets are
// cut with `AT TIME ZONE`, and UTC would hide an off-by-one-day grouping.
func TestTrendReadsMatchBetweenRollupAndRaw(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	base := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	seedTrendEvents(t, s, base)

	since := base.Add(-time.Hour)
	until := base.Add(40 * time.Hour)
	f := EventFilter{Since: &since, Until: &until, Timezone: "Asia/Seoul"}

	for _, granularity := range []string{"hour", "day", "week", "month"} {
		t.Run(granularity, func(t *testing.T) {
			// Marker absent -> raw path.
			rawModel, err := s.TimeSeriesStatsByModel(ctx, f, granularity)
			if err != nil {
				t.Fatalf("raw by-model: %v", err)
			}
			rawUser, err := s.TimeSeriesStatsByUser(ctx, f, granularity)
			if err != nil {
				t.Fatalf("raw by-user: %v", err)
			}

			if err := s.RefreshUsageHourlyRollups(ctx, nil); err != nil {
				t.Fatalf("RefreshUsageHourlyRollups: %v", err)
			}
			if _, err := s.pool.Exec(ctx,
				`INSERT INTO schema_backfills (name) VALUES ($1) ON CONFLICT DO NOTHING`,
				usageHourlyRollupBackfill); err != nil {
				t.Fatalf("mark rollup ready: %v", err)
			}
			t.Cleanup(func() {
				if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, usageHourlyRollupBackfill); err != nil {
					t.Fatalf("unmark rollup: %v", err)
				}
			})

			rolledModel, err := s.TimeSeriesStatsByModel(ctx, f, granularity)
			if err != nil {
				t.Fatalf("rollup by-model: %v", err)
			}
			rolledUser, err := s.TimeSeriesStatsByUser(ctx, f, granularity)
			if err != nil {
				t.Fatalf("rollup by-user: %v", err)
			}
			if len(rawModel) == 0 {
				t.Fatal("seed produced no by-model rows; the comparison would be vacuous")
			}
			assertSameJSON(t, "by-model "+granularity, rawModel, rolledModel)
			assertSameJSON(t, "by-user "+granularity, rawUser, rolledUser)
		})
	}
}

// Minute granularity has no rollup bucket fine enough, and a project filter
// reaches events through session_records, which the rollup flattens to one hash
// per session. Both must stay on the raw view.
func TestTrendSourceFallsBackWhereTheRollupCannotAnswer(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO schema_backfills (name) VALUES ($1) ON CONFLICT DO NOTHING`,
		usageHourlyRollupBackfill); err != nil {
		t.Fatalf("mark rollup ready: %v", err)
	}

	if got := s.trendSourceFor(ctx, EventFilter{}, "day"); got.table != rollupTrendSource.table {
		t.Errorf("plain day query reads %s, want the rollup", got.table)
	}
	if got := s.trendSourceFor(ctx, EventFilter{}, "minute"); got.table != rawTrendSource.table {
		t.Errorf("minute query reads %s, want raw", got.table)
	}
	if got := s.trendSourceFor(ctx, EventFilter{ProjectHash: "h"}, "day"); got.table != rawTrendSource.table {
		t.Errorf("project-filtered query reads %s, want raw", got.table)
	}
	if got := s.trendSourceFor(ctx, EventFilter{ProjectHashesPresent: true}, "day"); got.table != rawTrendSource.table {
		t.Errorf("empty-project-scope query reads %s, want raw", got.table)
	}
}

// An unfinished backfill must not be read. It would not fail -- it would report
// less usage than there was, which is indistinguishable from a quiet week.
func TestTrendSourceWaitsForTheBackfill(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	if got := s.trendSourceFor(context.Background(), EventFilter{}, "day"); got.table != rawTrendSource.table {
		t.Errorf("read %s before the backfill finished, want raw", got.table)
	}
}

func assertSameJSON(t *testing.T, what string, want, got any) {
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
		t.Errorf("%s differs between sources\n raw:    %s\n rollup: %s", what, wantJSON, gotJSON)
	}
}

// The rollup's rows are whole hours, but the dashboard sends `since` as now minus
// a window, which is almost never on the hour. Filtering hour buckets by their
// start drops the whole first hour and keeps the whole last one, so the two
// paths disagreed on exactly the windows the chart asks for (#768). Every edge
// range below holds an event on each side of its bound -- 04:30 against the
// since edge at 04:15, and 05:05, 19:05 inside the until edges, 19:30 past one --
// so dropping either edge, or reading past it, moves a number.
func TestTrendReadsMatchOnBoundsOffTheHour(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	base := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	seedTrendEvents(t, s, base)
	var inUntilEdges []*OtelEvent
	for _, d := range []time.Duration{5*time.Hour + 5*time.Minute, 19*time.Hour + 5*time.Minute} {
		inUntilEdges = append(inUntilEdges, &OtelEvent{
			Ts: base.Add(d), EventName: "api_request", SessionID: "sess-trend",
			UserID: "u1", ProfileEmail: "a@x.test", Model: "opus 5", Agent: "claude",
			BillingProvider: "anthropic", InputTokens: ptrInt(7), OutputTokens: ptrInt(3), CostUSD: ptrFloat(0.5),
		})
	}
	if err := s.InsertEvents(ctx, inUntilEdges); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	at := func(d time.Duration) *time.Time { v := base.Add(d); return &v }
	h := time.Hour

	windows := []struct {
		name         string
		since, until *time.Time
	}{
		{"aligned", at(4 * h), at(19 * h)},
		{"since off the hour", at(4*h + 15*time.Minute), at(19 * h)},
		{"until off the hour", at(4 * h), at(19*h + 15*time.Minute)},
		{"both off the hour", at(4*h + 15*time.Minute), at(19*h + 15*time.Minute)},
		{"under an hour across a boundary", at(4*h + 15*time.Minute), at(5*h + 15*time.Minute)},
		{"both in one hour", at(4*h + 15*time.Minute), at(4*h + 45*time.Minute)},
		{"since only", at(4*h + 15*time.Minute), nil},
		{"until only", nil, at(19*h + 15*time.Minute)},
	}
	type result struct{ model, user any }
	read := func(f EventFilter, granularity string) result {
		t.Helper()
		m, err := s.TimeSeriesStatsByModel(ctx, f, granularity)
		if err != nil {
			t.Fatalf("by-model: %v", err)
		}
		u, err := s.TimeSeriesStatsByUser(ctx, f, granularity)
		if err != nil {
			t.Fatalf("by-user: %v", err)
		}
		return result{m, u}
	}

	type key struct{ window, tz, granularity string }
	raw := map[key]result{}
	each := func(fn func(key, EventFilter)) {
		for _, w := range windows {
			for _, tz := range []string{"UTC", "Asia/Seoul"} {
				for _, g := range []string{"hour", "day"} {
					fn(key{w.name, tz, g}, EventFilter{Since: w.since, Until: w.until, Timezone: tz})
				}
			}
		}
	}
	each(func(k key, f EventFilter) { raw[k] = read(f, k.granularity) })
	markUsageRollupsBuilt(t, s)
	each(func(k key, f EventFilter) {
		got := read(f, k.granularity)
		what := k.window + " " + k.tz + " " + k.granularity
		assertSameJSON(t, "by-model "+what, raw[k].model, got.model)
		assertSameJSON(t, "by-user "+what, raw[k].user, got.user)
	})
}
