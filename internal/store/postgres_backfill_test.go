package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestPgStore_BackfillSessionRecordLoginEmail verifies the otel_events -> session_records
// login_email backfill: it fills empty rows from the session's OTEL account, never
// overwrites existing values, leaves sessions with no otel counterpart blank, refuses
// to touch empty session_id, and is idempotent.
func TestPgStore_BackfillSessionRecordLoginEmail(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)

	// A=single login, C=single login, D intentionally absent from otel.
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: ts, EventName: "api_request", SessionID: "sess-A", LoginEmail: "a@example.com"},
		{Ts: ts, EventName: "api_request", SessionID: "sess-C", LoginEmail: "otel-c@example.com"},
		// Empty session_id: must never form a join group. In a single-user dataset
		// this would otherwise bleed into every session_id='' record.
		{Ts: ts, EventName: "api_request", SessionID: "", LoginEmail: "ghost@example.com"},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	// session_records: A has two empty-login rows, C already filled, D empty w/o otel.
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "sess-A", RecordType: "user", UUID: "a1", Raw: json.RawMessage(`{}`)},
		{Ts: ts, SessionID: "sess-A", RecordType: "assistant", UUID: "a2", Raw: json.RawMessage(`{}`)},
		{Ts: ts, SessionID: "sess-C", RecordType: "user", UUID: "c1", LoginEmail: "existing-c@example.com", Raw: json.RawMessage(`{}`)},
		{Ts: ts, SessionID: "sess-D", RecordType: "user", UUID: "d1", Raw: json.RawMessage(`{}`)},
		{Ts: ts, SessionID: "", RecordType: "user", UUID: "e1", Raw: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	n, err := s.BackfillSessionRecordLoginEmail(ctx, time.Time{})
	if err != nil {
		t.Fatalf("BackfillSessionRecordLoginEmail: %v", err)
	}
	if n != 2 {
		t.Fatalf("rows updated = %d, want 2 (only sess-A's two rows)", n)
	}

	loginEmails := func(sid string) []string {
		rows, err := s.pool.Query(ctx, `SELECT login_email FROM session_records WHERE session_id=$1 ORDER BY uuid`, sid)
		if err != nil {
			t.Fatalf("query %s: %v", sid, err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var e string
			if err := rows.Scan(&e); err != nil {
				t.Fatalf("scan %s: %v", sid, err)
			}
			out = append(out, e)
		}
		return out
	}

	// A: both rows filled with the single otel login.
	for i, e := range loginEmails("sess-A") {
		if e != "a@example.com" {
			t.Fatalf("sess-A row %d login_email=%q, want a@example.com", i, e)
		}
	}
	// C: already filled -> not overwritten.
	if e := loginEmails("sess-C"); e[0] != "existing-c@example.com" {
		t.Fatalf("sess-C login_email=%q, want existing-c@example.com (no overwrite)", e[0])
	}
	// D: no otel counterpart -> stays empty.
	if e := loginEmails("sess-D"); e[0] != "" {
		t.Fatalf("sess-D login_email=%q, want ''", e[0])
	}
	// Empty session_id: must not be joined/filled — otherwise unrelated records
	// with session_id='' get a bogus account bulk-assigned.
	if e := loginEmails(""); e[0] != "" {
		t.Fatalf("empty session_id login_email=%q, want '' (must not be filled)", e[0])
	}

	// Idempotent: a second run changes nothing.
	n2, err := s.BackfillSessionRecordLoginEmail(ctx, time.Time{})
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("second run updated %d rows, want 0 (idempotent)", n2)
	}
}

// TestPgStore_BackfillSessionRecordLoginEmail_manySessions checks that a single
// statement covering many sessions stamps every row with its own session's
// account. The intervals are built per session_id, so a leak here would mean the
// partitioning is wrong rather than a batch boundary being wrong.
func TestPgStore_BackfillSessionRecordLoginEmail_manySessions(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)

	var events []*OtelEvent
	var records []*SessionRecord
	want := map[string]string{"s1": "one@example.com", "s2": "two@example.com", "s3": "three@example.com"}
	for sid, email := range want {
		events = append(events, &OtelEvent{Ts: ts, EventName: "api_request", SessionID: sid, LoginEmail: email})
		records = append(records,
			&SessionRecord{Ts: ts, SessionID: sid, RecordType: "user", UUID: sid + "-u", Raw: json.RawMessage(`{}`)},
			&SessionRecord{Ts: ts, SessionID: sid, RecordType: "assistant", UUID: sid + "-a", Raw: json.RawMessage(`{}`)},
		)
	}
	if err := s.InsertEvents(ctx, events); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	n, err := s.BackfillSessionRecordLoginEmail(ctx, time.Time{})
	if err != nil {
		t.Fatalf("BackfillSessionRecordLoginEmail: %v", err)
	}
	if n != 6 {
		t.Fatalf("rows updated = %d, want 6 (3 sessions x 2 rows)", n)
	}

	for sid, email := range want {
		rows, err := s.pool.Query(ctx, `SELECT login_email FROM session_records WHERE session_id=$1`, sid)
		if err != nil {
			t.Fatalf("query %s: %v", sid, err)
		}
		for rows.Next() {
			var got string
			if err := rows.Scan(&got); err != nil {
				rows.Close()
				t.Fatalf("scan %s: %v", sid, err)
			}
			if got != email {
				rows.Close()
				t.Fatalf("session %s login_email=%q, want %q (cross-session leak)", sid, got, email)
			}
		}
		rows.Close()
	}
}
