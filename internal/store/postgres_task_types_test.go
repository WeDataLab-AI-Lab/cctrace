package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestPgStore_BackfillTaskTypesOnlyLabelsPrompts(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	now := time.Date(2026, 8, 19, 4, 0, 0, 0, time.UTC)
	records := []*SessionRecord{
		{Ts: now, SessionID: "backfill", UUID: "plan", RecordType: "user", ProfileEmail: "user@example.com", Raw: json.RawMessage(`{"message":{"role":"user","content":"구현 계획을 세워줘"}}`)},
		{Ts: now.Add(time.Second), SessionID: "backfill", UUID: "tool", RecordType: "user", ProfileEmail: "user@example.com", Raw: json.RawMessage(`{"message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`)},
	}
	if err := s.InsertSessionRecords(context.Background(), records); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if _, err := s.pool.Exec(context.Background(), `UPDATE session_records SET task_type = '' WHERE session_id = 'backfill'`); err != nil {
		t.Fatalf("reset labels: %v", err)
	}

	updated, err := s.BackfillTaskTypes(context.Background(), 10)
	if err != nil {
		t.Fatalf("BackfillTaskTypes: %v", err)
	}
	if updated != 1 {
		t.Fatalf("updated = %d, want 1", updated)
	}
	var got string
	if err := s.pool.QueryRow(context.Background(), `SELECT task_type FROM session_records WHERE uuid = 'plan'`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != "planning" {
		t.Fatalf("task_type = %q, want planning", got)
	}
}

// A block-array message.content is the common shape for a real Claude Code human
// message, not the exception -- measured on prod, 85% of Claude user rows are
// array-shaped and 2.3% of those carry a genuine text block. A candidate query that
// only accepted a plain string would silently skip this row forever.
func TestPgStore_BackfillTaskTypesAcceptsArrayShapedTextBlocks(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	now := time.Date(2026, 8, 24, 5, 0, 0, 0, time.UTC)
	records := []*SessionRecord{
		{Ts: now, SessionID: "arr", UUID: "block", RecordType: "user", ProfileEmail: "user@example.com", Agent: "claude", Raw: json.RawMessage(`{"message":{"role":"user","content":[{"type":"text","text":"구현 계획을 세워줘"}]}}`)},
	}
	if err := s.InsertSessionRecords(context.Background(), records); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if _, err := s.pool.Exec(context.Background(), `UPDATE session_records SET task_type = '' WHERE session_id = 'arr'`); err != nil {
		t.Fatalf("reset labels: %v", err)
	}

	updated, err := s.BackfillTaskTypes(context.Background(), 10)
	if err != nil {
		t.Fatalf("BackfillTaskTypes: %v", err)
	}
	if updated != 1 {
		t.Fatalf("updated = %d, want 1 (array-shaped text block must be a candidate)", updated)
	}
	var got string
	if err := s.pool.QueryRow(context.Background(), `SELECT task_type FROM session_records WHERE uuid = 'block'`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != "planning" {
		t.Fatalf("task_type = %q, want planning", got)
	}
}

// The backfill writes the whole batch in one UPDATE via parallel id/task_type
// arrays (unnest), not one UPDATE per row. That only works if each id stays
// paired with its OWN classification -- a misalignment bug here would still
// update every row (so a single-candidate test can't catch it) but assign the
// wrong label to some of them. Three candidates with three different labels
// pins the pairing.
func TestPgStore_BackfillTaskTypesPairsEachRowWithItsOwnLabel(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	now := time.Date(2026, 8, 24, 19, 0, 0, 0, time.UTC)
	records := []*SessionRecord{
		{Ts: now, SessionID: "pair", UUID: "p1", RecordType: "user", ProfileEmail: "user@example.com", Raw: json.RawMessage(`{"message":{"role":"user","content":"테스트를 추가해줘"}}`)},
		{Ts: now.Add(time.Second), SessionID: "pair", UUID: "p2", RecordType: "user", ProfileEmail: "user@example.com", Raw: json.RawMessage(`{"message":{"role":"user","content":"문서를 작성해줘"}}`)},
		{Ts: now.Add(2 * time.Second), SessionID: "pair", UUID: "p3", RecordType: "user", ProfileEmail: "user@example.com", Raw: json.RawMessage(`{"message":{"role":"user","content":"구현 계획을 세워줘"}}`)},
	}
	if err := s.InsertSessionRecords(context.Background(), records); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if _, err := s.pool.Exec(context.Background(), `UPDATE session_records SET task_type = '' WHERE session_id = 'pair'`); err != nil {
		t.Fatalf("reset labels: %v", err)
	}

	updated, err := s.BackfillTaskTypes(context.Background(), 10)
	if err != nil {
		t.Fatalf("BackfillTaskTypes: %v", err)
	}
	if updated != 3 {
		t.Fatalf("updated = %d, want 3", updated)
	}
	want := map[string]string{"p1": "testing", "p2": "documentation", "p3": "planning"}
	for uuid, wantType := range want {
		var got string
		if err := s.pool.QueryRow(context.Background(), `SELECT task_type FROM session_records WHERE uuid = $1`, uuid).Scan(&got); err != nil {
			t.Fatalf("query %s: %v", uuid, err)
		}
		if got != wantType {
			t.Errorf("uuid=%s task_type = %q, want %q (id/task_type array pairing must not shift)", uuid, got, wantType)
		}
	}
}

func TestPgStore_IngestClassifiesArrayShapedTextBlockImmediately(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	now := time.Date(2026, 8, 24, 6, 0, 0, 0, time.UTC)
	records := []*SessionRecord{
		{Ts: now, SessionID: "arr2", UUID: "block2", RecordType: "user", ProfileEmail: "user@example.com", Agent: "claude", Raw: json.RawMessage(`{"message":{"role":"user","content":[{"type":"text","text":"테스트를 추가해줘"}]}}`)},
		{Ts: now.Add(time.Second), SessionID: "arr2", UUID: "tool2", RecordType: "user", ProfileEmail: "user@example.com", Agent: "claude", Raw: json.RawMessage(`{"message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`)},
	}
	if err := s.InsertSessionRecords(context.Background(), records); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	var textBlockType, toolResultType string
	if err := s.pool.QueryRow(context.Background(), `SELECT task_type FROM session_records WHERE uuid = 'block2'`).Scan(&textBlockType); err != nil {
		t.Fatalf("query text block: %v", err)
	}
	if textBlockType != "testing" {
		t.Fatalf("array-shaped text block task_type = %q, want testing (classified at ingest)", textBlockType)
	}
	if err := s.pool.QueryRow(context.Background(), `SELECT task_type FROM session_records WHERE uuid = 'tool2'`).Scan(&toolResultType); err != nil {
		t.Fatalf("query tool result: %v", err)
	}
	if toolResultType != "" {
		t.Fatalf("tool_result task_type = %q, want '' (never a classification candidate, not even unknown)", toolResultType)
	}
}
