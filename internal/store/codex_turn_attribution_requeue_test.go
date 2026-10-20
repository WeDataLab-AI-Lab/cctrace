package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// Only Codex sessions whose messages carry their own turn id are rebuilt, and
// only once.
func TestCodexTurnAttributionRebuildQueuesOnceAndOnlyCodex(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 10, 12, 4, 0, 0, 0, time.UTC)
	withTurn := json.RawMessage(`{"type":"response_item","metadata":{"retained_source":{"id":{"role":"user","turn_id":"turn-1"}}},"payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}`)
	plain := json.RawMessage(`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}`)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "cx-turn", UUID: "a", RecordType: "user", Agent: "codex", UserID: "owner", Raw: withTurn},
		{Ts: ts, SessionID: "cx-plain", UUID: "b", RecordType: "user", Agent: "codex", UserID: "owner", Raw: plain},
		{Ts: ts, SessionID: "claude", UUID: "c", RecordType: "user", Agent: "claude", UserID: "owner", Raw: withTurn},
	}); err != nil {
		t.Fatal(err)
	}
	for _, row := range [][2]string{{"cx-turn", "codex"}, {"cx-plain", "codex"}, {"claude", "claude"}} {
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_analysis_facts(session_id,agent,coverage,transcript,title_source,title_state,turn_boundaries,cost_source,rules_version,output_hash,source_generation)
 VALUES($1,$2,'full',true,'none','none','observed','none',1,'\x00'::bytea,1)`, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM session_fact_dirty`); err != nil {
		t.Fatal(err)
	}
	if err := s.enqueueCodexTurnAttributionRebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countWhere(t, s, "session_fact_dirty", "TRUE"); got != 1 || countWhere(t, s, "session_fact_dirty", "session_id='cx-turn'") != 1 {
		t.Fatalf("queued %d sessions, want only cx-turn", got)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM session_fact_dirty`); err != nil {
		t.Fatal(err)
	}
	if err := s.enqueueCodexTurnAttributionRebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countWhere(t, s, "session_fact_dirty", "TRUE"); got != 0 {
		t.Fatalf("second run queued %d sessions", got)
	}
}
