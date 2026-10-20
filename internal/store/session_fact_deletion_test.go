package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// seedCopyMarker writes a copy marker directly -- the copy judgement that writes
// them comes in a later pass: line srecID of sessionID is a copy of a line that
// original keeps.
func seedCopyMarker(t *testing.T, s *PgStore, srecID int64, sessionID, original string) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(), `INSERT INTO session_record_copies
		(srec_id, session_id, original_session_id, record_type, ts) VALUES ($1, $2, $3, 'assistant', now())`,
		srecID, sessionID, original); err != nil {
		t.Fatalf("seed copy marker %d: %v", srecID, err)
	}
}

// markersNaming counts the copy markers that name a session on either side.
func markersNaming(t *testing.T, s *PgStore, sessionID string) int {
	t.Helper()
	return countWhere(t, s, "session_record_copies", "session_id = $1 OR original_session_id = $1", sessionID)
}

// factsOf counts a session's rows in the three tables the worker fills.
func factsOf(t *testing.T, s *PgStore, sessionID string) int {
	t.Helper()
	n := 0
	for _, table := range []string{"session_analysis_facts", "session_turn_facts", "session_activity_segments"} {
		n += countWhere(t, s, table, "session_id = $1", sessionID)
	}
	return n
}

// seedCopyNeighbours seeds, around a session d a deletion is about to take, the
// facts of three other sessions and the copy markers between them:
//
//   - copy-of-d holds a copy of a line of d's: d is its original;
//   - d holds a copy of a line of orig-of-d's, the marker on line dLine;
//   - bystander holds a copy of a line elsewhere keeps, which no deletion here
//     touches.
//
// copy-of-d and bystander have a record each, backed by an OTEL event so no
// orphan cleanup takes them, under an identity no deletion here names; their
// markers are on those records. dLine is d's own line, which the caller seeds or
// leaves dangling.
func seedCopyNeighbours(t *testing.T, s *PgStore, d string, dLine int64) {
	t.Helper()
	ctx := context.Background()
	at := time.Now().Add(-time.Hour)
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: at, EventName: "api_request", SessionID: "copy-of-d", ProfileEmail: "other@example.com", UserID: "u-other"},
		{Ts: at, EventName: "api_request", SessionID: "bystander", ProfileEmail: "other@example.com", UserID: "u-other"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: at, SessionID: "copy-of-d", RecordType: "assistant", UUID: "cp1", ProfileEmail: "other@example.com", UserID: "u-other", Raw: json.RawMessage(`{}`)},
		{Ts: at, SessionID: "bystander", RecordType: "assistant", UUID: "by1", ProfileEmail: "other@example.com", UserID: "u-other", Raw: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"copy-of-d", "orig-of-d", "bystander"} {
		seedFacts(t, s, id, factsRow{})
	}
	seedCopyMarker(t, s, minRecordID(t, s, "copy-of-d"), "copy-of-d", d)
	seedCopyMarker(t, s, dLine, d, "orig-of-d")
	seedCopyMarker(t, s, minRecordID(t, s, "bystander"), "bystander", "elsewhere")
}

