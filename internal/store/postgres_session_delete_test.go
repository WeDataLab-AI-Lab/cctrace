package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// seedTwoSessions gives each session rows in all three tables, plus one row under
// the EMPTY session id.
//
// That last row is the point of the fixture. session_records legitimately holds
// rows whose session_id is the empty string (metadata lines belonging to no
// session), and on prod
// otel_metrics held 3.06M of its 3.67M rows under the same empty id -- every
// user's data. A delete that treats that empty value as a filter is not a no-op,
// it is a wipe,
// so every test here checks the empty-id rows are still standing afterwards.
func seedTwoSessions(t *testing.T, s *PgStore, ts time.Time) {
	t.Helper()
	ctx := context.Background()

	if err := s.UpsertProject(ctx, "claude", "ph-doomed", "doomed_project", "", "", "", "", ts); err != nil {
		t.Fatalf("UpsertProject doomed: %v", err)
	}
	if err := s.UpsertProject(ctx, "claude", "ph-kept", "kept_project", "", "", "", "", ts); err != nil {
		t.Fatalf("UpsertProject kept: %v", err)
	}

	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: ts, EventName: "api_request", SessionID: "doomed", ProfileEmail: "p@example.com", UserID: "u-1"},
		{Ts: ts, EventName: "api_request", SessionID: "kept", ProfileEmail: "p@example.com", UserID: "u-1"},
		{Ts: ts, EventName: "api_request", SessionID: "", ProfileEmail: "p@example.com", UserID: "u-1"},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if err := s.InsertMetrics(ctx, []*OtelMetric{
		{Ts: ts, MetricName: "token.usage", SessionID: "doomed", ProfileEmail: "p@example.com"},
		{Ts: ts, MetricName: "token.usage", SessionID: "kept", ProfileEmail: "p@example.com"},
		{Ts: ts, MetricName: "token.usage", SessionID: "", ProfileEmail: "p@example.com"},
	}); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "doomed", RecordType: "user", ProfileEmail: "p@example.com", UserID: "u-1",
			ProjectHash: "ph-doomed", UUID: "d1", Raw: json.RawMessage(`{"text":"private"}`)},
		{Ts: ts, SessionID: "kept", RecordType: "user", ProfileEmail: "p@example.com", UserID: "u-1",
			ProjectHash: "ph-kept", UUID: "k1", Raw: json.RawMessage(`{"text":"work"}`)},
		{Ts: ts, SessionID: "", RecordType: "last-prompt", ProfileEmail: "p@example.com", UserID: "u-1",
			UUID: "m1", Raw: json.RawMessage(`{"text":"metadata"}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
}

func countRows(t *testing.T, s *PgStore, table, sessionID string) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(),
		"SELECT count(*) FROM "+table+" WHERE session_id = $1", sessionID).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// countVisible reports what visible_session_records shows -- the view the session
// list reads. A delete is "done" from a person's point of view when this hits zero,
// which happens at commit, long before the rows are gone.
func countVisible(t *testing.T, s *PgStore, sessionID string) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(),
		"SELECT count(*) FROM visible_session_records WHERE session_id = $1", sessionID).Scan(&n); err != nil {
		t.Fatalf("count visible: %v", err)
	}
	return n
}

func TestPgStore_DeleteSession_hidesImmediatelyWithoutDeletingRows(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := recentDay(42).Add(10 * time.Hour)
	seedTwoSessions(t, s, ts)

	if countVisible(t, s, "doomed") == 0 {
		t.Fatal("fixture is wrong: the session is not visible before the delete")
	}

	res, err := s.DeleteSession(ctx, "doomed", "admin@example.com", "personal", false, false)
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if res.MarkedSessions != 1 {
		t.Errorf("MarkedSessions = %d, want 1", res.MarkedSessions)
	}

	// Gone from the list...
	if n := countVisible(t, s, "doomed"); n != 0 {
		t.Errorf("still visible after delete: %d rows — the whole point of writing to "+
			"excluded_sessions is that this is true before the sweep runs", n)
	}
	// ...but the rows are still there. Deleting them takes 15-30s on compressed
	// hypertables, which is why it does not happen on the request.
	if n := countRows(t, s, "session_records", "doomed"); n == 0 {
		t.Error("rows were deleted synchronously; that is the 15-second path this design removed")
	}
	if countVisible(t, s, "kept") == 0 {
		t.Error("an untouched session went missing")
	}
}

func TestPgStore_SweepDeletedSessions_reclaimsRows(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := recentDay(42).Add(10 * time.Hour)
	seedTwoSessions(t, s, ts)

	if _, err := s.DeleteSession(ctx, "doomed", "admin@example.com", "", false, false); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := s.SweepDeletedSessions(ctx, 25); err != nil {
		t.Fatalf("SweepDeletedSessions: %v", err)
	}

	for _, table := range []string{"otel_events", "otel_metrics", "session_records"} {
		if n := countRows(t, s, table, "doomed"); n != 0 {
			t.Errorf("%s still holds %d rows after the sweep", table, n)
		}
		if n := countRows(t, s, table, "kept"); n != 1 {
			t.Errorf("%s: the sweep reached an untouched session", table)
		}
		if n := countRows(t, s, table, ""); n != 1 {
			t.Errorf("%s: the sweep reached rows carrying no session id", table)
		}
	}

	// Idempotent: a second pass finds nothing left and must not error.
	if n, err := s.SweepDeletedSessions(ctx, 25); err != nil || n != 0 {
		t.Errorf("second sweep: n=%d err=%v; want 0 and no error", n, err)
	}
	// The tombstone outlives the rows. Dropping it here would let the next sync
	// re-add the session.
	bl, err := s.LoadIngestBlocklist(ctx)
	if err != nil {
		t.Fatalf("LoadIngestBlocklist: %v", err)
	}
	if !bl.DeletedSessions["doomed"] {
		t.Error("the sweep removed the tombstone along with the rows")
	}
}

func TestPgStore_RecomputeExcludedSessions_keepsTombstones(t *testing.T) {
	// The rebuild TRUNCATEs and re-derives from excluded_accounts. A tombstone that
	// did not survive it would put a deleted session back in every list.
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := recentDay(42).Add(10 * time.Hour)
	seedTwoSessions(t, s, ts)

	if _, err := s.DeleteSession(ctx, "doomed", "admin@example.com", "", false, false); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if err := s.RecomputeExcludedSessions(ctx); err != nil {
		t.Fatalf("RecomputeExcludedSessions: %v", err)
	}
	if n := countVisible(t, s, "doomed"); n != 0 {
		t.Errorf("deleted session reappeared after a recompute: %d rows", n)
	}
}

func TestPgStore_DeleteSession_rejectsEmptySessionID(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := recentDay(42).Add(10 * time.Hour)
	seedTwoSessions(t, s, ts)

	if _, err := s.DeleteSession(ctx, "", "admin@example.com", "", false, false); err == nil {
		t.Fatal("DeleteSession(\"\") returned nil error; the empty id is not an empty " +
			"filter — on prod it matched 3.06M otel_metrics rows belonging to every user")
	}

	for _, table := range []string{"otel_events", "otel_metrics", "session_records"} {
		if n := countRows(t, s, table, ""); n != 1 {
			t.Errorf("%s: empty-session-id rows = %d after the rejected delete; want 1", table, n)
		}
		if n := countRows(t, s, table, "doomed"); n != 1 {
			t.Errorf("%s: unrelated rows disturbed by the rejected delete", table)
		}
	}
}

func TestPgStore_DeleteSession_tombstoneSurvivesResync(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := recentDay(42).Add(10 * time.Hour)
	seedTwoSessions(t, s, ts)

	if _, err := s.DeleteSession(ctx, "doomed", "admin@example.com", "", false, false); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	bl, err := s.LoadIngestBlocklist(ctx)
	if err != nil {
		t.Fatalf("LoadIngestBlocklist: %v", err)
	}
	if !bl.DeletedSessions["doomed"] {
		t.Error("deleted session missing from the blocklist; ingest would re-accept it " +
			"on the client's next scan of the same local file")
	}
	if bl.DeletedSessions["kept"] {
		t.Error("blocklist names a session that was never deleted")
	}

	// A second delete of the same session must not fail: a sync already in flight
	// can put rows back between the delete and the cache refresh.
	if _, err := s.DeleteSession(ctx, "doomed", "other@example.com", "again", false, false); err != nil {
		t.Fatalf("second DeleteSession: %v; the tombstone upsert must tolerate a repeat", err)
	}
}

func TestPgStore_DeleteSession_blockProjectAndPurge(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := recentDay(42).Add(10 * time.Hour)
	seedTwoSessions(t, s, ts)

	res, err := s.DeleteSession(ctx, "doomed", "admin@example.com", "personal", true, true)
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if !res.ProjectBlocked {
		t.Fatal("ProjectBlocked = false; blockProject was true")
	}

	list, err := s.ListBlockedProjects(ctx)
	if err != nil {
		t.Fatalf("ListBlockedProjects: %v", err)
	}
	if len(list) != 1 || list[0].ProjectHash != "ph-doomed" {
		t.Fatalf("blocked = %+v; want exactly ph-doomed", list)
	}
	if list[0].ProjectName != "doomed_project" {
		t.Errorf("ProjectName = %q; the list has to name the project a human recognises", list[0].ProjectName)
	}

	// A block stops future collection; it does not erase stored history. Seed a row
	// that raced past ingest and check the sweep, not the block, is what removes it.
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "raced", RecordType: "user", ProfileEmail: "p@example.com",
			ProjectHash: "ph-doomed", UUID: "r1", Raw: json.RawMessage(`{"text":"slipped in"}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords raced: %v", err)
	}
	// PurgeBlockedProjects tombstones; it does not delete. That keeps every removal
	// on one path, which is where the decompression handling lives.
	if n, err := s.PurgeBlockedProjects(ctx); err != nil {
		t.Fatalf("PurgeBlockedProjects: %v", err)
	} else if n == 0 {
		t.Error("purge marked nothing; the session that raced past ingest is still visible")
	}
	if n := countVisible(t, s, "raced"); n != 0 {
		t.Errorf("raced session still visible after the purge: %d rows", n)
	}
	if _, err := s.SweepDeletedSessions(ctx, 25); err != nil {
		t.Fatalf("SweepDeletedSessions: %v", err)
	}
	if n := countRows(t, s, "session_records", "raced"); n != 0 {
		t.Errorf("raced row survived the sweep: %d rows", n)
	}
	if n := countRows(t, s, "session_records", "kept"); n != 1 {
		t.Error("the sweep reached a session outside the blocked project")
	}
	if n := countRows(t, s, "session_records", ""); n != 1 {
		t.Error("the sweep reached rows carrying no session id")
	}

	if err := s.UnblockProject(ctx, "ph-doomed"); err != nil {
		t.Fatalf("UnblockProject: %v", err)
	}
	if list, err := s.ListBlockedProjects(ctx); err != nil || len(list) != 0 {
		t.Errorf("after unblock: list = %+v, err = %v; want empty", list, err)
	}
}

