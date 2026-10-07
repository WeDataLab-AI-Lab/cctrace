package store

import (
	"context"
	"testing"
	"time"
)

// TestRangeBoundaryIsHalfOpen pins every time-range query to the same half-open
// convention: ts >= since AND ts < until.
//
// Most of the store already does this (postgres_cost.go, postgres_events.go,
// postgres_metrics.go, postgres_session_records.go, postgres_skill_usage.go,
// postgres_tools.go). The stats family used a closed upper bound instead, so an
// event landing exactly on `until` was counted by the stats charts and skipped
// by every other query over the same range -- and counted twice when a caller
// walked adjacent windows.
func TestRangeBoundaryIsHalfOpen(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	since := recentDay(20)
	until := since.AddDate(0, 0, 1)

	inside := since.Add(time.Hour)
	ok := true
	cost, in, out := 1.0, 10, 20
	// One event strictly inside the range, one exactly on `until`. Only the
	// first belongs to [since, until).
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: inside, EventName: "api_request", SessionID: "s1", Model: "claude-opus-5",
			CostUSD: &cost, InputTokens: &in, OutputTokens: &out},
		{Ts: until, EventName: "api_request", SessionID: "s1", Model: "claude-opus-5",
			CostUSD: &cost, InputTokens: &in, OutputTokens: &out},
		{Ts: inside, EventName: "tool_result", SessionID: "s1", ToolName: "Bash", ToolSuccess: &ok},
		{Ts: until, EventName: "tool_result", SessionID: "s1", ToolName: "Bash", ToolSuccess: &ok},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	f := EventFilter{Since: &since, Until: &until}

	sum := func(counts ...int64) int64 {
		var n int64
		for _, c := range counts {
			n += c
		}
		return n
	}

	t.Run("DailyStats", func(t *testing.T) {
		got, err := s.DailyStats(ctx, f)
		if err != nil {
			t.Fatalf("DailyStats: %v", err)
		}
		var n int64
		for _, d := range got {
			n = sum(n, d.EventCount)
		}
		// The stats family counts every visible_events row, so both `inside`
		// rows belong to the range and both `until` rows must not.
		if n != 2 {
			t.Fatalf("event count = %d, want 2 (the two ts==until rows must be excluded)", n)
		}
	})

	t.Run("TimeSeriesStats", func(t *testing.T) {
		got, err := s.TimeSeriesStats(ctx, f, "day")
		if err != nil {
			t.Fatalf("TimeSeriesStats: %v", err)
		}
		var n int64
		for _, d := range got {
			n = sum(n, d.EventCount)
		}
		if n != 2 {
			t.Fatalf("event count = %d, want 2", n)
		}
	})

	t.Run("TimeSeriesStatsByModel", func(t *testing.T) {
		got, err := s.TimeSeriesStatsByModel(ctx, f, "day")
		if err != nil {
			t.Fatalf("TimeSeriesStatsByModel: %v", err)
		}
		var n int64
		for _, d := range got {
			n = sum(n, d.EventCount)
		}
		if n != 2 {
			t.Fatalf("event count = %d, want 2", n)
		}
	})

	t.Run("TimeSeriesStatsByUser", func(t *testing.T) {
		got, err := s.TimeSeriesStatsByUser(ctx, f, "day")
		if err != nil {
			t.Fatalf("TimeSeriesStatsByUser: %v", err)
		}
		var n int64
		for _, d := range got {
			n = sum(n, d.EventCount)
		}
		if n != 2 {
			t.Fatalf("event count = %d, want 2", n)
		}
	})

	t.Run("CostByModel", func(t *testing.T) {
		got, err := s.CostByModel(ctx, since, until, "", "", "")
		if err != nil {
			t.Fatalf("CostByModel: %v", err)
		}
		var n int64
		for _, m := range got {
			n = sum(n, m.RequestCount)
		}
		if n != 1 {
			t.Fatalf("request count = %d, want 1", n)
		}
	})

	// The half-open convention is only worth pinning because the rest of the
	// store already follows it. ToolUsage is the reference implementation.
	t.Run("ToolUsage_reference", func(t *testing.T) {
		got, err := s.ToolUsage(ctx, since, until, "", "", "")
		if err != nil {
			t.Fatalf("ToolUsage: %v", err)
		}
		var n int64
		for _, u := range got {
			n = sum(n, u.UseCount)
		}
		if n != 1 {
			t.Fatalf("use count = %d, want 1", n)
		}
	})
}
