package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

const (
	keptAccount     = "kept@example.com"
	excludedAccount = "hidden@example.com"
)

// seedTwoAccounts gives each account one event, one conversation record and one
// metric, so every dashboard surface has something to show for both.
func seedTwoAccounts(t *testing.T, s *PgStore, ts time.Time) {
	t.Helper()
	ctx := context.Background()

	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: ts, EventName: "api_request", SessionID: "keep-1", LoginEmail: keptAccount,
			UserID: "u-keep", ProfileEmail: "p@example.com", CostUSD: ptrFloat(1.5)},
		{Ts: ts, EventName: "api_request", SessionID: "hide-1", LoginEmail: excludedAccount,
			UserID: "u-hide", ProfileEmail: "p@example.com", CostUSD: ptrFloat(99)},
		// Same account, a session whose records have not been backfilled yet.
		{Ts: ts, EventName: "api_request", SessionID: "hide-2", LoginEmail: excludedAccount,
			UserID: "u-hide", ProfileEmail: "p@example.com", CostUSD: ptrFloat(1)},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "keep-1", RecordType: "user", ProfileEmail: "p@example.com",
			LoginEmail: keptAccount, UUID: "k1", Raw: json.RawMessage(`{"text":"company work"}`)},
		{Ts: ts, SessionID: "hide-1", RecordType: "user", ProfileEmail: "p@example.com",
			LoginEmail: excludedAccount, UUID: "h1", Raw: json.RawMessage(`{"text":"private prompt"}`)},
		// How the sync path actually writes: login_email empty until the periodic
		// backfill runs. Exclusion has to hold in this window too, so this row is the
		// one that proves the view matches on the session's OTEL events.
		{Ts: ts, SessionID: "hide-2", RecordType: "user", ProfileEmail: "p@example.com",
			UUID: "h2", Raw: json.RawMessage(`{"text":"private prompt, not yet backfilled"}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.InsertMetrics(ctx, []*OtelMetric{
		{Ts: ts, MetricName: "codex.poll", LoginEmail: excludedAccount},
	}); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}
}

func sessionIDsInOverviews(t *testing.T, s *PgStore) map[string]bool {
	t.Helper()
	overviews, err := s.ListSessionOverviews(context.Background(), SessionOverviewFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListSessionOverviews: %v", err)
	}
	ids := map[string]bool{}
	for _, o := range overviews {
		ids[o.SessionID] = true
	}
	return ids
}

// TestPgStore_ExcludeAccount_hidesAccountAcrossDashboard drives the real dashboard
// readers rather than the views, because that distinction is exactly where an
// earlier version of this feature broke: the views filtered correctly while the
// session and conversation readers still served the excluded account.
func TestPgStore_ExcludeAccount_hidesAccountAcrossDashboard(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	since, until := ts.Add(-time.Hour), ts.Add(time.Hour)

	seedTwoAccounts(t, s, ts)

	if ids := sessionIDsInOverviews(t, s); !ids["hide-1"] {
		t.Fatalf("precondition failed: hide-1 missing from overviews before exclusion")
	}

	hidden, err := s.ExcludeAccount(ctx, excludedAccount, "personal account", "admin@example.com")
	if err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}
	if hidden != 2 {
		t.Fatalf("reported hidden events = %d, want 2 (blast radius must be reported)", hidden)
	}

	// Session list: the excluded sessions must be gone, not merely zero-cost.
	ids := sessionIDsInOverviews(t, s)
	if ids["hide-1"] {
		t.Fatalf("excluded account's session still listed")
	}
	if ids["hide-2"] {
		t.Fatalf("excluded account's not-yet-backfilled session still listed")
	}
	if !ids["keep-1"] {
		t.Fatalf("kept account's session disappeared")
	}

	// Conversation content is the sensitive part — it must not be readable, including
	// for the session whose records still carry an empty login_email.
	for _, sid := range []string{"hide-1", "hide-2"} {
		records, err := s.ListSessionRecords(ctx, SessionRecordFilter{SessionID: sid, Limit: 10})
		if err != nil {
			t.Fatalf("ListSessionRecords(%s): %v", sid, err)
		}
		if len(records) != 0 {
			t.Fatalf("excluded account's conversation still readable for %s (%d records)", sid, len(records))
		}
	}
	kept, err := s.ListSessionRecords(ctx, SessionRecordFilter{SessionID: "keep-1", Limit: 10})
	if err != nil {
		t.Fatalf("ListSessionRecords(keep): %v", err)
	}
	if len(kept) != 1 {
		t.Fatalf("kept account's conversation went missing (%d records)", len(kept))
	}

	// Cost aggregation must drop the excluded spend.
	summaries, err := s.CostByUser(ctx, since, until, "", "", "")
	if err != nil {
		t.Fatalf("CostByUser: %v", err)
	}
	for _, c := range summaries {
		for _, e := range c.LoginEmails {
			if e == excludedAccount {
				t.Fatalf("excluded account still in CostByUser")
			}
		}
	}

	// The account selector must not offer it either.
	accounts, err := s.ListLoginAccounts(ctx, "")
	if err != nil {
		t.Fatalf("ListLoginAccounts: %v", err)
	}
	for _, a := range accounts {
		if a == excludedAccount {
			t.Fatalf("excluded account still listed in account selector")
		}
	}

	// What it hides stays auditable.
	listed, err := s.ListExcludedAccounts(ctx)
	if err != nil {
		t.Fatalf("ListExcludedAccounts: %v", err)
	}
	if len(listed) != 1 || listed[0].LoginEmail != excludedAccount {
		t.Fatalf("excluded account not reported back: %+v", listed)
	}
	if listed[0].CostUSD != 100 || listed[0].EventCount != 2 {
		t.Fatalf("hidden volume not reported: cost=%v events=%d, want 100/2", listed[0].CostUSD, listed[0].EventCount)
	}
	if listed[0].CreatedBy != "admin@example.com" {
		t.Fatalf("actor not recorded: %q", listed[0].CreatedBy)
	}

	// Removing the entry restores everything — nothing was destroyed.
	if err := s.RemoveExcludedAccount(ctx, excludedAccount); err != nil {
		t.Fatalf("RemoveExcludedAccount: %v", err)
	}
	if ids := sessionIDsInOverviews(t, s); !ids["hide-1"] {
		t.Fatalf("session did not come back after removal — exclusion must be reversible")
	}
	back, err := s.ListSessionRecords(ctx, SessionRecordFilter{SessionID: "hide-1", Limit: 10})
	if err != nil {
		t.Fatalf("ListSessionRecords after removal: %v", err)
	}
	if len(back) != 1 {
		t.Fatalf("conversation did not come back after removal (%d records)", len(back))
	}
}

// TestPgStore_ExcludeAccount_normalizesAndRejectsBadInput guards the key format:
// an empty or malformed entry would hide every row whose login_email is unset.
func TestPgStore_ExcludeAccount_normalizesAndRejectsBadInput(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	for _, bad := range []string{"", "   ", "not-an-email"} {
		if _, err := s.ExcludeAccount(ctx, bad, "r", "a@example.com"); err == nil {
			t.Fatalf("ExcludeAccount(%q) succeeded, want rejection", bad)
		}
	}

	if _, err := s.ExcludeAccount(ctx, "  Hidden@Example.COM  ", "r", "a@example.com"); err != nil {
		t.Fatalf("ExcludeAccount with padding/case: %v", err)
	}
	listed, err := s.ListExcludedAccounts(ctx)
	if err != nil {
		t.Fatalf("ListExcludedAccounts: %v", err)
	}
	if len(listed) != 1 || listed[0].LoginEmail != excludedAccount {
		t.Fatalf("login_email not normalized: %+v", listed)
	}
}

// TestPgStore_CleanupOrphanSessionRecords_keepsExcludedAccountRows pins the
// invariant that makes exclusion reversible: orphan cleanup reads the unfiltered
// unified_events. If it ever reads visible_events, an excluded account's events
// are hidden, its sessions look orphaned, and its session_records get deleted —
// silent, permanent data loss dressed up as a presentation setting.
func TestPgStore_CleanupOrphanSessionRecords_keepsExcludedAccountRows(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 6, 11, 0, 0, 0, time.UTC)

	seedTwoAccounts(t, s, ts)
	if _, err := s.ExcludeAccount(ctx, excludedAccount, "personal account", "admin@example.com"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}

	if _, err := s.CleanupOrphanSessionRecords(ctx); err != nil {
		t.Fatalf("CleanupOrphanSessionRecords: %v", err)
	}

	var remaining int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM session_records WHERE session_id = $1`, "hide-1").Scan(&remaining); err != nil {
		t.Fatalf("count session_records: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("excluded account's session_records were deleted (%d left) — exclusion must not destroy data", remaining)
	}
}

// overviewRowVersions identifies the physical overview rows of one session. A
// rewrite of the row, even to identical values, gives it a new xmin.
func overviewRowVersions(t *testing.T, s *PgStore, sessionID string) string {
	t.Helper()
	var v string
	if err := s.pool.QueryRow(context.Background(), `
		SELECT COALESCE(string_agg(xmin::text, ',' ORDER BY scope_type, scope_value), '')
		FROM session_overview_rollups WHERE session_id = $1`, sessionID).Scan(&v); err != nil {
		t.Fatalf("read overview row versions: %v", err)
	}
	return v
}

// #715: excluding one address rebuilt the session overview for all history, in
// the request, under the exclusive maintenance lock -- 640s for one POST on a
// production-sized copy. Only the sessions whose visibility can change may be
// rewritten; a session of an unrelated address must be left exactly as it was.
func TestExclusionLeavesUnrelatedOverviewRowsUntouched(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)

	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: ts, EventName: "api_request", SessionID: "bystander", LoginEmail: "bystander@example.test",
			UserID: "u-b", ProfileEmail: "p@example.test", CostUSD: ptrFloat(1)},
		{Ts: ts, EventName: "api_request", SessionID: "personal", LoginEmail: "personal@example.test",
			UserID: "u-p", ProfileEmail: "p@example.test", CostUSD: ptrFloat(2)},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	before := overviewRowVersions(t, s, "bystander")
	if before == "" {
		t.Fatal("precondition failed: bystander has no overview rows")
	}

	if _, err := s.ExcludeAccount(ctx, "Personal@Example.test", "personal", "admin"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}
	if ids := sessionIDsInOverviews(t, s); ids["personal"] || !ids["bystander"] {
		t.Fatalf("after exclusion the list is %v, want bystander only", ids)
	}
	if got := overviewRowVersions(t, s, "bystander"); got != before {
		t.Errorf("excluding an unrelated address rewrote the bystander's overview rows (xmin %s -> %s)", before, got)
	}

	if err := s.RemoveExcludedAccount(ctx, "personal@example.test"); err != nil {
		t.Fatalf("RemoveExcludedAccount: %v", err)
	}
	if ids := sessionIDsInOverviews(t, s); !ids["personal"] || !ids["bystander"] {
		t.Fatalf("after un-excluding the list is %v, want both sessions", ids)
	}
	if got := overviewRowVersions(t, s, "bystander"); got != before {
		t.Errorf("un-excluding an unrelated address rewrote the bystander's overview rows (xmin %s -> %s)", before, got)
	}
}

