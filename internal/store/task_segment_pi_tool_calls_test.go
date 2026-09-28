package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// piToolResultRaw is the toolResult line gjc and omo both write, as
// internal/gjclog and internal/omolog parse it: the outcome sits in
// message.isError.
func piToolResultRaw(tool string, isError bool) json.RawMessage {
	flag := "false"
	if isError {
		flag = "true"
	}
	return json.RawMessage(`{"type":"message","message":{"role":"toolResult","toolName":"` + tool +
		`","toolCallId":"call-1","isError":` + flag + `,"content":[{"type":"text","text":"out"}]}}`)
}

// piToolCallSession seeds one gjc or omo segment: a typed turn, three tool
// results (one failed) and, for gjc, the sibling tool_call rows gjc also writes
// for the same calls. omo writes none, which is why tool_result is the basis.
func piToolCallSession(session, agent string, base time.Time) []*SessionRecord {
	rec := func(offset time.Duration, recordType, uuid, tool string, raw json.RawMessage) *SessionRecord {
		return &SessionRecord{Ts: base.Add(offset), SessionID: session, RecordType: recordType, Agent: agent,
			ProjectHash: "proj-a", ProfileEmail: "u@example.com", UserID: "uid-1", UUID: uuid, ToolName: tool, Raw: raw}
	}
	out := []*SessionRecord{
		rec(0, "user", session+"-u1", "", json.RawMessage(`{"type":"message","message":{"role":"user","content":"fix it"}}`)),
		rec(10*time.Second, "tool_result", session+"-r1", "bash", piToolResultRaw("bash", false)),
		rec(20*time.Second, "tool_result", session+"-r2", "edit", piToolResultRaw("edit", false)),
		rec(30*time.Second, "tool_result", session+"-r3", "bash", piToolResultRaw("bash", true)),
	}
	if agent == "gjc" {
		out = append(out,
			rec(9*time.Second, "tool_call", session+"-c1", "bash", gjcToolCallRaw("call-1", "bash", false)),
			rec(19*time.Second, "tool_call", session+"-c2", "edit", gjcToolCallRaw("call-2", "edit", false)),
			// isError on a tool_call row is a trap for the record_type filter: if
			// the count ever read tool_call rows, gjc would report 2 failures.
			rec(29*time.Second, "tool_call", session+"-c3", "bash", gjcToolCallRaw("call-3", "bash", true)),
		)
	}
	return out
}

// gjcToolCallRaw is the raw of a gjc tool_call record: gjclog stores the whole
// assistant line the toolCall block came from (gjclog/record.go, Raw: line).
// Real assistant lines carry no isError; the flag exists only to test the filter.
func gjcToolCallRaw(id, tool string, isError bool) json.RawMessage {
	flag := ""
	if isError {
		flag = `,"isError":true`
	}
	return json.RawMessage(`{"id":"m-` + id + `","parentId":null,"timestamp":"2026-09-14T09:00:00.000Z","type":"message",` +
		`"message":{"role":"assistant","model":"claude-opus-5"` + flag + `,"content":[{"type":"text","text":"running"},` +
		`{"type":"toolCall","id":"` + id + `","name":"` + tool + `","arguments":{}}]}}`)
}

// gjc and omo export no OTEL, so a segment built from otel_events counted zero
// calls for them while the session view listed each one. Their calls and
// outcomes are both in session_records: every tool_result row is one call, and
// message.isError says how it ended.
func TestPiSegmentsCountToolResultsFromJSONL(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	enableSegmentFacts(t, s)

	base := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	records := append(piToolCallSession("pi-gjc", "gjc", base), piToolCallSession("pi-omo", "omo", base)...)
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}

	for _, session := range []string{"pi-gjc", "pi-omo"} {
		calls, fails := segmentToolCount(t, s, session)
		if calls != 3 {
			t.Errorf("%s tool_call_count = %d, want 3 tool_result rows (gjc's sibling tool_call rows are the same calls)", session, calls)
		}
		if fails != 1 {
			t.Errorf("%s tool_fail_count = %d, want 1 from isError", session, fails)
		}
		// The labeler's other rules need write/read/Bash splits, which these
		// harnesses' tool names do not map onto, so feeding only failures in would
		// make 'diagnose' the sole reachable label. The label stays unknown until
		// the splits are defined; see the CASE in taskSegmentFactsInsertSQL.
		var activity string
		if err := s.pool.QueryRow(ctx, `SELECT activity_type FROM task_segment_facts WHERE session_id = $1`,
			session).Scan(&activity); err != nil {
			t.Fatal(err)
		}
		if activity != "unknown" {
			t.Errorf("%s activity_type = %q, want unknown", session, activity)
		}
	}
}

// Calls from an excluded account stay out of the count, as they do for Codex.
func TestPiSegmentToolCallsHonourAccountExclusion(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	enableSegmentFacts(t, s)
	if _, err := s.pool.Exec(ctx, `INSERT INTO excluded_accounts (login_email) VALUES ('hidden@example.com')`); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	records := piToolCallSession("pi-excluded", "omo", base)
	records[3].LoginEmail = "hidden@example.com" // the failed result
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}

	calls, fails := segmentToolCount(t, s, "pi-excluded")
	if calls != 2 || fails != 0 {
		t.Errorf("counts = %d calls / %d fails, want 2/0 -- the excluded account's call was counted", calls, fails)
	}
}

