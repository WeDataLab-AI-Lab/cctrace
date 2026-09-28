package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// Reenrich fills uuid on rows collected before the uuid enrichment landed. When a
// session was partly re-collected, the same (session_id, ts, record_type, profile_email)
// slot can hold both the legacy uuid=” row and the already-enriched uuid='u1' row.
// Writing u1 onto the legacy row then collides with its enriched twin on
// uniq_session_record_v2 (session_id, ts, record_type, profile_email, uuid) and the whole
// reenrich pass aborts — which is why a Claude reenrich could never finish, and the Codex
// leg that runs after it never started.
func TestPgStore_ReenrichSessionRecords_SkipsLegacyRowWhenEnrichedTwinExists(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 7, 2, 4, 29, 55, 858000000, time.UTC)

	const (
		session = "reenrich-uuid-conflict-s1"
		profile = "reenrich-uuid-conflict-profile"
	)
	base := func(uuid string) *SessionRecord {
		return &SessionRecord{
			Ts:           ts,
			SessionID:    session,
			RecordType:   "user",
			ProfileEmail: profile,
			UserID:       "u1",
			Agent:        "claude",
			UUID:         uuid,
			Raw:          json.RawMessage(`{}`),
		}
	}
	// The legacy row and its enriched twin coexist in the same slot.
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{base(""), base("turn-1")}); err != nil {
		t.Fatal(err)
	}

	// Reenrich replays the file, which carries the uuid.
	rec := base("turn-1")
	rec.SourceFile = "chat.jsonl"
	rec.PromptSource = "typed"
	if _, err := s.ReenrichSessionRecords(ctx, []*SessionRecord{rec}); err != nil {
		t.Fatalf("ReenrichSessionRecords: %v", err)
	}

	rows, err := s.pool.Query(ctx,
		`SELECT uuid, COALESCE(source_file,''), COALESCE(prompt_source,'')
		   FROM session_records WHERE session_id = $1 ORDER BY uuid`, session)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string][2]string{}
	for rows.Next() {
		var uuid, sf, ps string
		if err := rows.Scan(&uuid, &sf, &ps); err != nil {
			t.Fatal(err)
		}
		got[uuid] = [2]string{sf, ps}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 {
		t.Fatalf("rows = %d (%v), want 2 — the legacy row must survive untouched", len(got), got)
	}
	if enriched := got["turn-1"]; enriched != [2]string{"chat.jsonl", "typed"} {
		t.Errorf("enriched row = %v, want source_file/prompt_source filled", enriched)
	}
	if legacy := got[""]; legacy != [2]string{"", ""} {
		t.Errorf("legacy row = %v, want untouched", legacy)
	}
}

// The ordinary case must keep working: a lone legacy row with no enriched twin still
// receives its uuid.
func TestPgStore_ReenrichSessionRecords_FillsUUIDWhenNoTwin(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 7, 2, 5, 0, 0, 0, time.UTC)

	const (
		session = "reenrich-uuid-fill-s1"
		profile = "reenrich-uuid-fill-profile"
	)
	legacy := &SessionRecord{
		Ts: ts, SessionID: session, RecordType: "user", ProfileEmail: profile,
		UserID: "u1", Agent: "claude", Raw: json.RawMessage(`{}`),
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{legacy}); err != nil {
		t.Fatal(err)
	}

	rec := *legacy
	rec.UUID = "turn-9"
	rec.SourceFile = "chat.jsonl"
	if _, err := s.ReenrichSessionRecords(ctx, []*SessionRecord{&rec}); err != nil {
		t.Fatalf("ReenrichSessionRecords: %v", err)
	}

	var uuid, sf string
	if err := s.pool.QueryRow(ctx,
		`SELECT uuid, COALESCE(source_file,'') FROM session_records WHERE session_id = $1`,
		session).Scan(&uuid, &sf); err != nil {
		t.Fatal(err)
	}
	if uuid != "turn-9" || sf != "chat.jsonl" {
		t.Errorf("uuid/source_file = %q/%q, want turn-9/chat.jsonl", uuid, sf)
	}
}