// A session known to the address only through its OTEL rows -- nothing else, or
// conversation records not yet backfilled with the address -- is hidden through
// excluded_sessions. The scoped rebuild has to reach it on both edges.
func TestSessionHiddenThroughItsOtelRowsLeavesAndReturns(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	const addr = "personal@example.test"

	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: ts, EventName: "api_request", SessionID: "otel-only", LoginEmail: addr,
			UserID: "u-p", ProfileEmail: "p@example.test", CostUSD: ptrFloat(1)},
		{Ts: ts, EventName: "api_request", SessionID: "not-backfilled", LoginEmail: addr,
			UserID: "u-p", ProfileEmail: "p@example.test", CostUSD: ptrFloat(1)},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "not-backfilled", RecordType: "user", ProfileEmail: "p@example.test",
			UUID: "nb1", Raw: json.RawMessage(`{"text":"private prompt"}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	if _, err := s.ExcludeAccount(ctx, addr, "personal", "admin"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}
	ids := sessionIDsInOverviews(t, s)
	if ids["otel-only"] || ids["not-backfilled"] {
		t.Errorf("sessions of the excluded address are still listed: %v", ids)
	}

	if err := s.RemoveExcludedAccount(ctx, addr); err != nil {
		t.Fatalf("RemoveExcludedAccount: %v", err)
	}
	ids = sessionIDsInOverviews(t, s)
	if !ids["otel-only"] || !ids["not-backfilled"] {
		t.Errorf("sessions did not come back to the list after un-excluding: %v", ids)
	}
}