// gjc and omo record every call's outcome, so their counts are a measurement on
// both halves, unlike Codex.
func TestPiSegmentsReportObservedOutcomeEvidence(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	enableSegmentFacts(t, s)

	base := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	records := append(piToolCallSession("ev-gjc", "gjc", base), piToolCallSession("ev-omo", "omo", base)...)
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}

	segs, err := s.AIWeekSegments(ctx, AISegmentScope{UserID: "uid-1", ProfileEmail: "u@example.com",
		Since: base.Add(-time.Hour), Until: base.Add(time.Hour)}, AISegmentFilter{})
	if err != nil {
		t.Fatalf("AIWeekSegments: %v", err)
	}
	got := map[string][2]bool{}
	for _, seg := range segs {
		got[seg.SessionID] = [2]bool{seg.ToolEvidence, seg.ToolOutcomeEvidence}
	}
	for _, session := range []string{"ev-gjc", "ev-omo"} {
		if g, ok := got[session]; !ok || g != [2]bool{true, true} {
			t.Errorf("%s evidence = %v (present %v), want both observed", session, g, ok)
		}
	}
}

// compare_week sums calls over every agent but failures only over the agents
// that record an outcome, so a failure rate taken over tool_call_count reads low
// by whatever share Codex holds. The aggregate carries the matching denominator:
// the calls whose outcomes were observed.
func TestAIWeekAggregateReportsOutcomeObservedCalls(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	enableSegmentFacts(t, s)

	base := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	// The events land first: a fact counts the OTEL that exists when the session
	// record that builds it is written.
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: base.Add(time.Second), EventName: "tool_result", SessionID: "agg-claude", ProfileEmail: "u@example.com",
			UserID: "uid-1", Agent: "claude", ToolName: "Bash", ToolSuccess: ptrBool(true)},
		{Ts: base.Add(2 * time.Second), EventName: "tool_result", SessionID: "agg-claude", ProfileEmail: "u@example.com",
			UserID: "uid-1", Agent: "claude", ToolName: "Bash", ToolSuccess: ptrBool(false)},
	}); err != nil {
		t.Fatal(err)
	}
	records := append(codexToolCallSession("agg-codex", base), piToolCallSession("agg-omo", "omo", base)...)
	records = append(records, &SessionRecord{Ts: base, SessionID: "agg-claude", RecordType: "user", Agent: "claude",
		ProjectHash: "proj-a", ProfileEmail: "u@example.com", UserID: "uid-1", UUID: "agg-claude-1", Raw: str("go")})
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}

	agg, err := s.AIWeekAggregate(ctx, AISegmentScope{UserID: "uid-1", ProfileEmail: "u@example.com",
		Since: base.Add(-time.Hour), Until: base.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	// 3 Codex + 3 omo + 2 Claude calls; only the last two agents' outcomes exist.
	if agg.ToolCallCount != 8 {
		t.Errorf("tool_call_count = %d, want 8", agg.ToolCallCount)
	}
	if agg.ToolOutcomeObservedCount != 5 {
		t.Errorf("tool_outcome_observed_count = %d, want 5 (Codex's 3 calls have no outcome)", agg.ToolOutcomeObservedCount)
	}
	if agg.ToolFailCount != 2 {
		t.Errorf("tool_fail_count = %d, want 2", agg.ToolFailCount)
	}
}

// Facts built before this count still say 0 calls for every gjc and omo segment,
// and a finished session never writes again to refresh them. A one-time pass
// recomputes those two agents only, in batches, for the same reason the Codex
// pass is batched: a full rebuild holds the exclusive fact lock every record
// write waits on.
func TestBackfillPiSegmentToolCountsRecomputesOnlyPiFacts(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	enableSegmentFacts(t, s)

	base := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	records := append(piToolCallSession("bf-gjc", "gjc", base), piToolCallSession("bf-omo", "omo", base)...)
	records = append(records, &SessionRecord{Ts: base, SessionID: "bf-claude", RecordType: "user", Agent: "claude",
		ProjectHash: "proj-a", ProfileEmail: "u@example.com", UUID: "bf-claude-1", Raw: str("go")})
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	// What the facts held before this change. The Claude value is a sentinel: the
	// pass must not recompute Claude sessions at all.
	if _, err := s.pool.Exec(ctx, `UPDATE task_segment_facts SET tool_call_count =
		CASE WHEN agent IN ('gjc','omo') THEN 0 ELSE 99 END, tool_fail_count = 0
		WHERE session_id IN ('bf-gjc','bf-omo','bf-claude')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, piSegmentToolCountsBackfill); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE pi_segment_tool_counts_cursor SET last_session_id = ''`); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		if err := s.BackfillPiSegmentToolCounts(ctx); err != nil {
			t.Fatalf("pass %d: %v", i+1, err)
		}
	}

	for _, session := range []string{"bf-gjc", "bf-omo"} {
		if calls, fails := segmentToolCount(t, s, session); calls != 3 || fails != 1 {
			t.Errorf("%s = %d calls / %d fails after backfill, want 3/1", session, calls, fails)
		}
	}
	if calls, _ := segmentToolCount(t, s, "bf-claude"); calls != 99 {
		t.Errorf("claude tool_call_count = %d, want the untouched 99", calls)
	}
	var done bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_backfills WHERE name = $1)`,
		piSegmentToolCountsBackfill).Scan(&done); err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Error("marker not written after the pass finished")
	}
}
