package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Mirrors TestPluginInvocationFactsBackfillMaterializesCommands: the failure mode
// this guards against is not a missing word but a re-inlined CTE. If `bounded`'s
// end_ts computation (LEAD over the small per-segment set) were ever replaced by a
// per-row correlated lookup against the full session_records table, Postgres would
// push it into a join filter as a SubPlan re-evaluated per candidate row pair --
// the exact shape that cost plugin_invocation_facts 12+ minutes inlined.
func TestTaskSegmentFactsBackfillMaterializesTurns(t *testing.T) {
	s := acquireTestStore(t)
	rows, err := s.pool.Query(context.Background(), "EXPLAIN "+taskSegmentFactsInsertSQL, nil, "behavior-v1")
	if err != nil {
		t.Fatalf("explain backfill: %v", err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan = append(plan, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read plan: %v", err)
	}
	joined := strings.Join(plan, "\n")

	if !strings.Contains(joined, "CTE Scan on turns") {
		t.Fatalf("turns CTE was inlined -- boundary detection will be re-evaluated per join row:\n%s", joined)
	}
	for _, line := range plan {
		if strings.Contains(line, "Join Filter:") && strings.Contains(line, "SubPlan") {
			t.Fatalf("join filter re-evaluates a subquery per row pair:\n%s", line)
		}
	}
}

func str(v string) json.RawMessage { return json.RawMessage(`{"message":{"content":"` + v + `"}}`) }

func TestTaskSegmentFacts_BoundaryOnIdleGapAndProjectSwitch(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, taskSegmentFactsBackfill); err != nil {
		t.Fatalf("clear fact marker: %v", err)
	}
	if err := s.BackfillTaskSegmentFacts(ctx); err != nil {
		t.Fatalf("complete empty backfill: %v", err)
	}

	base := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	records := []*SessionRecord{
		// Segment 1: two turns two minutes apart, same project -- one segment.
		{Ts: base, SessionID: "s1", RecordType: "user", Agent: "claude", ProjectHash: "proj-a", ProfileEmail: "u@example.com", UUID: "t1", Raw: str("start the task")},
		{Ts: base.Add(2 * time.Minute), SessionID: "s1", RecordType: "user", Agent: "claude", ProjectHash: "proj-a", ProfileEmail: "u@example.com", UUID: "t2", Raw: str("ok")},
		// Segment 2: 31 minutes after t2 -- idle gap opens a new segment, same project.
		{Ts: base.Add(33 * time.Minute), SessionID: "s1", RecordType: "user", Agent: "claude", ProjectHash: "proj-a", ProfileEmail: "u@example.com", UUID: "t3", Raw: str("back again")},
		// Segment 3: immediately after t3 (no idle gap) but a different project.
		{Ts: base.Add(34 * time.Minute), SessionID: "s1", RecordType: "user", Agent: "claude", ProjectHash: "proj-b", ProfileEmail: "u@example.com", UUID: "t4", Raw: str("switch project")},
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatalf("insert: %v", err)
	}

	rows, err := s.pool.Query(ctx, `SELECT typed_turn_count, project_hash FROM task_segment_facts
		WHERE session_id = 's1' ORDER BY start_ts`)
	if err != nil {
		t.Fatalf("query facts: %v", err)
	}
	defer rows.Close()
	type seg struct {
		typedTurns  int
		projectHash string
	}
	var got []seg
	for rows.Next() {
		var g seg
		if err := rows.Scan(&g.typedTurns, &g.projectHash); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, g)
	}
	want := []seg{{2, "proj-a"}, {1, "proj-a"}, {1, "proj-b"}}
	if len(got) != len(want) {
		t.Fatalf("segments = %+v, want %+v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("segment %d = %+v, want %+v", i, got[i], w)
		}
	}
}

