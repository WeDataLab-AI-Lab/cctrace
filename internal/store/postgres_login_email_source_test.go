package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// clearLoginEmailSourceMarker drops the one-time marker so the repair runs, and
// puts it back afterwards.
//
// schema_backfills survives truncateTables on purpose (it is install state, not
// test data), so a test that consumed the marker and walked away would silently
// disarm this repair for every later test in the package.
func clearLoginEmailSourceMarker(t *testing.T, s *PgStore) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, loginEmailSourceBackfill); err != nil {
		t.Fatalf("clear marker: %v", err)
	}
	t.Cleanup(func() {
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO schema_backfills (name) VALUES ($1) ON CONFLICT DO NOTHING`, loginEmailSourceBackfill); err != nil {
			t.Fatalf("restore marker: %v", err)
		}
	})
}

func loginEmailSourceOf(t *testing.T, s *PgStore, uuid string) (string, string) {
	t.Helper()
	var email, source string
	if err := s.pool.QueryRow(context.Background(),
		`SELECT login_email, login_email_source FROM session_records WHERE uuid = $1`, uuid).
		Scan(&email, &source); err != nil {
		t.Fatalf("query %s: %v", uuid, err)
	}
	return email, source
}

// Rows attributed before login_email_source existed carry no provenance, so an
// empty source would mean both "never attributed" and "attributed by the OTEL
// backfill". The inference pass keys off empty meaning only the first.
//
// Mutation: drop the UPDATE and d1 keeps an empty source, which is the ambiguity
// this repair exists to remove.
func TestBackfillLoginEmailSourceStampsExistingRows(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	clearLoginEmailSourceMarker(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "done", RecordType: "user", UUID: "d1", LoginEmail: "one@example.com", Raw: json.RawMessage(`{}`)},
		{Ts: ts, SessionID: "blank", RecordType: "user", UUID: "b1", Raw: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	if err := s.BackfillLoginEmailSource(ctx); err != nil {
		t.Fatalf("BackfillLoginEmailSource: %v", err)
	}

	if _, src := loginEmailSourceOf(t, s, "d1"); src != "otel" {
		t.Errorf("attributed row source = %q, want otel", src)
	}
	// An unattributed row must stay '': stamping it would claim OTEL observed an
	// account that it never did.
	if email, src := loginEmailSourceOf(t, s, "b1"); email != "" || src != "" {
		t.Errorf("unattributed row = (%q, %q), want ('', '')", email, src)
	}
}

// The repair scans every attributed row on a multi-million row hypertable. The
// marker is what keeps that off every subsequent boot.
//
// Mutation: remove the marker check and the second run stamps late1, which is the
// per-boot full scan this guard exists to prevent.
func TestBackfillLoginEmailSourceRunsOnce(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	clearLoginEmailSourceMarker(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)

	if err := s.BackfillLoginEmailSource(ctx); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "late", RecordType: "user", UUID: "late1", LoginEmail: "one@example.com", Raw: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.BackfillLoginEmailSource(ctx); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if _, src := loginEmailSourceOf(t, s, "late1"); src != "" {
		t.Errorf("second run stamped %q; the marker should have skipped the scan", src)
	}
}

// The session-scoped backfill writes what OTEL observed, so it must say so. A
// value it left unstamped would be indistinguishable from an inferred one, and
// the inference pass would then be free to reconsider a directly observed fact.
//
// Mutation: drop login_email_source from its SET list and r1 comes back with an
// empty source, claiming the strongest attribution the system has is unattributed.
func TestBackfillSessionRecordLoginEmailStampsOtel(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := recentDay(49).Add(10 * time.Hour)

	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: ts, EventName: "api_request", SessionID: "has-otel", LoginEmail: "one@example.com"},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts.Add(time.Minute), SessionID: "has-otel", RecordType: "user", UUID: "r1", Raw: json.RawMessage(`{}`)},
		{Ts: ts, SessionID: "no-otel", RecordType: "user", UUID: "n1", Raw: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	if _, err := s.BackfillSessionRecordLoginEmail(ctx, time.Time{}); err != nil {
		t.Fatalf("BackfillSessionRecordLoginEmail: %v", err)
	}

	if email, src := loginEmailSourceOf(t, s, "r1"); email != "one@example.com" || src != "otel" {
		t.Errorf("filled row = (%q, %q), want (one@example.com, otel)", email, src)
	}
	if _, src := loginEmailSourceOf(t, s, "n1"); src != "" {
		t.Errorf("untouched row source = %q, want ''", src)
	}
}
