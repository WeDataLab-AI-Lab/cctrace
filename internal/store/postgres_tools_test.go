package store

import (
	"context"
	"testing"
	"time"
)

// TestPgStore_ToolTimeSeries_Timezone pins tool buckets to the caller's timezone.
//
// Every other chart on the dashboard buckets with `AT TIME ZONE tz`
// (postgres_stats.go, postgres_stats_model.go, postgres_stats_user.go) and the
// frontend renders the returned string by slicing it (tool-detail-panel.tsx
// formatTick), never by parsing it as an instant. A UTC-bucketed string
// therefore renders as a UTC wall clock labelled as local: for KST the tool
// chart sits 9 hours off every other chart over the same range.
func TestPgStore_ToolTimeSeries_Timezone(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	ok := true
	// 2026-08-18 00:30 KST == 2026-08-17 15:30 UTC. The two readings disagree on
	// the day, which is exactly what a user in Seoul sees as a misplaced bar.
	kstEarly := time.Date(2026, 8, 17, 15, 30, 0, 0, time.UTC)
	// 2026-08-18 20:00 KST == 2026-08-18 11:00 UTC. Same KST day as kstEarly,
	// so a correct KST bucketing collapses both into one bar.
	kstLate := time.Date(2026, 8, 18, 11, 0, 0, 0, time.UTC)

	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: kstEarly, EventName: "tool_result", SessionID: "s1", ToolName: "Bash", ToolSuccess: &ok},
		{Ts: kstLate, EventName: "tool_result", SessionID: "s1", ToolName: "Bash", ToolSuccess: &ok},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	got, err := s.ToolTimeSeries(ctx, "Bash", since, until, "", "", "", "day", "Asia/Seoul")
	if err != nil {
		t.Fatalf("ToolTimeSeries: %v", err)
	}
	if len(got) != 1 {
		dates := make([]string, len(got))
		for i, b := range got {
			dates[i] = b.Date
		}
		t.Fatalf("buckets = %v, want one KST day (2026-08-18)", dates)
	}
	if got[0].Date != "2026-08-18" {
		t.Fatalf("bucket date = %q, want 2026-08-18 (KST)", got[0].Date)
	}
	if got[0].SuccessCount != 2 {
		t.Fatalf("success count = %d, want 2", got[0].SuccessCount)
	}
}

// An empty tz must keep the previous behaviour rather than erroring: the
// handler only forwards what the client sent, and older clients send nothing.
func TestPgStore_ToolTimeSeries_EmptyTimezoneIsUTC(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	ok := true
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: time.Date(2026, 8, 17, 15, 30, 0, 0, time.UTC), EventName: "tool_result", SessionID: "s1", ToolName: "Bash", ToolSuccess: &ok},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	got, err := s.ToolTimeSeries(ctx, "Bash",
		time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		"", "", "", "day", "")
	if err != nil {
		t.Fatalf("ToolTimeSeries: %v", err)
	}
	if len(got) != 1 || got[0].Date != "2026-08-17" {
		t.Fatalf("got %+v, want single 2026-08-17 bucket (UTC)", got)
	}
}

// Codex sends no tool_result event: its calls arrive as the codex.tool.call delta
// metric (#698). The Tools screen and the Open API tool endpoints count them with
// Claude's, under the tool name each agent reports. Failure detail stays
// Claude-only -- a metric has no individual call to show.
func TestPgStore_ToolsIncludeCodexToolCallMetric(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	at := time.Date(2026, 8, 18, 3, 0, 0, 0, time.UTC)
	ok, failed := true, false
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: at, EventName: "tool_result", SessionID: "s1", UserID: "u1", ToolName: "shell", ToolSuccess: &ok},
		{Ts: at, EventName: "tool_result", SessionID: "s1", UserID: "u1", ToolName: "shell", ToolSuccess: &failed},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	three, one, nine := int64(3), int64(1), int64(9)
	metric := func(user, success string, v *int64) *OtelMetric {
		return &OtelMetric{Ts: at, MetricName: "codex.tool.call", UserID: user, Agent: "codex", BillingProvider: "openai",
			ValueInt: v, Dimensions: map[string]interface{}{"tool": "shell", "success": success}}
	}
	if err := s.InsertMetrics(ctx, []*OtelMetric{
		metric("u1", "true", &three),
		metric("u1", "false", &one),
		metric("u2", "false", &nine), // filtered out by user
	}); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}
	since, until := at.Add(-time.Hour), at.Add(time.Hour)

	usage, err := s.ToolUsage(ctx, since, until, "", "", "u1")
	if err != nil {
		t.Fatalf("ToolUsage: %v", err)
	}
	if len(usage) != 1 || usage[0].ToolName != "shell" || usage[0].UseCount != 6 || usage[0].SuccessCount != 4 || usage[0].FailCount != 2 {
		t.Fatalf("usage = %+v, want shell 6 uses, 4 ok, 2 failed", usage)
	}

	series, err := s.ToolTimeSeries(ctx, "shell", since, until, "", "", "u1", "day", "UTC")
	if err != nil {
		t.Fatalf("ToolTimeSeries: %v", err)
	}
	if len(series) != 1 || series[0].SuccessCount != 4 || series[0].FailCount != 2 {
		t.Fatalf("series = %+v, want one bucket with 4 ok and 2 failed", series)
	}

	failures, err := s.ToolFailures(ctx, "shell", since, until, "", "", "u1", 50)
	if err != nil {
		t.Fatalf("ToolFailures: %v", err)
	}
	if len(failures) != 1 {
		t.Fatalf("failures = %d, want only Claude's one failed call", len(failures))
	}
}
