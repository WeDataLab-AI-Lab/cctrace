package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"cctrace/internal/sessionfacts"
)

// Codex writes <environment_context> as a user-role line. It is not a request:
// the newest window must not open on it, and the conversation timeline must not
// count it. A <skill> call starts with "<" too but is typed by a person.
func TestCodexInjectedContextIsNotARequest(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 10, 16, 4, 39, 0, 0, time.UTC)
	msg := func(text string) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}}})
		return raw
	}
	rows := []*SessionRecord{
		{Ts: ts, SessionID: "cx", UUID: "typed", RecordType: "user", Agent: "codex", UserID: "owner", Raw: msg("refactor the loader")},
		{Ts: ts.Add(time.Second), SessionID: "cx", UUID: "skill", RecordType: "user", Agent: "codex", UserID: "owner", Raw: msg("<skill>\n<name>deploy</name>\n</skill>")},
		{Ts: ts.Add(2 * time.Second), SessionID: "cx", UUID: "context", RecordType: "user", Agent: "codex", UserID: "owner", Raw: msg("<environment_context>\n  <current_date>2026-10-16</current_date>\n</environment_context>")},
	}
	if err := s.InsertSessionRecords(ctx, rows); err != nil {
		t.Fatal(err)
	}
	offset := 0
	got, err := s.ListSessionRecords(ctx, SessionRecordFilter{SessionID: "cx", UserID: "owner", Order: "asc", Start: "latest-request", StartOffset: &offset, Limit: 10})
	if err != nil || len(got) == 0 || got[0].UUID != "skill" {
		t.Fatalf("latest request window starts at %v (err=%v), want the <skill> call", got, err)
	}
	timeline, err := s.ConversationTimeline(ctx, SessionRecordFilter{SessionID: "cx", UserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if len(timeline.Requests) != 2 || !timeline.Requests[0].Equal(ts) || !timeline.Requests[1].Equal(ts.Add(time.Second)) {
		t.Fatalf("timeline requests=%v, want the typed message and the skill call", timeline.Requests)
	}
}

// Stored Codex request numbers shared one counter, so a session's turns can all
// hold its total. The index numbers them by order and says when each opened.
func TestSessionTraceIndexNumbersRequestsByOrder(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "index-renumber"
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot) VALUES($1,'owner','owner@example.com','codex',now(),'{}'::jsonb)`, id); err != nil {
		t.Fatal(err)
	}
	// The index is read only for a session the caller can see.
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{Ts: time.Date(2026, 10, 12, 3, 0, 0, 0, time.UTC), SessionID: id, UUID: "seen", RecordType: "assistant", Agent: "codex", UserID: "owner", Raw: json.RawMessage(`{}`)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES($1) ON CONFLICT DO NOTHING`, id); err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, id, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if outcome, err := publishSessionP4ForTest(ctx, s, id, load, 1); err != nil || outcome != "written" {
		t.Fatalf("publish=%s, %v", outcome, err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM session_turn_facts WHERE session_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 12, 4, 0, 0, 0, time.UTC)
	for i, key := range []string{"turn-c", "turn-a", "turn-b"} {
		ordinal := map[string]int{"turn-a": 1, "turn-b": 2, "turn-c": 3}[key]
		started := base.Add(time.Duration(ordinal) * time.Minute)
		detail, _ := json.Marshal(map[string]any{"inputs": []any{map[string]any{"uuid": "", "ts": started.Add(-8 * time.Second), "kind": "typed", "n": ordinal}}})
		_, err = s.pool.Exec(ctx, `INSERT INTO session_turn_facts(session_id,turn_key,agent,ordinal,request_no,branch,started_at,ended_at,input_kind,is_request,status,preview,preview_state,cost_source,context_source,rules_version,detail)
 VALUES($1,$2,'codex',$3,3,'active',$4,$4,'typed',true,'ended',$2,'ok','none','unknown',$5,$6::jsonb)`, id, key, ordinal, started, sessionfacts.RulesVersion, string(detail))
		if err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	index, err := s.SessionTraceIndex(ctx, id, SessionAnalysisAccess{UserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if index.State != "ready" || len(index.Requests) != 3 {
		t.Fatalf("index state=%s requests=%+v", index.State, index.Requests)
	}
	for i, want := range []string{"turn-a", "turn-b", "turn-c"} {
		e := index.Requests[i]
		opened := base.Add(time.Duration(i+1)*time.Minute - 8*time.Second)
		if e.TurnKey != want || e.RequestNo != i+1 || !e.OpenedAt.Equal(opened) {
			t.Fatalf("request %d = %+v, want %s numbered %d opened at %v", i, e, want, i+1, opened)
		}
	}
}
