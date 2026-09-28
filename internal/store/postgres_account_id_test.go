package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// The Codex billing account id is per-record: records collected before the
// account was first observed carry "" on purpose, so the column must keep the
// two apart instead of filling one from the other.
func TestPgStore_InsertSessionRecords_persistsAccountID(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{
			Ts:           ts,
			SessionID:    "account-id-s1",
			RecordType:   "assistant",
			ProfileEmail: "user@example.test",
			Agent:        "codex",
			AccountID:    "acct-a",
			Raw:          json.RawMessage(`{"uuid":"turn-1"}`),
			UUID:         "turn-1",
		},
		{
			Ts:           ts,
			SessionID:    "account-id-s1",
			RecordType:   "assistant",
			ProfileEmail: "user@example.test",
			Agent:        "codex",
			Raw:          json.RawMessage(`{"uuid":"turn-2"}`),
			UUID:         "turn-2",
		},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	rows, err := s.pool.Query(ctx, `SELECT uuid, account_id FROM session_records WHERE session_id = $1 ORDER BY uuid`, "account-id-s1")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var uuid, accountID string
		if err := rows.Scan(&uuid, &accountID); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[uuid] = accountID
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if got["turn-1"] != "acct-a" {
		t.Fatalf("turn-1 account_id = %q, want acct-a", got["turn-1"])
	}
	if got["turn-2"] != "" {
		t.Fatalf("turn-2 account_id = %q, want empty", got["turn-2"])
	}
}
