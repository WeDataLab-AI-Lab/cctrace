package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestPgStore_ExcludedSessions_removalRecomputes_sharedSessionStaysHidden pins the
// reason excluded_sessions must be a full recompute on every excluded_accounts
// change, not a delete keyed on the removed account: a session that two accounts
// independently touched must stay hidden after only one of them is un-excluded.
// A naive "DELETE FROM excluded_sessions WHERE session_id IN (removed account's
// sessions)" implementation breaks this test.
//
// It checks ListSessionRecords (== visible_session_records, the second anti-join
// this feature rewrites) rather than the session overview list: the overview's
// otel arm reads visible_events directly, which reveals an account's own otel_events
// the moment that account itself is un-excluded, regardless of what else touched the
// session — a separate, correct behavior that would make an overview-based
// assertion pass even with a buggy (delete-based) excluded_sessions implementation.
func TestPgStore_ExcludedSessions_removalRecomputes_sharedSessionStaysHidden(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := recentDay(57).Add(10 * time.Hour)

	const accountA = "a@example.com"
	const accountB = "b@example.com"
	const sharedSession = "shared-1"

	// Both accounts show up in the same session's otel_events (e.g. account switched
	// mid-session, or a shared machine). The session_records row itself carries no
	// login_email (not yet backfilled), so its visibility depends entirely on the
	// second anti-join against these otel_events.
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: ts, EventName: "api_request", SessionID: sharedSession, LoginEmail: accountA, UserID: "u-a"},
		{Ts: ts, EventName: "api_request", SessionID: sharedSession, LoginEmail: accountB, UserID: "u-b"},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: sharedSession, RecordType: "user", ProfileEmail: "p@example.com",
			UUID: "shared-1-rec", Raw: json.RawMessage(`{"text":"hi"}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	recordsVisible := func() bool {
		t.Helper()
		records, err := s.ListSessionRecords(ctx, SessionRecordFilter{SessionID: sharedSession, Limit: 10})
		if err != nil {
			t.Fatalf("ListSessionRecords: %v", err)
		}
		return len(records) > 0
	}

	if _, err := s.ExcludeAccount(ctx, accountA, "r", "admin@example.com"); err != nil {
		t.Fatalf("ExcludeAccount(A): %v", err)
	}
	if _, err := s.ExcludeAccount(ctx, accountB, "r", "admin@example.com"); err != nil {
		t.Fatalf("ExcludeAccount(B): %v", err)
	}

	if recordsVisible() {
		t.Fatalf("shared session's records visible while both accounts excluded")
	}

	// Un-exclude just one of the two accounts that hid this session.
	if err := s.RemoveExcludedAccount(ctx, accountA); err != nil {
		t.Fatalf("RemoveExcludedAccount(A): %v", err)
	}

	if recordsVisible() {
		t.Fatalf("shared session's records became visible after un-excluding only one of two accounts that hid it")
	}

	// Un-excluding the second account should finally reveal it.
	if err := s.RemoveExcludedAccount(ctx, accountB); err != nil {
		t.Fatalf("RemoveExcludedAccount(B): %v", err)
	}
	if !recordsVisible() {
		t.Fatalf("shared session's records stayed hidden after both accounts that hid it were un-excluded")
	}
}

// TestPgStore_ExcludedSessions_emptySessionIDRemainsVisible pins the semantics
// carried over from the original otel_events anti-join: session_records rows with
// an empty session_id are never subject to the second exclusion check.
func TestPgStore_ExcludedSessions_emptySessionIDRemainsVisible(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "", RecordType: "user", ProfileEmail: "p@example.com",
			UUID: "no-session-id", Raw: json.RawMessage(`{"text":"orphan"}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	if _, err := s.ExcludeAccount(ctx, "someone@example.com", "r", "admin@example.com"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}

	var count int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM visible_session_records WHERE uuid = 'no-session-id'`).Scan(&count); err != nil {
		t.Fatalf("query visible_session_records: %v", err)
	}
	if count != 1 {
		t.Fatalf("empty session_id row hidden by exclusion (count=%d), want 1", count)
	}
}

// TestPgStore_RefreshExcludedSessionsIncremental_picksUpNewSessions pins the
// incremental refresh path used by the periodic job in cmd/cctraced: newly ingested
// otel_events for an already-excluded account must get their session_id added to
// excluded_sessions without a full recompute.
func TestPgStore_RefreshExcludedSessionsIncremental_picksUpNewSessions(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := recentDay(57).Add(10 * time.Hour)

	const account = "hidden@example.com"
	if _, err := s.ExcludeAccount(ctx, account, "r", "admin@example.com"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}

	// Ingest happens after the account is already excluded — simulating a new
	// session showing up between two ticks of the periodic refresh.
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: ts, EventName: "api_request", SessionID: "late-session", LoginEmail: account, UserID: "u"},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "late-session", RecordType: "user", ProfileEmail: "p@example.com",
			UUID: "late-rec", Raw: json.RawMessage(`{"text":"late"}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	// Before the incremental refresh runs, the new session is still visible — this
	// is the documented staleness window, not a bug.
	if ids := sessionIDsInOverviews(t, s); !ids["late-session"] {
		t.Fatalf("precondition failed: late-session should be visible before incremental refresh")
	}

	if err := s.RefreshExcludedSessionsIncremental(ctx); err != nil {
		t.Fatalf("RefreshExcludedSessionsIncremental: %v", err)
	}

	if ids := sessionIDsInOverviews(t, s); ids["late-session"] {
		t.Fatalf("late-session still visible after incremental refresh caught it up")
	}
}

