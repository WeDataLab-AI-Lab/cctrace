package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

func TestSessionInspectionWindowAndSources(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 10, 17, 12, 0, 0, 123000000, time.UTC)
	rows := []*SessionRecord{
		{Ts: ts, SessionID: "inspect", UUID: "first", RecordType: "user", PromptSource: "typed", UserID: "owner", Raw: json.RawMessage(`{"message":{"role":"user","content":"first"}}`)},
		{Ts: ts.Add(time.Second), SessionID: "inspect", UUID: "a", RecordType: "assistant", UserID: "owner", Raw: json.RawMessage(`{"message":{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"Read","input":{"file":"x"}}]}}`)},
		{Ts: ts.Add(2 * time.Second), SessionID: "inspect", UUID: "r", RecordType: "user", UserID: "owner", Raw: json.RawMessage(`{"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"done"}]}}`)},
		{Ts: ts.Add(3 * time.Second), SessionID: "inspect", UUID: "latest", RecordType: "user", PromptSource: "typed", UserID: "owner", Raw: json.RawMessage(`{"message":{"role":"user","content":"latest"}}`)},
		{Ts: ts.Add(4 * time.Second), SessionID: "inspect", UUID: "instructions", RecordType: "attachment", UserID: "owner", Raw: json.RawMessage(`{"attachment":{"type":"instructions","files":[{"path":"AGENTS.md","content":"rules"}]}}`)},
		{Ts: ts.Add(5 * time.Second), SessionID: "inspect", UUID: "hook", RecordType: "attachment", UserID: "owner", Raw: json.RawMessage(`{"attachment":{"type":"hook_success","toolUseID":"call-1","stdout":"hook"}}`)},
	}
	if err := s.InsertSessionRecords(ctx, rows); err != nil {
		t.Fatal(err)
	}
	offset := 0
	got, err := s.ListSessionRecords(ctx, SessionRecordFilter{SessionID: "inspect", UserID: "owner", Order: "asc", Start: "latest-request", StartOffset: &offset, Limit: 100})
	if err != nil || len(got) != 3 || offset != 3 || got[0].UUID != "latest" {
		t.Fatalf("latest window rows=%v offset=%d err=%v", len(got), offset, err)
	}
	got, err = s.ListSessionRecords(ctx, SessionRecordFilter{SessionID: "inspect", UserID: "owner", Order: "asc", Anchor: "r", StartOffset: &offset, Limit: 100})
	if err != nil || len(got) != 4 || offset != 2 || got[0].UUID != "r" {
		t.Fatalf("anchor window rows=%v offset=%d err=%v", len(got), offset, err)
	}

	for _, tc := range []struct {
		anchor, direction, want string
		offset                  int
	}{{"latest", "previous-request", "first", 0}, {"first", "next-request", "latest", 3}, {"a", "next-request", "latest", 3}, {"hook", "previous-request", "first", 0}} {
		got, err = s.ListSessionRecords(ctx, SessionRecordFilter{SessionID: "inspect", UserID: "owner", Order: "asc", Anchor: tc.anchor, Direction: tc.direction, StartOffset: &offset, Limit: 1})
		if err != nil || len(got) != 1 || got[0].UUID != tc.want || offset != tc.offset {
			t.Fatalf("adjacent %s rows=%d offset=%d err=%v", tc.direction, len(got), offset, err)
		}
	}
	got, err = s.ListSessionRecords(ctx, SessionRecordFilter{SessionID: "inspect", UserID: "owner", Order: "asc", Anchor: "first", Direction: "previous-request", StartOffset: &offset, Limit: 1})
	if err != nil || len(got) != 0 || offset != -1 {
		t.Fatalf("previous boundary rows=%d offset=%d err=%v", len(got), offset, err)
	}

	got, err = s.ListSessionRecords(ctx, SessionRecordFilter{SessionID: "inspect", UserID: "owner", ContextOnly: true, Order: "asc"})
	if err != nil || len(got) != 2 {
		t.Fatalf("context rows=%d err=%v", len(got), err)
	}
	got, err = s.ListSessionRecords(ctx, SessionRecordFilter{SessionID: "inspect", UserID: "owner", RelatedToolID: "call-1", Order: "asc"})
	if err != nil || len(got) != 3 {
		t.Fatalf("related exact rows=%d err=%v", len(got), err)
	}
	got, err = s.ListSessionRecords(ctx, SessionRecordFilter{SessionID: "inspect", UserID: "foreign", Anchor: "r", StartOffset: &offset, Limit: 100})
	if err != nil || len(got) != 0 || offset != -1 {
		t.Fatalf("foreign anchor disclosed rows=%d offset=%d err=%v", len(got), offset, err)
	}
	if _, err = s.pool.Exec(ctx, `INSERT INTO session_turn_facts(session_id,turn_key,agent,ordinal,branch,opener_uuid,started_at,ended_at,input_kind,is_request,status,preview_state,cost_source,context_source,rules_version)
 VALUES('inspect','turn-latest','claude',1,'active','latest',$1,$1,'typed',true,'ended','none','none','unknown',5)`, ts.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	got, err = s.ListSessionRecords(ctx, SessionRecordFilter{SessionID: "inspect", UserID: "owner", Order: "asc", TurnKey: "turn-latest", StartOffset: &offset, Limit: 1})
	if err != nil || len(got) != 1 || got[0].UUID != "latest" || offset != 3 {
		t.Fatalf("canonical turn rows=%d offset=%d err=%v", len(got), offset, err)
	}
	got, err = s.ListSessionRecords(ctx, SessionRecordFilter{SessionID: "inspect", UserID: "foreign", Order: "asc", TurnKey: "turn-latest", StartOffset: &offset, Limit: 1})
	if err != nil || len(got) != 0 || offset != -1 {
		t.Fatalf("foreign canonical turn rows=%d offset=%d err=%v", len(got), offset, err)
	}
}