// rollsBackWhole runs a writer or a deletion path with its queue write made to
// fail and checks it left nothing behind: what it writes or deletes commits with
// its queue rows, and with the steps of the deletion contract, or not at all
// (p1b-design §5.2, §6.1).
func rollsBackWhole(t *testing.T, s *PgStore, run func() error) {
	t.Helper()
	ctx := context.Background()
	tables := []string{"session_records", "otel_events", "session_snapshots", "deleted_sessions",
		"excluded_sessions", "excluded_accounts", "session_record_copies", "session_commit_facts", "session_task_facts", "session_analysis_facts",
		"session_turn_facts", "session_activity_segments", "session_fact_dirty", "session_overview_retention_state"}
	counts := func() map[string]int {
		out := map[string]int{}
		for _, table := range tables {
			out[table] = countWhere(t, s, table, "true")
		}
		return out
	}
	before := counts()
	for _, stmt := range []string{
		`CREATE OR REPLACE FUNCTION test_fail_queue() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'queue write refused'; END $$`,
		`CREATE TRIGGER test_fail_queue BEFORE INSERT OR UPDATE ON session_fact_dirty
			FOR EACH STATEMENT EXECUTE FUNCTION test_fail_queue()`,
	} {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	err := run()
	_, _ = s.pool.Exec(ctx, `DROP TRIGGER IF EXISTS test_fail_queue ON session_fact_dirty`)
	_, _ = s.pool.Exec(ctx, `DROP FUNCTION IF EXISTS test_fail_queue()`)
	if err == nil {
		t.Fatal("the path committed though its queue write failed")
	}
	for table, n := range counts() {
		if n != before[table] {
			t.Errorf("%s holds %d row(s) after the failed path, %d before: part of it committed", table, n, before[table])
		}
	}
}

// expectContract checks, after a deletion path took d, what p1b-design §6.1
// leaves: with steps 1 and 2 no marker names d and both neighbours are queued as
// peers, without them both markers stand and no neighbour is queued; d's facts
// are gone unless the path leaves them to the sweep; the bystander keeps its
// marker and facts. removals are the sessions queued as a removal under reason.
func expectContract(t *testing.T, s *PgStore, d string, markers, factsGone bool, reason int32, removals ...string) {
	t.Helper()
	if n := markersNaming(t, s, d); markers && n != 0 {
		t.Errorf("%d copy marker(s) still name %s", n, d)
	} else if !markers && n != 2 {
		t.Errorf("%d copy marker(s) name %s, want both kept: no line went", n, d)
	}
	if n := markersNaming(t, s, "bystander"); n != 1 {
		t.Errorf("the bystander's marker count is %d, want 1", n)
	}
	if n := factsOf(t, s, d); factsGone && n != 0 {
		t.Errorf("%d fact row(s) of %s survived", n, d)
	} else if !factsGone && n != 3 {
		t.Errorf("%s has %d fact row(s), want its 3 left to the sweep", d, n)
	}
	for _, id := range []string{"copy-of-d", "orig-of-d", "bystander"} {
		if n := factsOf(t, s, id); n != 3 {
			t.Errorf("%s has %d fact row(s), want 3 untouched", id, n)
		}
	}
	want := map[string]queued{}
	for _, id := range removals {
		want[id] = queued{reasons: reason, removal: true}
	}
	if markers {
		want["copy-of-d"] = queued{reasons: 256, scanFrom: int64Ptr(0)}
		want["orig-of-d"] = queued{reasons: 256, scanFrom: int64Ptr(0)}
	}
	expectQueued(t, s, want)
}

// The deletion contract (p1b-design §6.1) on each path of §6.2, over facts and
// copy markers written directly: the neighbours are found from the markers
// before they go (orig-of-d is reachable through d's own marker only), the
// markers naming the deleted session go in both directions, its facts go, and
// it and its neighbours are queued under the path's reason -- all in the
// deletion's own transaction, which a failed queue write rolls back whole.
//
// Mutation: drop a path's takeCopyMarkers, deleteSessionFacts or queue write,
// collect the neighbours after deleting the markers, or delete the snapshots
// outside the prune's transaction, and its subtest fails.
func TestSessionFactDeletionContract(t *testing.T) {
	s := acquireTestStore(t)
	guardQueueWriteLast(t, s)
	ctx := context.Background()
	at := time.Now().Add(-2 * time.Hour)
	victimRecords := func(t *testing.T, session string, rec SessionRecord) int64 {
		t.Helper()
		rec.Ts, rec.SessionID, rec.RecordType, rec.UUID, rec.Raw = at, session, "user", session+"-1", json.RawMessage(`{}`)
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{&rec}); err != nil {
			t.Fatal(err)
		}
		return minRecordID(t, s, session)
	}

	// 1, 2, 3 and 5; the facts wait for the sweep, and a tombstone is never queued.
	t.Run("tombstone", func(t *testing.T) {
		truncateTables(t, s)
		line := victimRecords(t, "d", SessionRecord{ProfileEmail: "victim@example.com"})
		seedFacts(t, s, "d", factsRow{})
		seedCopyNeighbours(t, s, "d", line)
		clearQueue(t, s)
		del := func() error {
			_, err := s.DeleteSession(ctx, "d", "admin@example.com", "privacy", false, false)
			return err
		}
		rollsBackWhole(t, s, del)
		if err := del(); err != nil {
			t.Fatal(err)
		}
		expectContract(t, s, "d", true, false, 0)
		if _, err := s.SweepDeletedSessions(ctx, 25); err != nil {
			t.Fatal(err)
		}
		if n := factsOf(t, s, "d"); n != 0 {
			t.Errorf("%d fact row(s) of d survived the sweep", n)
		}
	})

	// The tombstones of a multi-project delete are written project by project,
	// and its neighbours queued once, after all of them: orig-of-d, a neighbour of
	// the first project's d, belongs to the second project through its facts, and
	// a tombstoned session is never queued.
	t.Run("DeleteProject", func(t *testing.T) {
		truncateTables(t, s)
		line := victimRecords(t, "d", SessionRecord{ProfileEmail: "victim@example.com", ProjectHash: "-ph-first"})
		seedFacts(t, s, "d", factsRow{})
		seedCopyNeighbours(t, s, "d", line)
		if _, err := s.pool.Exec(ctx, `UPDATE session_analysis_facts SET project_hash = '-ph-second'
			WHERE session_id = 'orig-of-d'`); err != nil {
			t.Fatal(err)
		}
		clearQueue(t, s)
		del := func() error {
			_, err := s.DeleteProject(ctx, []string{"-ph-first", "-ph-second"}, "admin@example.com", "privacy", false)
			return err
		}
		rollsBackWhole(t, s, del)
		if err := del(); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"d", "orig-of-d"} {
			if n := markersNaming(t, s, id); n != 0 {
				t.Errorf("%d copy marker(s) still name %s", n, id)
			}
			if n := factsOf(t, s, id); n != 3 {
				t.Errorf("%s has %d fact row(s), want its 3 left to the sweep", id, n)
			}
		}
		expectQueued(t, s, map[string]queued{"copy-of-d": {reasons: 256, scanFrom: int64Ptr(0)}})
	})

	// The purge tombstones what a blocked project let through, under the same
	// contract.
	t.Run("PurgeBlockedProjects", func(t *testing.T) {
		truncateTables(t, s)
		if err := s.BlockProject(ctx, "-ph-blocked", "", "admin@example.com", "privacy"); err != nil {
			t.Fatal(err)
		}
		line := victimRecords(t, "d", SessionRecord{ProfileEmail: "victim@example.com", ProjectHash: "-ph-blocked"})
		seedFacts(t, s, "d", factsRow{})
		seedCopyNeighbours(t, s, "d", line)
		clearQueue(t, s)
		rollsBackWhole(t, s, func() error { _, err := s.PurgeBlockedProjects(ctx); return err })
		if n, err := s.PurgeBlockedProjects(ctx); err != nil || n != 1 {
			t.Fatalf("PurgeBlockedProjects = (%d, %v), want d tombstoned", n, err)
		}
		expectContract(t, s, "d", true, false, 0)
	})

	// The user's sessions include one reached only through the facts' identity
	// arrays: its records are gone, its facts still say what the user did.
	for _, mode := range []struct {
		name               string
		profile, userID    string
		rec                SessionRecord
		arraysOnly, others factsRow
	}{
		{name: "DeleteUserData by profile", profile: "victim@example.com",
			rec:        SessionRecord{ProfileEmail: "victim@example.com"},
			arraysOnly: factsRow{profileEmails: []string{"", "victim@example.com"}},
			others:     factsRow{profileEmails: []string{"other@example.com"}, userIDs: []string{"victim-uid"}}},
		{name: "DeleteUserData by user_id", userID: "victim-uid",
			rec:        SessionRecord{UserID: "victim-uid"},
			arraysOnly: factsRow{userIDs: []string{"victim-uid", "someone"}},
			others:     factsRow{userIDs: []string{"someone"}, profileEmails: []string{"victim@example.com"}}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			truncateTables(t, s)
			line := victimRecords(t, "d", mode.rec)
			seedFacts(t, s, "d", factsRow{})
			seedFacts(t, s, "arrays-only", mode.arraysOnly)
			// The other mode's identity: a profile delete does not search user_ids,
			// nor a user_id delete profile_emails.
			seedFacts(t, s, "other-identity", mode.others)
			seedCopyNeighbours(t, s, "d", line)
			clearQueue(t, s)
			del := func() error { return s.DeleteUserData(ctx, mode.profile, mode.userID) }
			rollsBackWhole(t, s, del)
			if err := del(); err != nil {
				t.Fatal(err)
			}
			expectContractWith(t, s, "d", "arrays-only", "other-identity")
		})
	}

	// 4 and 5: the rows change hands, no marker changes.
	t.Run("MergeUsers", func(t *testing.T) {
		truncateTables(t, s)
		line := victimRecords(t, "d", SessionRecord{ProfileEmail: "from@example.com"})
		seedFacts(t, s, "d", factsRow{})
		seedCopyNeighbours(t, s, "d", line)
		clearQueue(t, s)
		merge := func() error { return s.MergeUsers(ctx, "from@example.com", "", "to@example.com") }
		rollsBackWhole(t, s, merge)
		if err := merge(); err != nil {
			t.Fatal(err)
		}
		expectContract(t, s, "d", false, true, 4096, "d")
	})

	t.Run("CleanupOrphanSessionRecords", func(t *testing.T) {
		truncateTables(t, s)
		line := victimRecords(t, "d", SessionRecord{ProfileEmail: "victim@example.com"})
		seedFacts(t, s, "d", factsRow{})
		seedCopyNeighbours(t, s, "d", line)
		clearQueue(t, s)
		rollsBackWhole(t, s, func() error { _, err := s.CleanupOrphanSessionRecords(ctx); return err })
		if n, err := s.CleanupOrphanSessionRecords(ctx); err != nil || n != 1 {
			t.Fatalf("CleanupOrphanSessionRecords = (%d, %v), want d's one row", n, err)
		}
		expectContract(t, s, "d", true, true, 65536, "d")
	})

	// 4 and 5: the snapshot goes, the records and their markers stay.
	t.Run("PruneSessionObservations", func(t *testing.T) {
		truncateTables(t, s)
		t.Cleanup(func() { forceRetention(t, s, "session_records", nil) })
		forceRetention(t, s, "session_records", ptrInt(7))
		line := victimRecords(t, "d", SessionRecord{ProfileEmail: "victim@example.com"})
		ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{
			obsSnapshot("d", "victim@example.com", time.Now().Add(-10*24*time.Hour), "old title")}})
		seedFacts(t, s, "d", factsRow{})
		seedCopyNeighbours(t, s, "d", line)
		clearQueue(t, s)
		prune := func() error {
			_, err := s.PruneSessionObservations(ctx, time.Now().Add(-SessionObservationEventRetention))
			return err
		}
		rollsBackWhole(t, s, prune)
		if err := prune(); err != nil {
			t.Fatal(err)
		}
		if n := countWhere(t, s, "session_snapshots", "session_id = 'd'"); n != 0 {
			t.Fatalf("the old snapshot survived the prune")
		}
		expectContract(t, s, "d", false, true, 32768, "d")
	})
}

