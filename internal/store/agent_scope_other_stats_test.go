package store

import (
	"context"
	"testing"
	"time"
)

// TestPgStore_AgentScopeOther_CoversNonClaudeCodexHarnesses pins that the "other"
// harness scope means the same thing on the stats/events path as it already does on
// the session list path (appendAgentFilter): every harness that is not claude and not
// codex. The two paths disagreeing is what made Overview read $0 for a scope whose
// Sessions tab listed 28 sessions -- unified_events stores 'omo'/'gjc' and nothing is
// ever stored as 'other', so an equality test on the scope name can only return zero.
func TestPgStore_AgentScopeOther_CoversNonClaudeCodexHarnesses(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	if err := s.InsertEvents(ctx, []*OtelEvent{{
		Ts: now, EventName: "api_request", SessionID: "scope-claude",
		ProfileEmail: "claude@ex.com", UserID: "u-claude", Model: "claude-sonnet-5",
		CostUSD: ptrFloat(1), InputTokens: ptrInt(10), OutputTokens: ptrInt(5),
		Agent: "claude", BillingProvider: "anthropic",
	}}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	// gjc and omo carry their own exact cost in raw; the view reads it only when the
	// syncer left tokens on the record, so both are set here.
	usageRaw := []byte(`{"message":{"usage":{"cost":{"total":2}}}}`)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{
			Ts: now, SessionID: "scope-omo", UUID: "scope-omo-1", RecordType: "assistant",
			ProfileEmail: "omo@ex.com", UserID: "u-omo", Model: "gpt-5.5",
			InputTokens: ptrInt(20), OutputTokens: ptrInt(6),
			Agent: "omo", BillingProvider: "openai", Raw: usageRaw,
		},
		{
			Ts: now, SessionID: "scope-gjc", UUID: "scope-gjc-1", RecordType: "assistant",
			ProfileEmail: "gjc@ex.com", UserID: "u-gjc", Model: "claude-opus-5",
			InputTokens: ptrInt(30), OutputTokens: ptrInt(7),
			Agent: "gjc", BillingProvider: "anthropic", Raw: usageRaw,
		},
		{
			Ts: now, SessionID: "scope-codex", UUID: "scope-codex-1", RecordType: "usage",
			ProfileEmail: "codex@ex.com", UserID: "u-codex", Model: "gpt-5.5",
			InputTokens: ptrInt(40), OutputTokens: ptrInt(8),
			Agent: "codex", BillingProvider: "openai",
		},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	refreshCodexImputed(t, s)

	since := now.Add(-time.Minute)
	until := now.Add(time.Minute)
	f := func(agent string) EventFilter {
		return EventFilter{Since: &since, Until: &until, Agent: agent}
	}

	// One row per harness in scope: omo + gjc for "other", claude alone for "claude".
	cases := []struct {
		agent string
		want  int64
	}{{"other", 2}, {"claude", 1}}

	for _, c := range cases {
		daily, err := s.DailyStats(ctx, f(c.agent))
		if err != nil {
			t.Fatalf("DailyStats(%s): %v", c.agent, err)
		}
		var events int64
		for _, d := range daily {
			events += d.EventCount
		}
		if events != c.want {
			t.Errorf("DailyStats(%s) event count = %d, want %d", c.agent, events, c.want)
		}

		series, err := s.TimeSeriesStats(ctx, f(c.agent), "day")
		if err != nil {
			t.Fatalf("TimeSeriesStats(%s): %v", c.agent, err)
		}
		events = 0
		for _, d := range series {
			events += d.EventCount
		}
		if events != c.want {
			t.Errorf("TimeSeriesStats(%s) event count = %d, want %d", c.agent, events, c.want)
		}

		byModel, err := s.TimeSeriesStatsByModel(ctx, f(c.agent), "day")
		if err != nil {
			t.Fatalf("TimeSeriesStatsByModel(%s): %v", c.agent, err)
		}
		if got := sumEventCount(byModel); got != c.want {
			t.Errorf("TimeSeriesStatsByModel(%s) event count = %d, want %d", c.agent, got, c.want)
		}

		byUser, err := s.TimeSeriesStatsByUser(ctx, f(c.agent), "day")
		if err != nil {
			t.Fatalf("TimeSeriesStatsByUser(%s): %v", c.agent, err)
		}
		events = 0
		for _, u := range byUser {
			events += u.EventCount
		}
		if events != c.want {
			t.Errorf("TimeSeriesStatsByUser(%s) event count = %d, want %d", c.agent, events, c.want)
		}

		count, err := s.CountEvents(ctx, f(c.agent))
		if err != nil {
			t.Fatalf("CountEvents(%s): %v", c.agent, err)
		}
		if count != c.want {
			t.Errorf("CountEvents(%s) = %d, want %d", c.agent, count, c.want)
		}
	}

	// The latest-activity probe reaches the same column through a per-arm alias, so it
	// takes the format-string form of the predicate and is asserted separately.
	if _, ok, err := s.LatestActivityTs(ctx, f("other")); err != nil {
		t.Fatalf("LatestActivityTs(other): %v", err)
	} else if !ok {
		t.Error("LatestActivityTs(other) found no activity, want the omo/gjc rows")
	}

	// Cost follows the same predicate: $2 per gjc/omo record, and the claude event's
	// $1 must not leak into the other scope.
	daily, err := s.DailyStats(ctx, f("other"))
	if err != nil {
		t.Fatalf("DailyStats(other): %v", err)
	}
	var cost float64
	for _, d := range daily {
		cost += d.CostUSD
	}
	if cost != 4 {
		t.Errorf("DailyStats(other) cost = %v, want 4", cost)
	}
}
