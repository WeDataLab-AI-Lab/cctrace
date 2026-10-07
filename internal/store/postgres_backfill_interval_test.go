package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestBackfillSplitsMidSessionAccountSwitch is the reason the backfill matches
// intervals instead of collapsing a session to one account.
//
// session_id is stable across /login, so a session can legitimately hold rows
// from two accounts. Filling such a session with a single value (or skipping it)
// is the same mis-attribution #220 exists to remove, just from the other side:
// the token totals stay merged and the account label becomes arbitrary.
func TestBackfillSplitsMidSessionAccountSwitch(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	base := recentDay(32).Add(10 * time.Hour)
	switchAt := base.Add(2 * time.Hour)

	// OTEL is the ground truth: account "one" until switchAt, "two" after.
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: base, EventName: "api_request", SessionID: "sw", LoginEmail: "one@example.com"},
		{Ts: base.Add(time.Hour), EventName: "api_request", SessionID: "sw", LoginEmail: "one@example.com"},
		{Ts: switchAt, EventName: "api_request", SessionID: "sw", LoginEmail: "two@example.com"},
		{Ts: switchAt.Add(time.Hour), EventName: "api_request", SessionID: "sw", LoginEmail: "two@example.com"},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	rec := func(uuid string, ts time.Time) *SessionRecord {
		return &SessionRecord{Ts: ts, SessionID: "sw", RecordType: "user", UUID: uuid, Raw: json.RawMessage(`{}`)}
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		// Before the first OTEL event: belongs to the session, so the earliest
		// known account applies backwards. The reach is bounded by the session,
		// unlike the Codex observation log which spans the whole process life.
		rec("r0", base.Add(-30*time.Minute)),
		rec("r1", base.Add(10*time.Minute)),
		rec("r2", switchAt.Add(-time.Minute)),
		rec("r3", switchAt),
		rec("r4", switchAt.Add(90*time.Minute)),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	if _, err := s.BackfillSessionRecordLoginEmail(ctx, time.Time{}); err != nil {
		t.Fatalf("BackfillSessionRecordLoginEmail: %v", err)
	}

	got := map[string]string{}
	rows, err := s.pool.Query(ctx, `SELECT uuid, login_email FROM session_records WHERE session_id='sw'`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	for rows.Next() {
		var u, e string
		if err := rows.Scan(&u, &e); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		got[u] = e
	}
	rows.Close()

	want := map[string]string{
		"r0": "one@example.com", // pre-OTEL, inside the session
		"r1": "one@example.com",
		"r2": "one@example.com",
		"r3": "two@example.com", // the switch instant belongs to the new account
		"r4": "two@example.com",
	}
	for uuid, w := range want {
		if got[uuid] != w {
			t.Errorf("record %s login_email = %q, want %q", uuid, got[uuid], w)
		}
	}
}

// A bounded pass uses since to discover active sessions, not to truncate their
// timelines. If the pre-boundary switch below were discarded, the first surviving
// two@ observation would reach backwards (rn = 1) and incorrectly stamp a record
// that the complete retained timeline places under one@.
func TestBackfillBoundedPassKeepsPreSinceAccountSwitch(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	since := recentDay(43).Add(10 * time.Hour)
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: since.Add(-2 * time.Hour), EventName: "api_request", SessionID: "bounded-switch", LoginEmail: "one@example.com"},
		{Ts: since.Add(-time.Hour), EventName: "api_request", SessionID: "bounded-switch", LoginEmail: "two@example.com"},
		// This observation makes the session active for the bounded pass.
		{Ts: since.Add(time.Hour), EventName: "api_request", SessionID: "bounded-switch", LoginEmail: "two@example.com"},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
		Ts: since.Add(-90 * time.Minute), SessionID: "bounded-switch",
		RecordType: "user", UUID: "pre-since-switch", Raw: json.RawMessage(`{}`),
	}}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	p, err := s.PreviewBackfillSessionRecordLoginEmail(ctx, since)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if p.FillableRows != 1 {
		t.Fatalf("preview fillable rows = %d, want 1", p.FillableRows)
	}
	if _, err := s.BackfillSessionRecordLoginEmail(ctx, since); err != nil {
		t.Fatalf("bounded backfill: %v", err)
	}
	if email, source := loginEmailSourceOf(t, s, "pre-since-switch"); email != "one@example.com" || source != "otel" {
		t.Fatalf("pre-switch row = (%q, %q), want (one@example.com, otel)", email, source)
	}
}

// A session with no OTEL at all stays empty. The backfill degrades to blank,
// never to a guess -- the same contract as codexauth/CodexAccountAt.
func TestBackfillLeavesOtelLessSessionBlank(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := recentDay(31).Add(10 * time.Hour)

	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: ts, EventName: "api_request", SessionID: "has-otel", LoginEmail: "one@example.com"},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "no-otel", RecordType: "user", UUID: "n1", Raw: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	if _, err := s.BackfillSessionRecordLoginEmail(ctx, time.Time{}); err != nil {
		t.Fatalf("BackfillSessionRecordLoginEmail: %v", err)
	}

	var got string
	if err := s.pool.QueryRow(ctx, `SELECT login_email FROM session_records WHERE uuid='n1'`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != "" {
		t.Fatalf("login_email = %q, want '' (no OTEL for this session)", got)
	}
}

// The periodic pass bounds itself to recent OTEL so it stops rescanning the
// permanently unfillable tail on every tick. A full pass is `since` zero.
func TestBackfillSinceBoundsTheScan(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	old := recentDay(60).Add(10 * time.Hour)
	recent := recentDay(30).Add(10 * time.Hour)

	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: old, EventName: "api_request", SessionID: "old-sess", LoginEmail: "one@example.com"},
		{Ts: recent, EventName: "api_request", SessionID: "new-sess", LoginEmail: "two@example.com"},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: old, SessionID: "old-sess", RecordType: "user", UUID: "o1", Raw: json.RawMessage(`{}`)},
		{Ts: recent, SessionID: "new-sess", RecordType: "user", UUID: "r1", Raw: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	// Bounded pass: only the recent session is in scope.
	n, err := s.BackfillSessionRecordLoginEmail(ctx, recent.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("bounded backfill: %v", err)
	}
	if n != 1 {
		t.Fatalf("bounded pass updated %d rows, want 1", n)
	}
	var oldEmail string
	if err := s.pool.QueryRow(ctx, `SELECT login_email FROM session_records WHERE uuid='o1'`).Scan(&oldEmail); err != nil {
		t.Fatalf("query o1: %v", err)
	}
	if oldEmail != "" {
		t.Fatalf("o1 login_email = %q, want '' (out of bounded scan)", oldEmail)
	}

	// Full pass picks up what the bounded pass left behind.
	if _, err := s.BackfillSessionRecordLoginEmail(ctx, time.Time{}); err != nil {
		t.Fatalf("full backfill: %v", err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT login_email FROM session_records WHERE uuid='o1'`).Scan(&oldEmail); err != nil {
		t.Fatalf("query o1 again: %v", err)
	}
	if oldEmail != "one@example.com" {
		t.Fatalf("o1 login_email = %q, want one@example.com after full pass", oldEmail)
	}
}