// expectContractWith is expectContract for DeleteUserData, which also takes the
// session reached through the identity arrays and leaves one whose arrays name
// the user under the other identity.
func expectContractWith(t *testing.T, s *PgStore, d, arraysOnly, otherIdentity string) {
	t.Helper()
	if n := factsOf(t, s, arraysOnly); n != 0 {
		t.Errorf("%d fact row(s) of %s, reached only through its identity arrays, survived", n, arraysOnly)
	}
	if n := factsOf(t, s, otherIdentity); n != 3 {
		t.Errorf("%s has %d fact row(s), want 3: its arrays name the user only under the other identity", otherIdentity, n)
	}
	expectContract(t, s, d, true, true, 8192, d, arraysOnly)
}

// The retention reconciliation (p1b-design §6.3) takes the facts that read a row
// older than the session_records run's own cutoff -- with a 365-day session
// retention and the 90-day OTEL one, a fact 100 days old stays though the OTEL
// cutoff is later -- applies the deletion contract to them, finding the
// neighbours through markers that may already dangle, then deletes the markers
// left dangling. For the OTEL run it queues, not as a removal, the OTEL-costed
// sessions whose activity began before the oldest OTEL row left.
//
// Mutation: select by the latest cutoff of all runs and d100 goes too; delete the
// dangling markers before collecting the neighbours and orig-of-d is never
// queued; drop the OTEL branch and otel-old is not queued.
func TestRetentionReconciliationTakesSessionFacts(t *testing.T) {
	s := acquireTestStore(t)
	guardQueueWriteLast(t, s)
	truncateTables(t, s)
	ctx := context.Background()
	t.Cleanup(func() { forceRetention(t, s, "session_records", nil) })
	forceRetention(t, s, "session_records", ptrInt(365))
	forceRetention(t, s, "otel_events", ptrInt(90))
	days := func(n int) *time.Time { v := time.Now().AddDate(0, 0, -n); return &v }

	// d read a row 400 days old; its own line is one retention already dropped,
	// so its marker dangles.
	seedFacts(t, s, "d", factsRow{firstRecordAt: days(400)})
	seedFacts(t, s, "d100", factsRow{firstRecordAt: days(100)})
	seedCopyNeighbours(t, s, "d", 999999999)
	// A marker on a dropped line of a session no run selects still invalidates
	// the copy session whose facts used that marker.
	seedCopyMarker(t, s, 999999998, "dangling", "elsewhere")
	// The oldest OTEL row left is 30 days old.
	if err := s.InsertEvents(ctx, []*OtelEvent{{Ts: *days(30), EventName: "api_request", SessionID: "otel-anchor"}}); err != nil {
		t.Fatal(err)
	}
	seedFacts(t, s, "otel-old", factsRow{costSource: "otel_partial", firstActivityAt: days(40), firstRecordAt: days(40)})
	seedFacts(t, s, "otel-new", factsRow{costSource: "otel", firstActivityAt: days(20), firstRecordAt: days(20)})
	seedFacts(t, s, "estimated", factsRow{costSource: "estimate", firstActivityAt: days(40), firstRecordAt: days(40)})
	clearQueue(t, s)

	reconcile := func() error { _, err := s.ReconcileSessionOverviewRollupsForRetention(ctx); return err }
	rollsBackWhole(t, s, reconcile)
	if err := reconcile(); err != nil {
		t.Fatal(err)
	}
	if n := countWhere(t, s, "session_record_copies", "session_id = 'dangling'"); n != 0 {
		t.Errorf("a dangling marker survived the reconciliation")
	}
	if row := queueRows(t, s)["dangling"]; row.reasons != 256 || row.removalGeneration != 0 {
		t.Errorf("dangling copy marker removal was not queued for its session: %+v", row)
	}
	var copyRevision int64
	if err := s.pool.QueryRow(ctx, `SELECT revision FROM session_timeline_revisions WHERE session_id='dangling'`).Scan(&copyRevision); err != nil || copyRevision == 0 {
		t.Errorf("dangling copy marker did not advance its source witness: revision=%d err=%v", copyRevision, err)
	}
	clearQueueOf(t, s, "dangling")
	if n := factsOf(t, s, "d100"); n != 3 {
		t.Errorf("d100 has %d fact row(s), want 3: its rows are inside the 365-day session retention", n)
	}
	for _, id := range []string{"otel-old", "otel-new", "estimated"} {
		if n := factsOf(t, s, id); n != 3 {
			t.Errorf("%s has %d fact row(s), want 3: an OTEL run takes no fact", id, n)
		}
	}
	// expectContract compares the whole queue; the OTEL-costed session is the one
	// extra row, queued as an OTEL change rather than a removal.
	otel := queueRows(t, s)["otel-old"]
	if otel.reasons != 4 || otel.removalGeneration != 0 {
		t.Errorf("otel-old queued %+v, want an OTEL bump, not a removal", otel)
	}
	clearQueueOf(t, s, "otel-old")
	expectContract(t, s, "d", true, true, 128, "d")
}