func TestTaskSegmentFacts_AggregatesToolsCommandsAndTokens(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, taskSegmentFactsBackfill); err != nil {
		t.Fatalf("clear fact marker: %v", err)
	}

	base := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)

	// Tool events are OTEL, delivered on a separate ingest path from session_records
	// sync, and the fact table is only recomputed on session_records writes (the
	// same trigger plugin_invocation_facts uses). So the events must already exist
	// before the session_records write that triggers the recompute -- in production
	// this is the ordinary case (OTEL push arrives faster than periodic JSONL sync),
	// and a session_records write after the fact self-corrects any lag.
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: base.Add(time.Second), SessionID: "s2", EventName: "tool_result", ProfileEmail: "u@example.com", ToolName: "Bash", ToolSuccess: ptrBool(true)},
		{Ts: base.Add(time.Second + 500*time.Millisecond), SessionID: "s2", EventName: "tool_result", ProfileEmail: "u@example.com", ToolName: "Bash", ToolSuccess: ptrBool(false)},
	}); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	records := []*SessionRecord{
		{Ts: base, SessionID: "s2", RecordType: "user", Agent: "claude", ProjectHash: "proj-a", ProfileEmail: "u@example.com", CommandName: "deploy", CommandSource: "plugin", UUID: "u1", Raw: str("deploy it")},
		{Ts: base.Add(time.Second), SessionID: "s2", RecordType: "assistant", Agent: "claude", UUID: "a1", InputTokens: ptrInt(50), OutputTokens: ptrInt(20)},
		{Ts: base.Add(2 * time.Second), SessionID: "s2", RecordType: "assistant", Agent: "claude", UUID: "a2", InputTokens: ptrInt(30), OutputTokens: ptrInt(10)},
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var toolCalls, toolFails, commandCount, inputTokens, outputTokens int
	err := s.pool.QueryRow(ctx, `SELECT tool_call_count, tool_fail_count, command_count, input_tokens, output_tokens
		FROM task_segment_facts WHERE session_id = 's2'`).
		Scan(&toolCalls, &toolFails, &commandCount, &inputTokens, &outputTokens)
	if err != nil {
		t.Fatalf("query fact: %v", err)
	}
	if toolCalls != 2 || toolFails != 1 {
		t.Fatalf("tools = calls=%d fails=%d, want calls=2 fails=1", toolCalls, toolFails)
	}
	if commandCount != 1 {
		t.Fatalf("command_count = %d, want 1", commandCount)
	}
	if inputTokens != 80 || outputTokens != 30 {
		t.Fatalf("tokens = in=%d out=%d, want in=80 out=30", inputTokens, outputTokens)
	}
}

// End-to-end wiring check for the activity_type CASE expression in
// taskSegmentFactsInsertSQL -- internal/activitylabel's own tests pin the rule
// table itself, this pins that the SQL mirrors it against real tool events.
func TestTaskSegmentFacts_ActivityTypeReflectsToolEvidence(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Date(2026, 8, 24, 18, 0, 0, 0, time.UTC)

	// s-ops: three Bash calls, no writes, no failures -- near-pure shell.
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: base.Add(time.Second), SessionID: "s-ops", EventName: "tool_result", ProfileEmail: "u@example.com", ToolName: "Bash", ToolSuccess: ptrBool(true)},
		{Ts: base.Add(2 * time.Second), SessionID: "s-ops", EventName: "tool_result", ProfileEmail: "u@example.com", ToolName: "Bash", ToolSuccess: ptrBool(true)},
		{Ts: base.Add(3 * time.Second), SessionID: "s-ops", EventName: "tool_result", ProfileEmail: "u@example.com", ToolName: "Bash", ToolSuccess: ptrBool(true)},
	}); err != nil {
		t.Fatalf("insert ops events: %v", err)
	}
	// s-author: one Edit call, no failures.
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: base.Add(time.Second), SessionID: "s-author", EventName: "tool_result", ProfileEmail: "u@example.com", ToolName: "Edit", ToolSuccess: ptrBool(true)},
	}); err != nil {
		t.Fatalf("insert author events: %v", err)
	}
	// s-explore: Read calls only, no writes.
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: base.Add(time.Second), SessionID: "s-explore", EventName: "tool_result", ProfileEmail: "u@example.com", ToolName: "Read", ToolSuccess: ptrBool(true)},
	}); err != nil {
		t.Fatalf("insert explore events: %v", err)
	}

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: base, SessionID: "s-ops", RecordType: "user", Agent: "claude", ProjectHash: "proj-a", ProfileEmail: "u@example.com", UUID: "ops-t1", Raw: str("go")},
		{Ts: base, SessionID: "s-author", RecordType: "user", Agent: "claude", ProjectHash: "proj-a", ProfileEmail: "u@example.com", UUID: "author-t1", Raw: str("go")},
		{Ts: base, SessionID: "s-explore", RecordType: "user", Agent: "claude", ProjectHash: "proj-a", ProfileEmail: "u@example.com", UUID: "explore-t1", Raw: str("go")},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	want := map[string]string{"s-ops": "ops", "s-author": "author", "s-explore": "explore"}
	for sessionID, wantType := range want {
		var gotType, gotVersion string
		if err := s.pool.QueryRow(ctx, `SELECT activity_type, labeler_version FROM task_segment_facts WHERE session_id = $1`, sessionID).
			Scan(&gotType, &gotVersion); err != nil {
			t.Fatalf("%s: query fact: %v", sessionID, err)
		}
		if gotType != wantType {
			t.Errorf("%s: activity_type = %q, want %q", sessionID, gotType, wantType)
		}
		if gotVersion != "behavior-v1" {
			t.Errorf("%s: labeler_version = %q, want behavior-v1", sessionID, gotVersion)
		}
	}
}

