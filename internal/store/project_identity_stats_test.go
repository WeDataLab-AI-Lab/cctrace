package store

import (
	"context"
	"testing"
	"time"
)

func TestPgStore_ProjectIdentityStats_MultiHashFiltersSiblingProjects(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	input := func(v int) *int { return &v }
	records := []*SessionRecord{
		{Ts: now, SessionID: "stats-main", UUID: "stats-main", RecordType: "assistant", ProfileEmail: "stats@example.test", UserID: "stats-user", ProjectHash: "h-main", InputTokens: input(100), OutputTokens: input(50), Raw: []byte(`{"message":{"role":"assistant"}}`)},
		{Ts: now.Add(time.Minute), SessionID: "stats-worktree", UUID: "stats-worktree", RecordType: "assistant", ProfileEmail: "stats@example.test", UserID: "stats-user", ProjectHash: "h-worktree", InputTokens: input(100), OutputTokens: input(50), Raw: []byte(`{"message":{"role":"assistant"}}`)},
		{Ts: now.Add(2 * time.Minute), SessionID: "stats-subpath", UUID: "stats-subpath", RecordType: "assistant", ProfileEmail: "stats@example.test", UserID: "stats-user", ProjectHash: "h-subpath", InputTokens: input(100), OutputTokens: input(50), Raw: []byte(`{"message":{"role":"assistant"}}`)},
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: now, EventName: "api_request", SessionID: "stats-main", ProfileEmail: "stats@example.test", UserID: "stats-user", Model: "claude-sonnet", InputTokens: input(100), OutputTokens: input(50)},
		{Ts: now.Add(time.Minute), EventName: "api_request", SessionID: "stats-worktree", ProfileEmail: "stats@example.test", UserID: "stats-user", Model: "claude-sonnet", InputTokens: input(100), OutputTokens: input(50)},
		{Ts: now.Add(2 * time.Minute), EventName: "api_request", SessionID: "stats-subpath", ProfileEmail: "stats@example.test", UserID: "stats-user", Model: "claude-sonnet", InputTokens: input(100), OutputTokens: input(50)},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	since, until := now.Add(-time.Minute), now.Add(10*time.Minute)
	filter := EventFilter{
		Since:        &since,
		Until:        &until,
		ProfileEmail: "stats@example.test",
		ProjectHash:  "h-subpath", // A non-empty member list must take precedence.
		ProjectHashes: []string{
			"h-main",
			"h-worktree",
		},
	}

	daily, err := s.TimeSeriesStats(ctx, filter, "day")
	if err != nil {
		t.Fatalf("TimeSeriesStats: %v", err)
	}
	assertDailyStatsTotals(t, daily, 2, 200, 100)

	byModel, err := s.TimeSeriesStatsByModel(ctx, filter, "day")
	if err != nil {
		t.Fatalf("TimeSeriesStatsByModel: %v", err)
	}
	if len(byModel) != 1 || byModel[0].EventCount != 2 || byModel[0].InputTokens != 200 || byModel[0].OutputTokens != 100 {
		t.Fatalf("TimeSeriesStatsByModel = %+v, want one two-event sibling row", byModel)
	}

	byUser, err := s.TimeSeriesStatsByUser(ctx, filter, "day")
	if err != nil {
		t.Fatalf("TimeSeriesStatsByUser: %v", err)
	}
	if len(byUser) != 1 || byUser[0].EventCount != 2 || byUser[0].InputTokens != 200 || byUser[0].OutputTokens != 100 {
		t.Fatalf("TimeSeriesStatsByUser = %+v, want one two-event sibling row", byUser)
	}

	latest, ok, err := s.LatestActivityTs(ctx, filter)
	if err != nil {
		t.Fatalf("LatestActivityTs: %v", err)
	}
	if !ok || !latest.Equal(now.Add(time.Minute)) {
		t.Fatalf("LatestActivityTs = (%s, %v), want (%s, true)", latest, ok, now.Add(time.Minute))
	}
}