// Every completed retention run restarts the clock of the queue rows older than
// the latest cutoff of the completed runs, the shortest retention it enforces
// (p1b-design §6.3): it bumps them under the clock restart, which sets
// first_dirty_at and not_before to now. A bulk row stays bulk and a live one
// live, no removal is recorded, and their facts stay. An OTEL run alone does it
// too -- production runs no session_records retention. A stale row whose facts
// a session_records run takes is a removal besides. The run's other steps
// follow which runs completed: orphan copy markers go only with a
// session_records run, and with otel_events emptied every OTEL-costed session
// is queued.
//
// Mutation: drop the stale rows from the queue write, queue them under
// retention, give the clock restart the live priority, read them only on a
// session_records run or delete their facts, and a check below fails; so does
// running the orphan-marker DELETE on every run or on none, or comparing with no
// COALESCE to infinity. Restart them with an UPDATE of the run's own and the
// checks fail only for what it leaves unwritten -- the reason bit, the new
// generation. One that wrote every column the queue write does would pass: the
// order rows are locked in shows only under concurrency, and is tested for the
// queue write alone (TestMarkSessionFactsDirty_overlappingWritersDoNotDeadlock).
func TestRetentionReconciliationRestartsStaleQueueRows(t *testing.T) {
	s := acquireTestStore(t)
	guardQueueWriteLast(t, s)
	ctx := context.Background()
	t.Cleanup(func() { forceRetention(t, s, "session_records", nil) })
	days := func(n int) *time.Time { v := time.Now().AddDate(0, 0, -n); return &v }
	for _, runs := range []struct {
		name       string
		records    *int
		recordsRun bool
	}{
		{"an OTEL run alone", nil, false},
		{"session_records and OTEL runs", ptrInt(365), true},
	} {
		t.Run(runs.name, func(t *testing.T) {
			truncateTables(t, s)
			forceRetention(t, s, "session_records", runs.records)
			forceRetention(t, s, "otel_events", ptrInt(90))
			// Three rows queued 100 days ago, older than the 90-day OTEL cutoff, the
			// latest of the runs: a bulk one, as the 30-minute imputed rewrite queues
			// it; a live one that once lost source rows, its removal_generation
			// behind its generation; and a bulk one whose facts read a row 400 days
			// old, outside the 365-day session retention. fresh was queued 10 days
			// ago, inside the cutoff.
			markInTx(t, s, []string{"stale-bulk", "stale-old", "fresh"}, nil, dirtyImputed)
			markInTx(t, s, []string{"stale-live"}, nil, dirtyDeletionUser)
			markInTx(t, s, []string{"stale-live"}, nil, dirtyOtel)
			if _, err := s.pool.Exec(ctx, `UPDATE session_fact_dirty SET first_dirty_at = now() - CASE session_id
				WHEN 'fresh' THEN interval '10 days' ELSE interval '100 days' END`); err != nil {
				t.Fatal(err)
			}
			was := queueRows(t, s)
			if was["stale-bulk"].priority != 1 || was["stale-live"].priority != 0 || was["stale-live"].removalGeneration == 0 {
				t.Fatalf("stale-bulk = %+v, stale-live = %+v, want a bulk row and a live one with a removal recorded",
					was["stale-bulk"], was["stale-live"])
			}
			// The facts of the first two read rows 100 days old, inside the 365-day
			// session retention.
			seedFacts(t, s, "stale-bulk", factsRow{firstRecordAt: days(100)})
			seedFacts(t, s, "stale-live", factsRow{firstRecordAt: days(100)})
			seedFacts(t, s, "stale-old", factsRow{firstRecordAt: days(400)})
			// otel_events is empty: no OTEL row is left, and every OTEL-costed
			// session lost its cost rows.
			seedFacts(t, s, "otel-costed", factsRow{costSource: "otel", firstActivityAt: days(1), firstRecordAt: days(1)})
			seedFacts(t, s, "estimated", factsRow{costSource: "estimate", firstActivityAt: days(1), firstRecordAt: days(1)})
			// A marker on a line that is gone.
			seedCopyMarker(t, s, 999999990, "dangling", "elsewhere")

			if _, err := s.ReconcileSessionOverviewRollupsForRetention(ctx); err != nil {
				t.Fatal(err)
			}

			// What tells the queue write from an UPDATE of the run's own is the
			// reason bit and the new generation, not the order the rows were locked in.
			queued := queueRows(t, s)
			for _, id := range []string{"stale-bulk", "stale-live"} {
				r, w := queued[id], was[id]
				if !clockRestarted(t, s, id) || r.generation <= w.generation || r.reasons != w.reasons|131072 {
					t.Errorf("%s = %+v, was %+v: want its clock restarted by a queue write that adds the clock restart alone", id, r, w)
				}
				if r.priority != w.priority || r.removalGeneration != w.removalGeneration {
					t.Errorf("%s priority=%d removal_generation=%d, want them kept (%d, %d): a restart raises no priority and records no removal",
						id, r.priority, r.removalGeneration, w.priority, w.removalGeneration)
				}
				if n := factsOf(t, s, id); n != 3 {
					t.Errorf("%s has %d fact row(s), want 3: a restart deletes nothing", id, n)
				}
			}
			// stale-old is a removal besides, and live as every removal is, only where
			// a session_records run took its facts.
			old, oldWas := queued["stale-old"], was["stale-old"]
			wantReasons, wantPriority, wantFacts := oldWas.reasons|131072, int16(1), 3
			if runs.recordsRun {
				wantReasons, wantPriority, wantFacts = wantReasons|128, 0, 0
			}
			removal, facts := old.removalGeneration > oldWas.generation, factsOf(t, s, "stale-old")
			if !clockRestarted(t, s, "stale-old") || old.reasons != wantReasons || old.priority != wantPriority ||
				removal != runs.recordsRun || facts != wantFacts {
				t.Errorf("stale-old = %+v with %d fact row(s), was %+v: want its clock restarted, reasons %d, priority %d, %d fact row(s), a removal recorded: %v",
					old, facts, oldWas, wantReasons, wantPriority, wantFacts, runs.recordsRun)
			}
			if fresh := queued["fresh"]; !fresh.firstDirtyAt.Equal(was["fresh"].firstDirtyAt) || fresh.generation != was["fresh"].generation {
				t.Errorf("fresh = %+v, was %+v: a row inside the cutoff keeps its clock and generation", fresh, was["fresh"])
			}
			if q, ok := queued["otel-costed"]; !ok || q.reasons != 4 {
				t.Errorf("otel-costed queued %v with %+v, want an OTEL bump: no OTEL row is left", ok, q)
			}
			if _, ok := queued["estimated"]; ok {
				t.Error("an estimated session was queued by the OTEL run")
			}
			n := markersNaming(t, s, "dangling")
			if runs.recordsRun && n != 0 {
				t.Errorf("the dangling marker survived a session_records run")
			}
			if !runs.recordsRun && n != 1 {
				t.Errorf("an OTEL run alone deleted the dangling marker; only a session_records run drops a record")
			}
		})
	}
}