func TestSessionInspectionHashAnchorsAndLineageWindow(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 10, 17, 12, 0, 0, 123456000, time.UTC)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "parent", UUID: "parent-request", UserID: "owner", RecordType: "user", PromptSource: "typed", Raw: json.RawMessage(`{"message":{"role":"user","content":"parent"}}`)},
		{Ts: ts.Add(time.Second), SessionID: "child", UUID: "", UserID: "owner", Agent: "codex", RecordType: "assistant", ForkedFromSession: "parent", Raw: json.RawMessage(`{"payload":{"role":"assistant","content":[{"type":"output_text","text":"legacy"}]}}`)},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListSessionLineage(ctx, SessionRecordFilter{SessionID: "child", UserID: "owner", Order: "asc"})
	if err != nil || len(rows) != 2 {
		t.Fatalf("lineage rows=%d err=%v", len(rows), err)
	}
	legacy := rows[1]
	h := sha256.New()
	for _, part := range []string{legacy.SessionID, legacy.Ts.UTC().Format(time.RFC3339Nano), legacy.RecordType} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	h.Write(legacy.Raw)
	anchor := "h:" + hex.EncodeToString(h.Sum(nil))[:16]
	offset := 0
	found, err := s.ListSessionLineage(ctx, SessionRecordFilter{SessionID: "child", UserID: "owner", Order: "asc", Anchor: anchor, StartOffset: &offset, Limit: 1})
	if err != nil || len(found) != 1 || offset != 1 || found[0].RecordType != "assistant" {
		t.Fatalf("legacy anchor=%s rows=%d offset=%d err=%v", anchor, len(found), offset, err)
	}
	found, err = s.ListSessionLineage(ctx, SessionRecordFilter{SessionID: "child", UserID: "owner", Order: "asc", AnchorTime: &ts, StartOffset: &offset, Limit: 1})
	if err != nil || len(found) != 1 || offset != 0 || found[0].UUID != "parent-request" {
		t.Fatalf("timestamp rows=%d offset=%d err=%v", len(found), offset, err)
	}
}

func TestSessionInspectionLegacyTimestampTiesResolveExactRecord(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "tied", Agent: "codex", RecordType: "assistant", Raw: json.RawMessage(`{"payload":{"role":"assistant","content":"first source"}}`)},
		{Ts: ts, SessionID: "tied", Agent: "codex", RecordType: "user", Raw: json.RawMessage(`{"payload":{"role":"user","content":"second source"}}`)},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListSessionRecords(ctx, SessionRecordFilter{SessionID: "tied", Order: "asc"})
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	for index, row := range rows {
		h := sha256.New()
		for _, part := range []string{row.SessionID, row.Ts.UTC().Format(time.RFC3339Nano), row.RecordType} {
			h.Write([]byte(part))
			h.Write([]byte{0})
		}
		h.Write(row.Raw)
		anchor := "h:" + hex.EncodeToString(h.Sum(nil))[:16]
		offset := 0
		for _, exact := range []bool{false, true} {
			got, err := s.ListSessionRecords(ctx, SessionRecordFilter{SessionID: "tied", Order: "asc", Anchor: anchor, ExactAnchor: exact, StartOffset: &offset, Limit: 1})
			if err != nil || len(got) != 1 || string(got[0].Raw) != string(row.Raw) || (!exact && offset != index) {
				t.Fatalf("exact=%v row=%d got=%v offset=%d err=%v", exact, index, got, offset, err)
			}
		}
	}
}

func TestSessionInspectionContextIncludesTopLevelDeveloperSources(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "developer-context", UUID: "top-role", Agent: "codex", RecordType: "user", Raw: json.RawMessage(`{"type":"message","role":"developer","content":[{"type":"input_text","text":"top-level instructions"}]}`)},
		{Ts: ts.Add(time.Second), SessionID: "developer-context", UUID: "typed-developer", Agent: "codex", RecordType: "developer", Raw: json.RawMessage(`{"type":"message","content":[{"type":"input_text","text":"typed developer instructions"}]}`)},
	}); err != nil {
		t.Fatal(err)
	}
	// Historical rows can still have their old record type before developer backfill.
	if _, err := s.pool.Exec(ctx, `UPDATE session_records SET record_type='user' WHERE session_id='developer-context' AND uuid='top-role'`); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListSessionRecords(ctx, SessionRecordFilter{SessionID: "developer-context", Order: "asc", ContextOnly: true})
	if err != nil || len(rows) != 2 {
		t.Fatalf("developer context rows=%d err=%v", len(rows), err)
	}
}