// TestPgStore_Migrate_backfillGuardDoesNotRerunOnEmptyResult pins the migration's
// one-time backfill guard against the same trap projects.last_session_at hit: an
// excluded account with zero matching otel_events makes the backfill INSERT 0 rows,
// so a guard keyed on "is excluded_sessions empty" can never distinguish "hasn't run
// yet" from "ran and found nothing" — it reruns the otel_events scan on every
// restart. The guard must be keyed on excluded_sessions_refresh_state's existence
// instead, which the backfill writes unconditionally (even on a 0-row match).
//
// Observed indirectly: after the first Migrate (0 matching events), a session that
// starts matching the excluded account is inserted directly (bypassing
// ExcludeAccount/RecomputeExcludedSessions, which would mask the migration guard's
// own behavior). A second Migrate call must NOT rerun the one-time backfill — if it
// did, the unconditional "SELECT DISTINCT ... JOIN excluded_accounts" would sweep up
// that new session and hide it, even though nothing asked for a resync.
func TestPgStore_Migrate_backfillGuardDoesNotRerunOnEmptyResult(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := recentDay(45).Add(10 * time.Hour)

	const account = "guard-empty@example.com"
	// Insert the exclusion directly, not via ExcludeAccount: that call already
	// invokes RecomputeExcludedSessions itself, which would seed
	// excluded_sessions_refresh_state and mask exactly the migration-guard bug this
	// test targets.
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO excluded_accounts (login_email, reason, created_by) VALUES ($1, 'r', 'a')`,
		account); err != nil {
		t.Fatalf("insert excluded_accounts: %v", err)
	}

	// No otel_events exist yet, so the migration's one-time backfill matches 0 rows.
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate (first): %v", err)
	}
	var stateRows int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM excluded_sessions_refresh_state`).Scan(&stateRows); err != nil {
		t.Fatalf("count refresh_state: %v", err)
	}
	if stateRows != 1 {
		t.Fatalf("refresh_state rows after first Migrate = %d, want 1 (backfill must record it ran even on a 0-row match)", stateRows)
	}

	// A session for the excluded account shows up after that first Migrate call —
	// nothing has resynced excluded_sessions for it yet (documented staleness window).
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: ts, EventName: "api_request", SessionID: "post-migrate-session", LoginEmail: account, UserID: "u"},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "post-migrate-session", RecordType: "user", ProfileEmail: "p@example.com",
			UUID: "post-migrate-rec", Raw: json.RawMessage(`{"text":"hi"}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate (second): %v", err)
	}

	records, err := s.ListSessionRecords(ctx, SessionRecordFilter{SessionID: "post-migrate-session", Limit: 10})
	if err != nil {
		t.Fatalf("ListSessionRecords: %v", err)
	}
	if len(records) == 0 {
		t.Fatalf("session hidden after a second Migrate call — the one-time backfill guard re-ran and swept up a session nothing asked it to resync")
	}
}

// TestPgStore_RefreshExcludedSessionsIncremental_catchesDelayedArrival pins the
// watermark's id (not ts) basis: otel_events can replay through the disk WAL buffer
// (internal/buffer/diskspiller.go) after downtime, and a replayed row keeps its
// original event ts. If the watermark were ts-based, a replayed row whose ts is
// already behind the watermark would never be picked up by "ts > watermark" —
// permanently leaking an excluded account's session. Because id is assigned at
// insert time, a replayed row always gets an id above the watermark regardless of
// its ts, so "id > watermark" catches it.
func TestPgStore_RefreshExcludedSessionsIncremental_catchesDelayedArrival(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	const account = "delayed@example.com"
	if _, err := s.ExcludeAccount(ctx, account, "r", "admin@example.com"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}

	// Advance the watermark past a "recent" event.
	recentTs := recentDay(45).Add(12 * time.Hour)
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: recentTs, EventName: "api_request", SessionID: "recent-session", LoginEmail: account, UserID: "u"},
	}); err != nil {
		t.Fatalf("InsertEvents(recent): %v", err)
	}
	if err := s.RefreshExcludedSessionsIncremental(ctx); err != nil {
		t.Fatalf("RefreshExcludedSessionsIncremental (advance watermark): %v", err)
	}

	// A delayed/replayed event arrives afterwards, carrying an OLD ts (well before
	// the watermark just advanced past) but — because it is a fresh insert — a NEW,
	// larger otel_events.id.
	delayedTs := recentTs.Add(-24 * time.Hour)
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: delayedTs, EventName: "api_request", SessionID: "delayed-session", LoginEmail: account, UserID: "u"},
	}); err != nil {
		t.Fatalf("InsertEvents(delayed): %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: delayedTs, SessionID: "delayed-session", RecordType: "user", ProfileEmail: "p@example.com",
			UUID: "delayed-rec", Raw: json.RawMessage(`{"text":"delayed"}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	if err := s.RefreshExcludedSessionsIncremental(ctx); err != nil {
		t.Fatalf("RefreshExcludedSessionsIncremental (catch delayed): %v", err)
	}

	records, err := s.ListSessionRecords(ctx, SessionRecordFilter{SessionID: "delayed-session", Limit: 10})
	if err != nil {
		t.Fatalf("ListSessionRecords: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("delayed-arrival session still visible after incremental refresh — a ts-based watermark would miss it (its ts is older than the watermark), leaking an excluded account's session indefinitely")
	}
}

// An event beyond the watermark can yield no newly excluded sessions, either
// because its account is visible or because ON CONFLICT returns no rows.
func TestPgStore_ExcludedSessions_incrementalNoNewExclusionsPreservesRollups(t *testing.T) {
	for _, alreadyExcluded := range []bool{false, true} {
		name := "visible account"
		if alreadyExcluded {
			name = "already excluded session"
		}
		t.Run(name, func(t *testing.T) {
			s := acquireTestStore(t)
			truncateTables(t, s)
			ctx := context.Background()
			if err := s.InsertEvents(ctx, []*OtelEvent{{Ts: time.Now(), EventName: "api_request", SessionID: "untouched"}}); err != nil {
				t.Fatal(err)
			}
			// Leave an orphan rollup: a full rebuild would delete it, even if an
			// ordinary delete/reinsert would otherwise produce identical values.
			if _, err := s.pool.Exec(ctx, `DELETE FROM otel_events WHERE session_id='untouched'`); err != nil {
				t.Fatal(err)
			}
			if err := s.InsertEvents(ctx, []*OtelEvent{{Ts: time.Now(), EventName: "api_request", SessionID: "new-event", LoginEmail: "account@example.com"}}); err != nil {
				t.Fatal(err)
			}
			if alreadyExcluded {
				if _, err := s.pool.Exec(ctx, `INSERT INTO excluded_accounts (login_email) VALUES ('account@example.com'); INSERT INTO excluded_sessions (session_id) VALUES ('new-event')`); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.RefreshExcludedSessionsIncremental(ctx); err != nil {
				t.Fatal(err)
			}
			var preserved bool
			if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM session_overview_rollups WHERE session_id='untouched')`).Scan(&preserved); err != nil {
				t.Fatal(err)
			}
			if !preserved {
				t.Error("incremental refresh rebuilt unrelated rollups with zero newly excluded sessions")
			}
			var caughtUp bool
			if err := s.pool.QueryRow(ctx, `SELECT last_event_id = (SELECT max(id) FROM otel_events) FROM excluded_sessions_refresh_state WHERE id`).Scan(&caughtUp); err != nil {
				t.Fatal(err)
			}
			if !caughtUp {
				t.Error("incremental watermark did not advance")
			}
		})
	}
}