func TestPgStore_DeletionPolicy_defaultsToAllowingOwners(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	// truncateTables empties the seeded row, which stands in for a database that
	// predates the migration. That must read as the default, not as "denied" -- a
	// missing row silently locking everyone out is the failure this pins.
	p, err := s.GetDeletionPolicy(ctx)
	if err != nil {
		t.Fatalf("GetDeletionPolicy: %v", err)
	}
	if !p.AllowOwnerDelete {
		t.Error("AllowOwnerDelete = false with no row present; want the seeded default true")
	}

	if err := s.SetDeletionPolicy(ctx, false, "admin@example.com"); err != nil {
		t.Fatalf("SetDeletionPolicy: %v", err)
	}
	p, err = s.GetDeletionPolicy(ctx)
	if err != nil {
		t.Fatalf("GetDeletionPolicy after set: %v", err)
	}
	if p.AllowOwnerDelete {
		t.Error("AllowOwnerDelete = true after being turned off")
	}
	if p.UpdatedBy != "admin@example.com" {
		t.Errorf("UpdatedBy = %q; want the actor recorded", p.UpdatedBy)
	}
}

func TestPgStore_SessionOwner(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := recentDay(42).Add(10 * time.Hour)
	seedTwoSessions(t, s, ts)

	email, userID, err := s.SessionOwner(ctx, "kept")
	if err != nil {
		t.Fatalf("SessionOwner: %v", err)
	}
	if email != "p@example.com" || userID != "u-1" {
		t.Errorf("owner = %q/%q; want p@example.com/u-1", email, userID)
	}

	// An unknown session is not an owned one. Returning empty strings without an
	// error lets the handler answer "not yours" rather than 500.
	email, userID, err = s.SessionOwner(ctx, "no-such-session")
	if err != nil {
		t.Fatalf("SessionOwner(unknown): %v", err)
	}
	if email != "" || userID != "" {
		t.Errorf("unknown session reported an owner: %q/%q", email, userID)
	}

	if _, _, err := s.SessionOwner(ctx, ""); err == nil {
		t.Error("SessionOwner(\"\") returned nil error; the empty id must be refused here too")
	}

	// /api/sync stores the user_id and profile_email the client sends, so anyone
	// holding an upload token can add a row under someone else's session_id. A
	// session whose rows name two different owners belongs to no one; picking any
	// one row let the planted row win the ownership check.
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "planted", RecordType: "user", ProfileEmail: "p@example.com", UserID: "u-1", UUID: "pl-1", Raw: json.RawMessage(`{}`)},
		{Ts: ts.Add(time.Second), SessionID: "planted", RecordType: "user", ProfileEmail: "x@example.com", UserID: "u-9", UUID: "pl-2", Raw: json.RawMessage(`{}`)},
		// A row synced before user_id existed carries only the email; it is the same
		// owner, not a second one.
		{Ts: ts, SessionID: "partial", RecordType: "user", ProfileEmail: "p@example.com", UUID: "pa-1", Raw: json.RawMessage(`{}`)},
		{Ts: ts.Add(time.Second), SessionID: "partial", RecordType: "user", ProfileEmail: "p@example.com", UserID: "u-1", UUID: "pa-2", Raw: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	for i := 0; i < 5; i++ { // an arbitrary row pick could pass by luck once
		email, userID, err = s.SessionOwner(ctx, "planted")
		if err != nil {
			t.Fatalf("SessionOwner(planted): %v", err)
		}
		if email != "" || userID != "" {
			t.Fatalf("session with two owners reported %q/%q; want no owner", email, userID)
		}
	}
	email, userID, err = s.SessionOwner(ctx, "partial")
	if err != nil {
		t.Fatalf("SessionOwner(partial): %v", err)
	}
	if email != "p@example.com" || userID != "u-1" {
		t.Errorf("owner of partially attributed session = %q/%q; want p@example.com/u-1", email, userID)
	}
}

