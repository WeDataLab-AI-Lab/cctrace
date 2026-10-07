package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func codexToolCallRaw(payloadType, name string) json.RawMessage {
	return json.RawMessage(`{"type":"response_item","payload":{"type":"` + payloadType + `","name":"` + name + `"}}`)
}

// codexToolCallSession seeds one Codex segment: a typed turn and three tool calls
// in the JSONL shapes Codex writes -- function_call and custom_tool_call.
func codexToolCallSession(session string, base time.Time) []*SessionRecord {
	rec := func(offset time.Duration, recordType, uuid, tool string, raw json.RawMessage) *SessionRecord {
		return &SessionRecord{Ts: base.Add(offset), SessionID: session, RecordType: recordType, Agent: "codex",
			ProjectHash: "proj-a", ProfileEmail: "u@example.com", UserID: "uid-1", UUID: uuid, ToolName: tool, Raw: raw}
	}
	return []*SessionRecord{
		rec(0, "user", session+"-u1", "", codexMessageRaw("user", "fix it")),
		rec(10*time.Second, "tool_call", session+"-c1", "exec_command", codexToolCallRaw("function_call", "exec_command")),
		rec(20*time.Second, "tool_call", session+"-c2", "apply_patch", codexToolCallRaw("custom_tool_call", "apply_patch")),
		rec(30*time.Second, "tool_call", session+"-c3", "exec", codexToolCallRaw("custom_tool_call", "exec")),
	}
}

// Codex exports no OTEL log events by design (JSONL-first, adr-codex-support.md),
// so a segment built from otel_events counted zero calls for every Codex session
// while the session view listed each one. The calls are in session_records.
func TestCodexSegmentsCountToolCallsFromJSONL(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	enableSegmentFacts(t, s)

	base := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	if err := s.InsertSessionRecords(ctx, codexToolCallSession("cx-tools", base)); err != nil {
		t.Fatal(err)
	}

	calls, fails := segmentToolCount(t, s, "cx-tools")
	if calls != 3 {
		t.Errorf("tool_call_count = %d, want 3 from the JSONL tool_call rows", calls)
	}
	if fails != 0 {
		t.Errorf("tool_fail_count = %d, want 0 -- Codex JSONL records no outcome", fails)
	}

	// The labeler rules need write/read/Bash splits and failures. Codex records no
	// outcome, and code-mode `exec` wraps apply_patch and exec_command in one call
	// (#698), so neither split is defined: the label stays unknown, as it was.
	var activity string
	if err := s.pool.QueryRow(ctx, `SELECT activity_type FROM task_segment_facts WHERE session_id = 'cx-tools'`).
		Scan(&activity); err != nil {
		t.Fatal(err)
	}
	if activity != "unknown" {
		t.Errorf("activity_type = %q, want unknown", activity)
	}
}

// Calls from an excluded account stay out of the count, the way visible_events
// keeps them out of a Claude segment.
func TestCodexSegmentToolCallsHonourAccountExclusion(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	enableSegmentFacts(t, s)
	if _, err := s.pool.Exec(ctx, `INSERT INTO excluded_accounts (login_email) VALUES ('hidden@example.com')`); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	records := codexToolCallSession("cx-excluded", base)
	records[3].LoginEmail = "hidden@example.com"
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}

	if calls, _ := segmentToolCount(t, s, "cx-excluded"); calls != 2 {
		t.Errorf("tool_call_count = %d, want 2 -- the excluded account's call was counted", calls)
	}
}