func clearQueueOf(t *testing.T, s *PgStore, sessionID string) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(), `DELETE FROM session_fact_dirty WHERE session_id = $1`, sessionID); err != nil {
		t.Fatal(err)
	}
}

// The orphan cleanup and the snapshot prune take the retention lock before the
// source lock, as the retention reconciliation does (p1b-design §5.4): while one
// waits for the retention lock it holds no source lock, so it cannot sit in the
// source lock's queue ahead of a DeleteUserData that the holder of the
// retention lock is waiting behind.
//
// Mutation: take either path's source lock first and the waiter holds it.
func TestRetentionLockIsTakenBeforeTheSourceLock(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	for _, path := range []struct {
		name string
		seed func(t *testing.T)
		run  func() error
	}{
		{"CleanupOrphanSessionRecords", func(t *testing.T) {
			if err := s.InsertSessionRecords(ctx, []*SessionRecord{{Ts: time.Now(), SessionID: "orphan",
				RecordType: "user", UUID: "o1", Raw: json.RawMessage(`{}`)}}); err != nil {
				t.Fatal(err)
			}
		}, func() error { _, err := s.CleanupOrphanSessionRecords(ctx); return err }},
		{"PruneSessionObservations", func(t *testing.T) {
			t.Cleanup(func() { forceRetention(t, s, "session_records", nil) })
			forceRetention(t, s, "session_records", ptrInt(7))
			ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{
				obsSnapshot("stale", "p@example.com", time.Now().Add(-10*24*time.Hour), "x")}})
		}, func() error {
			_, err := s.PruneSessionObservations(ctx, time.Now().Add(-SessionObservationEventRetention))
			return err
		}},
	} {
		t.Run(path.name, func(t *testing.T) {
			truncateTables(t, s)
			path.seed(t)
			holder, err := s.pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Release()
			if _, err := holder.Exec(ctx, `SELECT pg_advisory_lock(hashtext('session_overview_retention'))`); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- path.run() }()

			const retentionKey = `((classid::bigint << 32) | objid::bigint) = hashtext('session_overview_retention')::bigint
				AND objsubid = 1`
			var waiter int
			deadline := time.Now().Add(10 * time.Second)
			for waiter == 0 {
				if err := s.pool.QueryRow(ctx, `SELECT COALESCE(max(pid), 0) FROM pg_locks
					WHERE locktype = 'advisory' AND NOT granted AND `+retentionKey).Scan(&waiter); err != nil {
					t.Fatal(err)
				}
				if waiter == 0 {
					if time.Now().After(deadline) {
						t.Fatal("the path never waited for the retention lock")
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			var holdsSource bool
			if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid = $1
				AND locktype = 'advisory' AND granted AND classid = hashtext('session_overview_source')::oid
				AND objid = 0 AND objsubid = 2)`, waiter).Scan(&holdsSource); err != nil {
				t.Fatal(err)
			}
			if holdsSource {
				t.Error("the path holds the source lock while it waits for the retention lock")
			}
			if _, err := holder.Exec(ctx, `SELECT pg_advisory_unlock(hashtext('session_overview_retention'))`); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatalf("%s: %v", path.name, err)
			}
		})
	}
}

// DeleteUserData finds a user's sessions through the facts' user_ids by
// containment, which the GIN index on user_ids serves; $1 = ANY(user_ids) reads
// the same rows and no index can serve it. With sequential scans priced out, the
// plan of the lookup DeleteUserData runs has to use the index.
//
// Mutation: write the lookup as $1 = ANY(%s) and the plan stays a Seq Scan.
func TestDeleteUserDataFactsLookupUsesTheUserIDsIndex(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, `EXPLAIN (COSTS OFF) `+fmt.Sprintf(sessionFactsOfIdentitySQL, "user_ids"), "victim-uid")
	if err != nil {
		t.Fatalf("explain the facts lookup: %v", err)
	}
	plan, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("explain the facts lookup: %v", err)
	}
	if !strings.Contains(strings.Join(plan, "\n"), "Bitmap Index Scan on idx_session_analysis_user_ids") {
		t.Errorf("the user_ids lookup does not use its GIN index; plan:\n%s", strings.Join(plan, "\n"))
	}
}