func TestPgStore_DeleteSession_purgeTakesTheProjectsOtherSessions(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := recentDay(42).Add(10 * time.Hour)
	seedTwoSessions(t, s, ts)

	// A sibling in the same project, plus one in another project that must survive.
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "sibling", RecordType: "user", ProfileEmail: "p@example.com",
			ProjectHash: "ph-doomed", UUID: "s1", Raw: json.RawMessage(`{"text":"same project"}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords sibling: %v", err)
	}

	if n, err := s.ProjectSessionCount(ctx, "ph-doomed", "doomed"); err != nil {
		t.Fatalf("ProjectSessionCount: %v", err)
	} else if n != 1 {
		t.Fatalf("ProjectSessionCount = %d, want 1 — the dialog shows this number before "+
			"anything is deleted, so it must exclude the session being clicked", n)
	}

	res, err := s.DeleteSession(ctx, "doomed", "admin@example.com", "personal", false, true)
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if res.PurgedSessions != 1 {
		t.Errorf("PurgedSessions = %d, want 1", res.PurgedSessions)
	}
	if res.ProjectBlocked {
		t.Error("purge implied a block; they are separate decisions")
	}

	// Hidden at commit; the rows go on the sweep.
	if n := countVisible(t, s, "sibling"); n != 0 {
		t.Errorf("sibling still visible after the purge: %d rows", n)
	}
	if _, err := s.SweepDeletedSessions(ctx, 25); err != nil {
		t.Fatalf("SweepDeletedSessions: %v", err)
	}
	if n := countRows(t, s, "session_records", "sibling"); n != 0 {
		t.Errorf("sibling survived the sweep: %d rows", n)
	}
	if n := countRows(t, s, "session_records", "kept"); n != 1 {
		t.Error("the sweep reached a session in a different project")
	}
	if n := countRows(t, s, "session_records", ""); n != 1 {
		t.Error("the sweep reached rows carrying no session id")
	}

	// Every purged session needs its own tombstone, or the next sync walks the whole
	// project back in one session at a time.
	bl, err := s.LoadIngestBlocklist(ctx)
	if err != nil {
		t.Fatalf("LoadIngestBlocklist: %v", err)
	}
	for _, id := range []string{"doomed", "sibling"} {
		if !bl.DeletedSessions[id] {
			t.Errorf("%q has no tombstone after the purge", id)
		}
	}
	if bl.BlockedProjects["ph-doomed"] {
		t.Error("purge registered a project block it was not asked for")
	}
}

func TestPgStore_ListBlockedProjects_oneRowPerDirectory(t *testing.T) {
	// projects is keyed by (agent, project_hash), so one directory worked in by two
	// agents has two rows there. Joining blocked_projects to it drew a single block
	// twice -- and, where the two agents started in different directories of the same
	// repository, twice under two different names.
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

	// One repository, two agents, two start directories -- the shape that produced the
	// duplicate rows. github.com/org/repo is the placeholder the rest of this package
	// already uses; the case was found on real data, and carrying those names into a
	// fixture would publish them.
	if err := s.UpsertProject(ctx, "claude", "ph-shared", "widget",
		"", "github.com/org/repo", "repo", "widget/", ts); err != nil {
		t.Fatalf("UpsertProject claude: %v", err)
	}
	if err := s.UpsertProject(ctx, "codex", "ph-shared", "repo",
		"", "github.com/org/repo", "repo", "", ts); err != nil {
		t.Fatalf("UpsertProject codex: %v", err)
	}
	if err := s.BlockProject(ctx, "ph-shared", "", "admin@example.com", "personal"); err != nil {
		t.Fatalf("BlockProject: %v", err)
	}

	list, err := s.ListBlockedProjects(ctx)
	if err != nil {
		t.Fatalf("ListBlockedProjects: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d rows, want 1 — a block is one directory, however many agents "+
			"have worked in it: %+v", len(list), list)
	}
	// The repository name, not whichever start directory happened to be scanned last:
	// the block covers the repository, so the label has to come from that level.
	if list[0].ProjectName != "repo" {
		t.Errorf("ProjectName = %q, want %q", list[0].ProjectName, "repo")
	}
	if list[0].Subpaths != "widget/" {
		t.Errorf("Subpaths = %q, want %q — the merged identity is itself worth showing",
			list[0].Subpaths, "widget/")
	}
}

func TestPgStore_ListSessionOverviews_dropsDeletedSessionsWithEvents(t *testing.T) {
	// The list is a UNION of session records and events. Hiding a session only on the
	// records side let it walk back in through its own telemetry -- the delete returned
	// 200, the rows were tombstoned, and the row stayed on screen. Measured on dev
	// before this fix: visible_session_records 0, list API still returned it.
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := recentDay(42).Add(10 * time.Hour)
	seedTwoSessions(t, s, ts)

	inList := func() map[string]bool {
		t.Helper()
		rows, err := s.ListSessionOverviews(ctx, SessionOverviewFilter{Limit: 50})
		if err != nil {
			t.Fatalf("ListSessionOverviews: %v", err)
		}
		out := map[string]bool{}
		for _, r := range rows {
			out[r.SessionID] = true
		}
		return out
	}

	if before := inList(); !before["doomed"] {
		t.Fatal("fixture is wrong: the session is not in the list before the delete")
	}

	if _, err := s.DeleteSession(ctx, "doomed", "admin@example.com", "", false, false); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	after := inList()
	if after["doomed"] {
		t.Error("deleted session still in the list — its otel_events are still there " +
			"until the sweep, so the events arm has to refuse it too")
	}
	if !after["kept"] {
		t.Error("an untouched session went missing")
	}
}

// The backstop finds its work by reading swept_at, so a sweep that reclaimed rows
// but left the flag unset would make it repeat that session forever -- which is the
// state this change exists to remove.
func TestPgStore_SweepDeletedSessions_marksSwept(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	seedTwoSessions(t, s, recentDay(42).Add(10*time.Hour))

	if _, err := s.DeleteSession(ctx, "doomed", "admin@example.com", "", false, false); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if unswept := countUnsweptTombstones(t, s); unswept != 1 {
		t.Fatalf("a fresh tombstone should be unswept: got %d", unswept)
	}
	if _, err := s.SweepDeletedSessions(ctx, 25); err != nil {
		t.Fatalf("SweepDeletedSessions: %v", err)
	}
	if unswept := countUnsweptTombstones(t, s); unswept != 0 {
		t.Errorf("tombstone still unswept after the sweep: got %d", unswept)
	}
}

// The flag is only as true as whatever wrote it. This is the pass that asks the
// data instead, so a tombstone whose rows outlived its mark goes back in the queue.
func TestPgStore_VerifyDeletedSessions_requeuesTombstoneWithSurvivingRows(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := recentDay(42).Add(10 * time.Hour)
	seedTwoSessions(t, s, ts)

	if _, err := s.DeleteSession(ctx, "doomed", "admin@example.com", "", false, false); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	// Mark it swept without reclaiming anything -- the shape of a lost write, or of
	// rows that landed on another instance before its blocklist knew the tombstone.
	if _, err := s.pool.Exec(ctx,
		`UPDATE deleted_sessions SET swept_at = now() WHERE session_id = 'doomed'`); err != nil {
		t.Fatalf("mark swept: %v", err)
	}
	if n, err := s.SweepDeletedSessions(ctx, 25); err != nil || n != 0 {
		t.Fatalf("sweep should see no work while the flag is set: n=%d err=%v", n, err)
	}

	requeued, err := s.VerifyDeletedSessions(ctx)
	if err != nil {
		t.Fatalf("VerifyDeletedSessions: %v", err)
	}
	if requeued != 1 {
		t.Fatalf("verify should requeue the tombstone: got %d", requeued)
	}
	if _, err := s.SweepDeletedSessions(ctx, 25); err != nil {
		t.Fatalf("SweepDeletedSessions after verify: %v", err)
	}
	for _, table := range []string{"otel_events", "otel_metrics", "session_records"} {
		if n := countRows(t, s, table, "doomed"); n != 0 {
			t.Errorf("%s still holds %d rows after verify + sweep", table, n)
		}
	}

	// Nothing left to find: the expected steady state.
	if requeued, err := s.VerifyDeletedSessions(ctx); err != nil || requeued != 0 {
		t.Errorf("second verify: requeued=%d err=%v; want 0 and no error", requeued, err)
	}
}

func countUnsweptTombstones(t *testing.T, s *PgStore) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM deleted_sessions WHERE swept_at IS NULL`).Scan(&n); err != nil {
		t.Fatalf("count unswept tombstones: %v", err)
	}
	return n
}