func TestPgStore_ProjectIdentityStats_DoesNotCrossAgentBoundaryForSharedSessionID(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	input := func(v int) *int { return &v }

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: now, SessionID: "stats-shared-session", UUID: "stats-shared-claude", Agent: "claude", RecordType: "assistant", ProfileEmail: "stats-agent@example.test", ProjectHash: "h-selected", Raw: []byte(`{"message":{"role":"assistant"}}`)},
		{Ts: now.Add(time.Millisecond), SessionID: "stats-shared-session", UUID: "stats-shared-codex", Agent: "codex", RecordType: "assistant", ProfileEmail: "stats-agent@example.test", ProjectHash: "h-other", Raw: []byte(`{"message":{"role":"assistant"}}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: now, EventName: "api_request", SessionID: "stats-shared-session", Agent: "claude", ProfileEmail: "stats-agent@example.test", Model: "claude-sonnet", InputTokens: input(1), OutputTokens: input(2)},
		{Ts: now.Add(time.Millisecond), EventName: "api_request", SessionID: "stats-shared-session", Agent: "codex", ProfileEmail: "stats-agent@example.test", Model: "gpt-codex", InputTokens: input(10), OutputTokens: input(20)},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	since, until := now.Add(-time.Minute), now.Add(time.Minute)
	filter := EventFilter{
		Since:                &since,
		Until:                &until,
		ProfileEmail:         "stats-agent@example.test",
		Agent:                "codex",
		ProjectHashes:        []string{"h-selected"},
		ProjectHashesPresent: true,
	}

	daily, err := s.TimeSeriesStats(ctx, filter, "day")
	if err != nil {
		t.Fatalf("TimeSeriesStats: %v", err)
	}
	assertDailyStatsTotals(t, daily, 0, 0, 0)

	byModel, err := s.TimeSeriesStatsByModel(ctx, filter, "day")
	if err != nil {
		t.Fatalf("TimeSeriesStatsByModel: %v", err)
	}
	if len(byModel) != 0 {
		t.Fatalf("TimeSeriesStatsByModel = %+v, want no cross-agent rows", byModel)
	}

	byUser, err := s.TimeSeriesStatsByUser(ctx, filter, "day")
	if err != nil {
		t.Fatalf("TimeSeriesStatsByUser: %v", err)
	}
	if len(byUser) != 0 {
		t.Fatalf("TimeSeriesStatsByUser = %+v, want no cross-agent rows", byUser)
	}

	latest, ok, err := s.LatestActivityTs(ctx, filter)
	if err != nil {
		t.Fatalf("LatestActivityTs: %v", err)
	}
	if ok {
		t.Fatalf("LatestActivityTs = (%s, true), want no cross-agent rows", latest)
	}
}

func TestPgStore_ProjectIdentityStats_ScalarProjectHashRemainsCompatible(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	input := func(v int) *int { return &v }

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: now, SessionID: "stats-scalar-main", UUID: "stats-scalar-main", RecordType: "assistant", ProfileEmail: "stats-scalar@example.test", ProjectHash: "h-main", Raw: []byte(`{"message":{"role":"assistant"}}`)},
		{Ts: now.Add(time.Minute), SessionID: "stats-scalar-subpath", UUID: "stats-scalar-subpath", RecordType: "assistant", ProfileEmail: "stats-scalar@example.test", ProjectHash: "h-subpath", Raw: []byte(`{"message":{"role":"assistant"}}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: now, EventName: "api_request", SessionID: "stats-scalar-main", ProfileEmail: "stats-scalar@example.test", InputTokens: input(1), OutputTokens: input(2)},
		{Ts: now.Add(time.Minute), EventName: "api_request", SessionID: "stats-scalar-subpath", ProfileEmail: "stats-scalar@example.test", InputTokens: input(10), OutputTokens: input(20)},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	since, until := now.Add(-time.Minute), now.Add(5*time.Minute)
	filter := EventFilter{Since: &since, Until: &until, ProfileEmail: "stats-scalar@example.test", ProjectHash: "h-main"}

	daily, err := s.TimeSeriesStats(ctx, filter, "day")
	if err != nil {
		t.Fatalf("TimeSeriesStats: %v", err)
	}
	assertDailyStatsTotals(t, daily, 1, 1, 2)

	byModel, err := s.TimeSeriesStatsByModel(ctx, filter, "day")
	if err != nil {
		t.Fatalf("TimeSeriesStatsByModel with scalar project hash: %v", err)
	}
	if len(byModel) != 1 || byModel[0].EventCount != 1 || byModel[0].InputTokens != 1 || byModel[0].OutputTokens != 2 {
		t.Fatalf("TimeSeriesStatsByModel with scalar project hash = %+v, want one event", byModel)
	}

	byUser, err := s.TimeSeriesStatsByUser(ctx, filter, "day")
	if err != nil {
		t.Fatalf("TimeSeriesStatsByUser with scalar project hash: %v", err)
	}
	if len(byUser) != 1 || byUser[0].EventCount != 1 || byUser[0].InputTokens != 1 || byUser[0].OutputTokens != 2 {
		t.Fatalf("TimeSeriesStatsByUser with scalar project hash = %+v, want one event", byUser)
	}

	latest, ok, err := s.LatestActivityTs(ctx, filter)
	if err != nil {
		t.Fatalf("LatestActivityTs: %v", err)
	}
	if !ok || !latest.Equal(now) {
		t.Fatalf("LatestActivityTs = (%s, %v), want (%s, true)", latest, ok, now)
	}

	filter.ProjectHashes = []string{}
	filter.ProjectHashesPresent = true
	daily, err = s.TimeSeriesStats(ctx, filter, "day")
	if err != nil {
		t.Fatalf("TimeSeriesStats with explicit empty member list: %v", err)
	}
	assertDailyStatsTotals(t, daily, 0, 0, 0)

	byModel, err = s.TimeSeriesStatsByModel(ctx, filter, "day")
	if err != nil {
		t.Fatalf("TimeSeriesStatsByModel with explicit empty member list: %v", err)
	}
	if len(byModel) != 0 {
		t.Fatalf("TimeSeriesStatsByModel with explicit empty member list = %+v, want no rows", byModel)
	}

	byUser, err = s.TimeSeriesStatsByUser(ctx, filter, "day")
	if err != nil {
		t.Fatalf("TimeSeriesStatsByUser with explicit empty member list: %v", err)
	}
	if len(byUser) != 0 {
		t.Fatalf("TimeSeriesStatsByUser with explicit empty member list = %+v, want no rows", byUser)
	}

	latest, ok, err = s.LatestActivityTs(ctx, filter)
	if err != nil {
		t.Fatalf("LatestActivityTs with explicit empty member list: %v", err)
	}
	if ok {
		t.Fatalf("LatestActivityTs with explicit empty member list = (%s, true), want no rows", latest)
	}
}

func assertDailyStatsTotals(t *testing.T, rows []*DailyStat, events, input, output int64) {
	t.Helper()
	var gotEvents, gotInput, gotOutput int64
	for _, row := range rows {
		gotEvents += row.EventCount
		gotInput += row.InputTokens
		gotOutput += row.OutputTokens
	}
	if gotEvents != events || gotInput != input || gotOutput != output {
		t.Fatalf("daily stats totals = events:%d input:%d output:%d, want events:%d input:%d output:%d (rows=%+v)", gotEvents, gotInput, gotOutput, events, input, output, rows)
	}
}
