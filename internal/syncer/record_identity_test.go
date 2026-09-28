package syncer

import (
	"encoding/json"
	"testing"
	"time"

	"cctrace/internal/sessionlog"
)

// A record's storage key is (session_id, ts, record_type, profile_email, uuid).
// Metadata lines carry neither a uuid nor a timestamp, so both of those columns
// used to be filled with whatever the clock said at ingest -- a rescan produced a
// different key for the same line and ON CONFLICT never fired. 15.2% of lines in a
// real sample are shaped this way (#57).
//
// The fix is that the key must come from the line, not from the clock.
func TestMetadataRecordKeyIsDerivedFromContentNotTheClock(t *testing.T) {
	line := []byte(`{"type":"last-prompt","content":"do the thing"}`)
	var r1, r2 sessionlog.Record
	if err := json.Unmarshal(line, &r1); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(line, &r2); err != nil {
		t.Fatal(err)
	}
	r1.SessionID, r2.SessionID = "s1", "s1"
	// The scanner keeps the bytes it parsed; identity is derived from those.
	r1.RawLine, r2.RawLine = line, line

	first := toStoreRecord(&r1, "a@ex.invalid", "u", "hash")
	time.Sleep(2 * time.Millisecond)
	second := toStoreRecord(&r2, "a@ex.invalid", "u", "hash")

	if first == nil || second == nil {
		t.Fatal("record was skipped")
	}
	if first.UUID == "" {
		t.Fatal("no identity was derived; the row still has nothing to dedupe on")
	}
	if first.UUID != second.UUID {
		t.Errorf("same line produced two identities:\n  %q\n  %q", first.UUID, second.UUID)
	}
	if !first.Ts.Equal(second.Ts) {
		t.Errorf("same line produced two timestamps: %v vs %v", first.Ts, second.Ts)
	}
}

// Two different metadata lines in one session must not collapse into one row.
func TestDifferentMetadataLinesGetDifferentIdentities(t *testing.T) {
	mk := func(content string) *sessionlog.Record {
		var r sessionlog.Record
		if err := json.Unmarshal([]byte(`{"type":"last-prompt","content":"`+content+`"}`), &r); err != nil {
			t.Fatal(err)
		}
		r.SessionID = "s1"
		r.RawLine = []byte(`{"type":"last-prompt","content":"` + content + `"}`)
		return &r
	}
	a := toStoreRecord(mk("first"), "a@ex.invalid", "u", "hash")
	b := toStoreRecord(mk("second"), "a@ex.invalid", "u", "hash")
	if a.UUID == b.UUID {
		t.Errorf("two different lines share an identity: %q", a.UUID)
	}
}

// Records that carry their own uuid must keep it: it is the identity Claude Code
// assigned, and rewriting it would break dedup against rows already stored.
func TestRealUUIDIsLeftAlone(t *testing.T) {
	var r sessionlog.Record
	if err := json.Unmarshal([]byte(`{"type":"user","uuid":"real-uuid","timestamp":"2026-08-20T00:00:00Z"}`), &r); err != nil {
		t.Fatal(err)
	}
	r.SessionID = "s1"
	got := toStoreRecord(&r, "a@ex.invalid", "u", "hash")
	if got.UUID != "real-uuid" {
		t.Errorf("UUID = %q, want the one from the log", got.UUID)
	}
}