// A session can be deleted, slip back in through a race with an in-flight sync,
// and be deleted again. The second delete has work to do, so the tombstone must
// stop looking swept -- otherwise the 60-second backstop, which finds its work by
// reading that column, skips the rows that just came back.
func seedStrictDeletionTargets(t *testing.T, s *PgStore, ts time.Time, sessionID string) {
	t.Helper()
	ctx := context.Background()
	var sourceID int64
	if err := s.pool.QueryRow(ctx, `SELECT id FROM session_records WHERE session_id=$1 LIMIT 1`, sessionID).Scan(&sourceID); err != nil {
		t.Fatalf("source record id: %v", err)
	}
	statements := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO plugin_invocation_facts (source_record_id, session_id, command_ts, command_name)
		  VALUES ($1,$2,$3,'private-command')`, []any{sourceID, sessionID, ts}},
		{`INSERT INTO task_segment_facts (boundary_record_id, session_id, start_ts)
		  VALUES ($1,$2,$3)`, []any{sourceID, sessionID, ts}},
		{`INSERT INTO claude_imputed_cost (srec_id, session_id, ts) VALUES ($1,$2,$3)`, []any{sourceID, sessionID, ts}},
		{`INSERT INTO codex_imputed_cost (srec_id, session_id, ts) VALUES ($1,$2,$3)`, []any{sourceID, sessionID, ts}},
		// Repair staging joined the target list in #396. This test asserts an
		// unrelated session survives in every target, so every target has to be
		// seeded or the assertion reads a never-written row as a deleted one.
		{`INSERT INTO login_email_history_repair_state (name, phase, max_record_id)
			VALUES ($1, 'source', 0) ON CONFLICT DO NOTHING`, []any{"r-" + sessionID}},
		{`INSERT INTO login_email_history_session_intervals
			(repair_name, session_id, interval_no, login_email, from_ts, first_interval)
			VALUES ($1, $2, 1, 'staged@example.com', $3, true)`, []any{"r-" + sessionID, sessionID, ts}},
		{`INSERT INTO codex_login_email_repair_state (name, phase, max_record_id)
			VALUES ($1, 'account', 0) ON CONFLICT DO NOTHING`, []any{"c-" + sessionID}},
		{`INSERT INTO codex_login_email_repair_candidates (repair_name, id, session_id, account_id, any_inferred)
			VALUES ($1, $2, $3, 'acc', false)`, []any{"c-" + sessionID, sourceID, sessionID}},
	}
	for _, stmt := range statements {
		if _, err := s.pool.Exec(ctx, stmt.q, stmt.args...); err != nil {
			t.Fatalf("seed strict target: %v", err)
		}
	}
	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{{
		BillingProvider: "codex", AccountID: "strict-" + sessionID, WindowKey: "5h",
		SampledAt: ts, SourceSessionID: sessionID,
	}}); err != nil {
		t.Fatalf("seed quota target: %v", err)
	}
}

func TestPgStore_StrictDeletionSweepsAuthoritativeTargetsAndIsolatesAccountData(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := recentDay(37).Add(10 * time.Hour)
	seedTwoSessions(t, s, ts)
	seedStrictDeletionTargets(t, s, ts, "doomed")
	seedStrictDeletionTargets(t, s, ts.Add(time.Millisecond), "kept")
	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{{
		BillingProvider: "codex", AccountID: "account-level", WindowKey: "5h",
		SampledAt: ts.Add(time.Second), SourceSessionID: "",
	}}); err != nil {
		t.Fatalf("seed account quota: %v", err)
	}

	if _, err := s.DeleteSession(ctx, "doomed", "admin@example.com", "strict", false, false); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := s.SweepDeletedSessions(ctx, 25); err != nil {
		t.Fatalf("SweepDeletedSessions: %v", err)
	}
	for _, target := range sessionDeletionTargets {
		var n int
		q := "SELECT count(*) FROM " + target.table + " WHERE " + target.key + "=$1"
		if err := s.pool.QueryRow(ctx, q, "doomed").Scan(&n); err != nil {
			t.Fatalf("count %s: %v", target.table, err)
		}
		if n != 0 {
			t.Errorf("%s retained %d strict target rows", target.table, n)
		}
		if err := s.pool.QueryRow(ctx, q, "kept").Scan(&n); err != nil {
			t.Fatalf("count kept %s: %v", target.table, err)
		}
		if n == 0 {
			t.Errorf("%s cleanup reached the unrelated kept session", target.table)
		}
	}
	var accountRows int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM quota_samples
		WHERE account_id='account-level' AND source_session_id=''`).Scan(&accountRows); err != nil {
		t.Fatalf("count account quota: %v", err)
	}
	if accountRows != 1 {
		t.Fatalf("unrelated account-level quota rows = %d, want 1", accountRows)
	}
}

