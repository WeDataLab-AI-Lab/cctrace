package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestCodexTurnMetadataInsertConflictReenrichAndLoad(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 10, 9, 6, 20, 1, 345_000_000, time.UTC)
	makeRecord := func(turnID, rootID string) *SessionRecord {
		return &SessionRecord{
			Ts: ts, SessionID: "codex-turn-metadata", RecordType: "user", Agent: "codex",
			ProfileEmail: "p", UserID: "u", UUID: "row-1",
			Raw:         json.RawMessage(`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`),
			CodexTurnID: turnID, CodexRootTurnID: rootID, CodexTurnMetadataKnown: true,
		}
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{makeRecord("turn-a", "root-a")}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{makeRecord("turn-b", "root-b")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReenrichSessionRecords(ctx, []*SessionRecord{makeRecord("turn-c", "root-c")}); err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, "codex-turn-metadata", "codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(load.Input.CodexRows) != 1 {
		t.Fatalf("Codex rows=%d", len(load.Input.CodexRows))
	}
	row := load.Input.CodexRows[0]
	if row.CodexTurnID != "turn-c" || row.CodexRootTurnID != "root-c" || !row.CodexTurnMetadataKnown {
		t.Fatalf("loaded turn metadata=%+v", row)
	}
}

func TestCodexTurnMetadataWithNULStaysKnownButUnattributed(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
		Ts: time.Now().UTC(), SessionID: "codex-turn-metadata-nul", RecordType: "user", Agent: "codex",
		ProfileEmail: "p", UserID: "u", UUID: "row-1", Raw: json.RawMessage(`{"type":"response_item"}`),
		CodexTurnID: "child\x00invalid", CodexRootTurnID: "root\x00invalid", CodexTurnMetadataKnown: true,
	}}); err != nil {
		t.Fatal(err)
	}
	var turnID, rootID string
	var known bool
	if err := s.pool.QueryRow(ctx, `SELECT codex_turn_id,codex_root_turn_id,codex_turn_metadata_known
		FROM session_records WHERE session_id='codex-turn-metadata-nul'`).Scan(&turnID, &rootID, &known); err != nil {
		t.Fatal(err)
	}
	if turnID != "" || rootID != "" || !known {
		t.Fatalf("stored invalid lifecycle identity: turn=%q root=%q known=%v", turnID, rootID, known)
	}
}
