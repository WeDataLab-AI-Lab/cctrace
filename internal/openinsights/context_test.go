package openinsights

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func intp(v int) *int           { return &v }
func floatp(v float64) *float64 { return &v }

func req(session string, at time.Duration, input, read, create int, cost float64) Event {
	return Event{
		Ts: t1.Add(at), SessionID: session, UserID: "u1", Model: "opus", CostUSD: floatp(cost),
		InputTokens: intp(input), CacheReadTokens: intp(read), CacheCreateTokens: intp(create),
	}
}

func TestAggregateContextHitRateIgnoresRowsWithoutTokens(t *testing.T) {
	events := []Event{
		req("s", 0, 100, 0, 900, 1),
		req("s", time.Minute, 100, 900, 0, 1),
		{Ts: t1, SessionID: "s", UserID: "u1"}, // a tool event carries no tokens
	}

	got := AggregateContext(Window{t1, t2}, events, 5, ScopeUser)

	if got.Overall.Requests != 2 || got.Overall.ContextTokens != 2000 {
		t.Fatalf("overall = %+v", got.Overall)
	}
	if got.Overall.HitRate == nil || *got.Overall.HitRate != 0.45 {
		t.Fatalf("hit rate = %v, want 0.45", got.Overall.HitRate)
	}
	if len(got.ByModel) != 1 || got.ByModel[0].Model != "opus" {
		t.Fatalf("by_model = %+v", got.ByModel)
	}
}

func TestAggregateContextCountsRebuildsAfterTheFirstRequestOnly(t *testing.T) {
	events := []Event{
		// input order is irrelevant: the late rebuild is listed first
		req("s", 40*time.Minute, 10, 1_000, 30_000, 3),
		req("s", 0, 10, 0, 30_000, 1),                  // first request: a cold start, not a rebuild
		req("s", 2*time.Minute, 10, 29_000, 1_000, 1),  // warm
		req("s", 10*time.Minute, 10, 4_000, 15_000, 1), // create > read but under 20k context: ignored
		req("t", 0, 10, 0, 50_000, 1),                  // another session's first request
	}

	got := AggregateContext(Window{t1, t2}, events, 5, ScopeUser)

	if got.Rebuilds.Count != 1 || got.Rebuilds.Tokens != 30_000 {
		t.Fatalf("rebuilds = %+v", got.Rebuilds)
	}
	if got.Rebuilds.MedianGapSeconds == nil || *got.Rebuilds.MedianGapSeconds != 30*60 {
		t.Fatalf("median gap = %v, want 1800", got.Rebuilds.MedianGapSeconds)
	}
}

func TestAggregateContextBloatedShareAndSessions(t *testing.T) {
	events := []Event{
		req("small", 0, 10, 0, 1_000, 1),
		req("big", 0, 10, 99_990, 0, 2), // exactly 100k: bloated
		req("big", time.Minute, 10, 150_000, 0, 3),
		req("mid", 0, 10, 120_000, 0, 4),
	}

	got := AggregateContext(Window{t1, t2}, events, 5, ScopeUser)

	if got.Bloated.Requests != 3 || got.Bloated.CostShare == nil || *got.Bloated.CostShare != 0.9 {
		t.Fatalf("bloated = %+v share=%v", got.Bloated, got.Bloated.CostShare)
	}
	if len(got.BloatedSessions) != 2 || got.BloatedSessions[0].SessionID != "big" {
		t.Fatalf("bloated sessions = %+v", got.BloatedSessions)
	}
	if b := got.BloatedSessions[0]; b.BloatedRequests != 2 || b.MaxContextTokens != 150_010 || b.CostUSD != 5 {
		t.Fatalf("big session = %+v", b)
	}
}

func TestAggregateContextEmptyWindow(t *testing.T) {
	got := AggregateContext(Window{t1, t2}, nil, 5, ScopeUser)

	if got.Overall.HitRate != nil || got.Bloated.CostShare != nil || got.Rebuilds.MedianGapSeconds != nil {
		t.Fatalf("empty window should carry nulls: %+v", got)
	}
	if !hasCaveat(got.Caveats, "window_empty") || !hasCaveat(got.Caveats, "low_volume") {
		t.Fatalf("caveats = %+v", got.Caveats)
	}
}

func TestResultsEncodeEmptyListsAsArrays(t *testing.T) {
	cases := map[string]struct {
		v     any
		lists []string
	}{
		"context": {AggregateContext(Window{t1, t2}, nil, 5, ScopeUser), []string{"by_model", "bloated_sessions", "caveats"}},
		"cost":    {AggregateCost(Window{t0, t1}, Window{t1, t2}, nil, nil, nil, nil, 5, ScopeUser), []string{"by_model", "top_sessions", "caveats"}},
	}
	for name, c := range cases {
		b, err := json.Marshal(c.v)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range c.lists {
			if !strings.Contains(string(b), `"`+field+`":[`) {
				t.Errorf("%s: %q is not an array in %s", name, field, b)
			}
		}
	}
}

func TestResultTimestampsAreUTC(t *testing.T) {
	seoul := time.FixedZone("KST", 9*60*60)
	s := sess("a", "opus", 1)
	s.StartTime = t1.In(seoul)
	e := req("s", 0, 10, 0, 200_000, 1)
	e.Ts = e.Ts.In(seoul)

	cost := AggregateCost(Window{t0, t1}, Window{t1, t2}, nil, []Session{s}, nil, nil, 5, ScopeUser)
	ctx := AggregateContext(Window{t1, t2}, []Event{e}, 5, ScopeUser)

	for name, ts := range map[string]time.Time{
		"top_sessions.start_time":   cost.TopSessions[0].StartTime,
		"bloated_sessions.first_ts": ctx.BloatedSessions[0].FirstTs,
		"bloated_sessions.last_ts":  ctx.BloatedSessions[0].LastTs,
	} {
		if ts.Location() != time.UTC {
			t.Errorf("%s = %v, want UTC", name, ts)
		}
	}
}

func TestAggregateContextIgnoresRebuildsWithoutAPause(t *testing.T) {
	events := []Event{
		req("s", 0, 10, 0, 30_000, 1),
		req("s", 0, 10, 0, 30_000, 1),               // parallel subagent start at the same instant
		req("s", time.Minute, 10, 2_000, 30_000, 1), // cold subagent a minute later: cache still warm elsewhere
		req("s", 20*time.Minute, 10, 1_000, 30_000, 1),
	}

	got := AggregateContext(Window{t1, t2}, events, 5, ScopeUser)

	if got.Rebuilds.Count != 1 || got.Rebuilds.MinGapSeconds != 300 {
		t.Fatalf("rebuilds = %+v", got.Rebuilds)
	}
	if *got.Rebuilds.MedianGapSeconds != 19*60 {
		t.Fatalf("median gap = %v", *got.Rebuilds.MedianGapSeconds)
	}
}