func TestPgStore_PostSweepWritersCannotResurrectStrictTargets(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	clearLoginEmailHistoryBackfillMarker(t, s)
	ctx := context.Background()
	ts := recentDay(37).Add(10*time.Hour + 30*time.Minute)
	seedTwoSessions(t, s, ts)
	if _, err := s.DeleteSession(ctx, "doomed", "admin@example.com", "", false, false); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := s.SweepDeletedSessions(ctx, 1); err != nil {
		t.Fatalf("initial sweep: %v", err)
	}
	if err := s.InsertEvents(ctx, []*OtelEvent{{Ts: ts.Add(time.Hour), EventName: "api_request", SessionID: "doomed"}}); err != nil {
		t.Fatalf("guarded event writer: %v", err)
	}
	if err := s.InsertMetrics(ctx, []*OtelMetric{{Ts: ts.Add(time.Hour), MetricName: "late", SessionID: "doomed"}}); err != nil {
		t.Fatalf("guarded metric writer: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{Ts: ts.Add(time.Hour), SessionID: "doomed",
		RecordType: "user", UUID: "late", Raw: json.RawMessage(`{"message":{"content":"late"}}`)}}); err != nil {
		t.Fatalf("guarded fact source writer: %v", err)
	}
	if n, err := s.InsertQuotaSamples(ctx, []*QuotaSample{{BillingProvider: "codex", AccountID: "late",
		WindowKey: "5h", SampledAt: ts.Add(time.Hour), SourceSessionID: "doomed"}}); err != nil || n != 0 {
		t.Fatalf("guarded quota writer = (%d,%v), want dropped", n, err)
	}
	if err := s.RefreshClaudeImputedCost(ctx); err != nil {
		t.Fatalf("claude imputed refresh: %v", err)
	}
	if err := s.RefreshCodexImputedCost(ctx); err != nil {
		t.Fatalf("codex imputed refresh: %v", err)
	}
	if _, _, err := s.BackfillSessionRecordLoginEmailHistoryOnce(ctx); err != nil {
		t.Fatalf("history repair after deletion: %v", err)
	}
	for _, target := range sessionDeletionTargets {
		var n int
		if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM "+target.table+" WHERE "+target.key+"=$1", "doomed").Scan(&n); err != nil {
			t.Fatalf("count %s: %v", target.table, err)
		}
		if n != 0 {
			t.Errorf("%s writer resurrected %d rows", target.table, n)
		}
	}
	var staged int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM login_email_history_session_intervals WHERE session_id='doomed'`).Scan(&staged); err != nil {
		t.Fatalf("count history staging: %v", err)
	}
	if staged != 0 {
		t.Fatalf("history repair resurrected deleted staging: %d", staged)
	}
}

func TestPgStore_ConcurrentSweepClaimsOnceAndWriterCannotResurrect(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := recentDay(37).Add(11 * time.Hour)
	seedTwoSessions(t, s, ts)
	if _, err := s.DeleteSession(ctx, "doomed", "admin@example.com", "", false, false); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	claim, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin claim: %v", err)
	}
	if err := lockSessionDeletionTargets(ctx, claim, []string{"doomed"}); err != nil {
		t.Fatalf("hold claim: %v", err)
	}

	writerStarted := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			writerDone <- err
			return
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
			writerDone <- err
			return
		}
		close(writerStarted)
		live, err := liveSessionIDs(ctx, tx, []string{"doomed"})
		if err == nil && live["doomed"] {
			_, err = tx.Exec(ctx, `INSERT INTO otel_metrics (ts, metric_name, session_id)
				VALUES ($1,'race.metric','doomed')`, ts.Add(time.Minute))
		}
		if err == nil {
			err = tx.Commit(ctx)
		}
		writerDone <- err
	}()
	<-writerStarted

	results := make(chan struct {
		n   int64
		err error
	}, 2)
	for range 2 {
		go func() {
			n, err := s.SweepDeletedSessions(ctx, 1)
			results <- struct {
				n   int64
				err error
			}{n, err}
		}()
	}
	if err := claim.Commit(ctx); err != nil {
		t.Fatalf("release claim: %v", err)
	}
	if err := <-writerDone; err != nil {
		t.Fatalf("guarded writer: %v", err)
	}
	var reclaimed int64
	for range 2 {
		r := <-results
		if r.err != nil {
			t.Fatalf("concurrent sweep: %v", r.err)
		}
		reclaimed += r.n
	}
	if reclaimed != 3 {
		t.Fatalf("concurrent sweeps reclaimed %d rows, want the original three exactly once", reclaimed)
	}
	if n := countRows(t, s, "otel_metrics", "doomed"); n != 0 {
		t.Fatalf("writer resurrected %d metrics after sweep", n)
	}
}

func TestPgStore_DeleteCancelsHistoryStagingAndRestartExcludesTombstone(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	clearLoginEmailHistoryBackfillMarker(t, s)
	ctx := context.Background()
	ts := recentDay(37).Add(12 * time.Hour)
	seedTwoSessions(t, s, ts)
	if err := s.InsertEvents(ctx, []*OtelEvent{{Ts: ts.Add(time.Second), EventName: "api_request",
		SessionID: "doomed", UserID: "u-1", LoginEmail: "private@example.com"}}); err != nil {
		t.Fatalf("seed staged history source: %v", err)
	}
	if _, err := ensureHistorySnapshotForTest(ctx, s); err != nil {
		t.Fatalf("create history staging: %v", err)
	}
	if _, err := s.DeleteSession(ctx, "doomed", "admin@example.com", "", false, false); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	var staged int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM login_email_history_repair_state`).Scan(&staged); err != nil {
		t.Fatalf("count canceled staging: %v", err)
	}
	if staged != 0 {
		t.Fatalf("history repair state survived deletion: %d", staged)
	}
	if _, err := ensureHistorySnapshotForTest(ctx, s); err != nil {
		t.Fatalf("restart history staging: %v", err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM login_email_history_session_intervals
		WHERE session_id='doomed'`).Scan(&staged); err != nil {
		t.Fatalf("count restarted staging: %v", err)
	}
	if staged != 0 {
		t.Fatalf("restarted history staging retained deleted session: %d", staged)
	}
}

func ensureHistorySnapshotForTest(ctx context.Context, s *PgStore) (bool, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Release()
	if err := lockLoginEmailHistoryRepair(ctx, conn); err != nil {
		return false, err
	}
	defer unlockLoginEmailHistoryRepair(conn)
	return ensureLoginEmailHistorySnapshot(ctx, conn)
}

func TestPgStore_VerifyDeletedSessionsDrainsMultipleZeroSurvivorBatches(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `INSERT INTO deleted_sessions
		(session_id, deleted_by, reason, swept_at)
		SELECT 'verify-drain-' || lpad(n::text, 3, '0'), 'test', 'clean', now()
		FROM generate_series(0, 204) n`); err != nil {
		t.Fatalf("seed swept tombstones: %v", err)
	}

	if requeued, err := s.VerifyDeletedSessions(ctx); err != nil || requeued != 0 {
		t.Fatalf("VerifyDeletedSessions = (%d,%v), want drained clean backlog with zero requeues", requeued, err)
	}
	var verified, unverified int
	if err := s.pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE verified_at IS NOT NULL),
		count(*) FILTER (WHERE verified_at IS NULL)
		FROM deleted_sessions WHERE session_id LIKE 'verify-drain-%'`).Scan(&verified, &unverified); err != nil {
		t.Fatalf("count verification cursor: %v", err)
	}
	if verified != 205 || unverified != 0 {
		t.Fatalf("verification cursor covered %d and missed %d tombstones, want 205/0", verified, unverified)
	}
}