// A boundary that arrives after its response rows must reshape existing facts,
// not merely append to them -- sync can deliver records out of order.
func TestTaskSegmentFacts_LateBoundaryReshapesSegments(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	base := time.Date(2026, 8, 24, 11, 0, 0, 0, time.UTC)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: base, SessionID: "s3", RecordType: "user", Agent: "claude", ProjectHash: "proj-a", ProfileEmail: "u@example.com", UUID: "l1", Raw: str("first")},
	}); err != nil {
		t.Fatalf("initial insert: %v", err)
	}
	var before int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM task_segment_facts WHERE session_id = 's3'`).Scan(&before); err != nil {
		t.Fatalf("count before: %v", err)
	}
	if before != 1 {
		t.Fatalf("segments before late insert = %d, want 1", before)
	}

	// Arrives late, but its timestamp is 40 minutes after the first turn -- it must
	// open a second segment once inserted, and re-inserting it must not duplicate it.
	late := []*SessionRecord{
		{Ts: base.Add(40 * time.Minute), SessionID: "s3", RecordType: "user", Agent: "claude", ProjectHash: "proj-a", ProfileEmail: "u@example.com", UUID: "l2", Raw: str("late arrival")},
	}
	if err := s.InsertSessionRecords(ctx, late); err != nil {
		t.Fatalf("late insert: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, late); err != nil {
		t.Fatalf("idempotent insert: %v", err)
	}

	var after int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM task_segment_facts WHERE session_id = 's3'`).Scan(&after); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if after != 2 {
		t.Fatalf("segments after late insert = %d, want 2 (idempotent re-sync must not duplicate)", after)
	}
}

func TestPgStore_MigrateDoesNotWaitForTaskSegmentFactsBackfill(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, taskSegmentFactsBackfill); err != nil {
		t.Fatalf("clear fact marker: %v", err)
	}

	blocker, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin blocker: %v", err)
	}
	defer blocker.Rollback(ctx) //nolint:errcheck
	if err := lockTaskSegmentFacts(ctx, blocker, false); err != nil {
		t.Fatalf("hold fact backfill lock: %v", err)
	}
	migrateCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := s.Migrate(migrateCtx); err != nil {
		t.Fatalf("Migrate waited for task segment fact data repair: %v", err)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatalf("release fact backfill lock: %v", err)
	}
	if err := s.BackfillTaskSegmentFacts(ctx); err != nil {
		t.Fatalf("background fact backfill: %v", err)
	}
}

// Codex has no message.content at all -- its typed text lives under payload.content
// (an array), confirmed on prod (50,518 of 50,518 Codex user rows). Without the
// agent='codex' exception in the turns CTE, every Codex session gets zero segments
// -- this was shipped and deployed to dev before being caught here.
func TestTaskSegmentFacts_CodexTurnsCountDespiteNoMessageContent(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	base := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	codexRaw := json.RawMessage(`{"type":"event_msg","payload":{"type":"user_message","content":[{"type":"input_text","text":"hi"}]}}`)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: base, SessionID: "codex1", RecordType: "user", Agent: "codex", ProjectHash: "proj-a", ProfileEmail: "u@example.com", UUID: "cx1", Raw: codexRaw},
		{Ts: base.Add(time.Minute), SessionID: "codex1", RecordType: "user", Agent: "codex", ProjectHash: "proj-a", ProfileEmail: "u@example.com", UUID: "cx2", Raw: codexRaw},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var typedTurns int
	err := s.pool.QueryRow(ctx, `SELECT typed_turn_count FROM task_segment_facts WHERE session_id = 'codex1'`).Scan(&typedTurns)
	if err != nil {
		t.Fatalf("query fact (no segment created for codex session -- the bug): %v", err)
	}
	if typedTurns != 2 {
		t.Fatalf("typed_turn_count = %d, want 2", typedTurns)
	}
}

// A Claude human turn is not always a plain string -- measured on prod, 8,375 of
// 370,858 array-shaped message.content values carry a real text/input_text block
// (the rest are tool_result). Without the array+text/input_text exception, the
// turns CTE's WHERE clause silently drops these real typed turns, undercounting
// typed_turn_count.
func TestTaskSegmentFacts_ArrayShapedTextBlockCountsAsTypedTurn(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	base := time.Date(2026, 8, 24, 14, 0, 0, 0, time.UTC)
	arrayText := json.RawMessage(`{"message":{"content":[{"type":"text","text":"please continue"}]}}`)
	toolResult := json.RawMessage(`{"message":{"content":[{"type":"tool_result","content":"ok"}]}}`)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: base, SessionID: "arr-seg", RecordType: "user", Agent: "claude", ProjectHash: "proj-a", ProfileEmail: "u@example.com", UUID: "as1", Raw: arrayText},
		{Ts: base.Add(time.Minute), SessionID: "arr-seg", RecordType: "user", Agent: "claude", ProjectHash: "proj-a", ProfileEmail: "u@example.com", UUID: "as2", Raw: toolResult},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var typedTurns int
	err := s.pool.QueryRow(ctx, `SELECT typed_turn_count FROM task_segment_facts WHERE session_id = 'arr-seg'`).Scan(&typedTurns)
	if err != nil {
		t.Fatalf("query fact (no segment created -- array-shaped text block dropped from turns): %v", err)
	}
	if typedTurns != 1 {
		t.Fatalf("typed_turn_count = %d, want 1 (array-shaped text block counts, tool_result does not)", typedTurns)
	}
}