// Facts built before the JSONL count still say 0 for every Codex segment, and a
// finished session never writes again to refresh them. A one-time pass recomputes
// Codex sessions only: a full rebuild holds the exclusive fact lock every record
// write waits on (4m41s on a 6.0M-row copy, see codex_developer_records.go).
func TestBackfillCodexSegmentToolCountsRecomputesOnlyCodexFacts(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	enableSegmentFacts(t, s)

	base := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	records := codexToolCallSession("bf-codex", base)
	records = append(records, &SessionRecord{Ts: base, SessionID: "bf-claude", RecordType: "user", Agent: "claude",
		ProjectHash: "proj-a", ProfileEmail: "u@example.com", UUID: "bf-claude-1", Raw: str("go")})
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	// What the facts held before this change. The Claude value is a sentinel: the
	// pass must not recompute Claude sessions at all.
	if _, err := s.pool.Exec(ctx, `UPDATE task_segment_facts SET tool_call_count =
		CASE agent WHEN 'codex' THEN 0 ELSE 99 END WHERE session_id IN ('bf-codex','bf-claude')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, codexSegmentToolCountsBackfill); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE codex_segment_tool_counts_cursor SET last_session_id = ''`); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		if err := s.BackfillCodexSegmentToolCounts(ctx); err != nil {
			t.Fatalf("pass %d: %v", i+1, err)
		}
	}

	if calls, _ := segmentToolCount(t, s, "bf-codex"); calls != 3 {
		t.Errorf("codex tool_call_count = %d after backfill, want 3", calls)
	}
	if calls, _ := segmentToolCount(t, s, "bf-claude"); calls != 99 {
		t.Errorf("claude tool_call_count = %d, want the untouched 99", calls)
	}
	var done bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_backfills WHERE name = $1)`,
		codexSegmentToolCountsBackfill).Scan(&done); err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Error("marker not written after the pass finished")
	}
}

// The two halves of a tool count are measured by different sources. A Codex
// segment's calls come from JSONL, which every Codex segment has, so its count is
// a measurement -- but JSONL records no outcome, so its zero failures are not one.
// A Claude segment's calls and outcomes both come from OTEL and stand or fall
// together (#435).
func TestSegmentsSeparateCallEvidenceFromOutcomeEvidence(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	enableSegmentFacts(t, s)

	base := recentDay(18).Add(9 * time.Hour)
	records := codexToolCallSession("ev-codex", base)
	records = append(records,
		&SessionRecord{Ts: base, SessionID: "ev-claude", RecordType: "user", Agent: "claude",
			ProjectHash: "proj-a", ProfileEmail: "u@example.com", UserID: "uid-1", UUID: "ev-claude-1", Raw: str("go")},
		&SessionRecord{Ts: base, SessionID: "ev-claude-bare", RecordType: "user", Agent: "claude",
			ProjectHash: "proj-a", ProfileEmail: "u@example.com", UserID: "uid-1", UUID: "ev-bare-1", Raw: str("go")},
	)
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: base.Add(time.Second), EventName: "tool_result", SessionID: "ev-claude", ProfileEmail: "u@example.com",
			UserID: "uid-1", Agent: "claude", ToolName: "Bash", ToolSuccess: ptrBool(true)},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_records SET task_type = 'build'
		WHERE session_id IN ('ev-codex','ev-claude','ev-claude-bare')`); err != nil {
		t.Fatal(err)
	}

	type evidence struct{ calls, outcome bool }
	want := map[string]evidence{
		"ev-codex":       {calls: true, outcome: false},
		"ev-claude":      {calls: true, outcome: true},
		"ev-claude-bare": {calls: false, outcome: false},
	}

	page, err := s.TaskSegmentsByType(ctx, base.Add(-time.Hour), base.Add(time.Hour), "build", "u@example.com", "uid-1")
	if err != nil {
		t.Fatalf("TaskSegmentsByType: %v", err)
	}
	gotModal := map[string]evidence{}
	for _, seg := range page.Segments {
		gotModal[seg.SessionID] = evidence{seg.ToolEvidence, seg.ToolOutcomeEvidence}
	}

	segs, err := s.AIWeekSegments(ctx, AISegmentScope{UserID: "uid-1", ProfileEmail: "u@example.com",
		Since: base.Add(-time.Hour), Until: base.Add(time.Hour)}, AISegmentFilter{})
	if err != nil {
		t.Fatalf("AIWeekSegments: %v", err)
	}
	gotAI := map[string]evidence{}
	for _, seg := range segs {
		gotAI[seg.SessionID] = evidence{seg.ToolEvidence, seg.ToolOutcomeEvidence}
	}

	for session, w := range want {
		if g, ok := gotModal[session]; !ok || g != w {
			t.Errorf("modal %s evidence = %+v (present %v), want %+v", session, g, ok, w)
		}
		if g, ok := gotAI[session]; !ok || g != w {
			t.Errorf("AI tools %s evidence = %+v (present %v), want %+v", session, g, ok, w)
		}
	}
}