func TestPgStore_VerifyDeletedSessionsHonorsCanceledContext(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.VerifyDeletedSessions(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("VerifyDeletedSessions canceled error = %v, want context.Canceled", err)
	}
}

func TestPgStore_VerifierUsesEveryStrictTarget(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := recentDay(37).Add(13 * time.Hour)
	seedTwoSessions(t, s, ts)
	if _, err := s.DeleteSession(ctx, "doomed", "admin@example.com", "", false, false); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := s.SweepDeletedSessions(ctx, 1); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO quota_samples
		(billing_provider,account_id,window_key,sampled_at,source_session_id)
		VALUES ('codex','late','5h',$1,'doomed')`, ts.Add(time.Hour)); err != nil {
		t.Fatalf("seed late strict target: %v", err)
	}
	if n, err := s.VerifyDeletedSessions(ctx); err != nil || n != 1 {
		t.Fatalf("verify = (%d,%v), want one requeued quota survivor", n, err)
	}
	if _, err := s.SweepDeletedSessions(ctx, 1); err != nil {
		t.Fatalf("resweep: %v", err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM quota_samples WHERE source_session_id='doomed'`).Scan(&n); err != nil {
		t.Fatalf("count quota survivor: %v", err)
	}
	if n != 0 {
		t.Fatalf("quota survivor remained after verifier + sweep: %d", n)
	}
}

func TestPgStore_DeleteSession_reDeleteClearsSweptAt(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := recentDay(42).Add(10 * time.Hour)
	seedTwoSessions(t, s, ts)

	if _, err := s.DeleteSession(ctx, "doomed", "admin@example.com", "", false, false); err != nil {
		t.Fatalf("first DeleteSession: %v", err)
	}
	if _, err := s.SweepDeletedSessions(ctx, 25); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if unswept := countUnsweptTombstones(t, s); unswept != 0 {
		t.Fatalf("tombstone should be swept after the first pass: got %d", unswept)
	}

	// The rows come back: a sync that was already in flight when the delete landed.
	seedTwoSessions(t, s, ts.Add(time.Hour))

	if _, err := s.DeleteSession(ctx, "doomed", "admin@example.com", "", false, false); err != nil {
		t.Fatalf("second DeleteSession: %v", err)
	}
	if unswept := countUnsweptTombstones(t, s); unswept != 1 {
		t.Fatalf("re-delete must clear swept_at: got %d unswept", unswept)
	}

	// The next ordinary sweep reclaims them -- without waiting for the daily verify.
	if _, err := s.SweepDeletedSessions(ctx, 25); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	for _, table := range []string{"otel_events", "otel_metrics", "session_records"} {
		if n := countRows(t, s, table, "doomed"); n != 0 {
			t.Errorf("%s still holds %d rows after the re-delete sweep", table, n)
		}
	}
}

// A shared project must not make a plain owner deletion affect another owner.
func TestPgStore_DeleteSession_preservesOtherOwnerInSameProject(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	if err := s.UpsertProject(ctx, "claude", "ph-shared", "shared", "", "", "", "", ts); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "sess-a", RecordType: "user", ProfileEmail: "a@example.com", UserID: "u-a", ProjectHash: "ph-shared", UUID: "shared-a", Raw: json.RawMessage(`{"text":"A"}`)},
		{Ts: ts, SessionID: "sess-b", RecordType: "user", ProfileEmail: "b@example.com", UserID: "u-b", ProjectHash: "ph-shared", UUID: "shared-b", Raw: json.RawMessage(`{"text":"B"}`)},
	}); err != nil {
		t.Fatal(err)
	}
	if countVisible(t, s, "sess-a") != 1 || countVisible(t, s, "sess-b") != 1 {
		t.Fatal("both owners must initially be visible")
	}
	result, err := s.DeleteSession(ctx, "sess-a", "a@example.com", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.MarkedSessions != 1 || result.PurgedSessions != 0 || result.ProjectBlocked {
		t.Fatalf("unexpected result: %+v", result)
	}
	if countVisible(t, s, "sess-a") != 0 || countRows(t, s, "deleted_sessions", "sess-a") != 1 {
		t.Error("A must be tombstoned and hidden")
	}
	if countRows(t, s, "session_records", "sess-b") != 1 || countVisible(t, s, "sess-b") != 1 || countRows(t, s, "deleted_sessions", "sess-b") != 0 {
		t.Error("B must remain stored, visible and without a tombstone")
	}
	var blocked int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM blocked_projects").Scan(&blocked); err != nil {
		t.Fatal(err)
	}
	if blocked != 0 {
		t.Errorf("blocked projects = %d, want 0", blocked)
	}
}
