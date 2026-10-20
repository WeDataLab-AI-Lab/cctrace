package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// sessionAnalysisTables are the session analysis tables (p1b-design §2.3), named
// here rather than read from sessionDeletionTargets: a table dropped from that
// list has to fail these tests, not drop out of them.
var sessionAnalysisTables = []string{
	"session_analysis_facts", "session_turn_facts", "session_activity_segments", "session_commit_facts", "session_task_facts",
	"session_fact_dirty", "session_record_copies",
}

// seedSessionAnalysisRows writes one row of a session into each session analysis
// table, directly rather than through the worker that computes them, so the
// deletion paths are tested on their own. srecID keys the copy marker and has to
// differ between sessions.
func seedSessionAnalysisRows(t *testing.T, s *PgStore, sessionID, projectHash string, srecID int64) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
	for _, stmt := range []struct {
		q    string
		args []any
	}{
		{`INSERT INTO session_analysis_facts (session_id, agent, coverage, project_hash, transcript,
			title_source, title_state, turn_boundaries, cost_source, output_hash, source_generation, rules_version)
			VALUES ($1, 'claude', 'full', $2, true, 'none', 'none', 'observed', 'none', '', 1, 1)`,
			[]any{sessionID, projectHash}},
		{`INSERT INTO session_turn_facts (session_id, turn_key, agent, ordinal, branch, started_at, ended_at,
			input_kind, is_request, status, preview_state, cost_source, context_source, rules_version)
			VALUES ($1, 'turn-1', 'claude', 1, 'active', $2, $2, 'typed', true, 'ended', 'none', 'none', 'unknown', 1)`,
			[]any{sessionID, at}},
		{`INSERT INTO session_activity_segments (session_id, seg_start, seg_end, agent, records, human_inputs, rules_version)
			VALUES ($1, $2, $2, 'claude', 1, 1, 1)`, []any{sessionID, at}},
		{`INSERT INTO session_commit_facts(session_id,ordinal,subject_state,committed_at,kind,source,rules_version)
			VALUES($1,1,'none',$2,'commit','call',1)`, []any{sessionID, at}},
		{`INSERT INTO session_task_facts(session_id,item_key,kind,display_id,title_state,first_ts,rules_version)
			VALUES($1,'task:1','task','1','none',$2,1)`, []any{sessionID, at}},
		// The writers that seeded the session's records have already queued it.
		{`INSERT INTO session_fact_dirty (session_id) VALUES ($1) ON CONFLICT DO NOTHING`, []any{sessionID}},
		// The original is a session no test deletes. A marker whose original is
		// deleted is not the sweep's to take (sessionDeletionTargets says why).
		{`INSERT INTO session_record_copies (srec_id, session_id, original_session_id, record_type, ts)
			VALUES ($1, $2, 'original-elsewhere', 'assistant', $3)`, []any{srecID, sessionID, at}},
	} {
		if _, err := s.pool.Exec(ctx, stmt.q, stmt.args...); err != nil {
			t.Fatalf("seed %s: %v", firstLine(stmt.q), err)
		}
	}
}

// namesSessionAnalysisRelation says whether a migration statement is one of the
// session analysis statements: every one of them names a table above or the
// queue's sequence, and no other statement does.
func namesSessionAnalysisRelation(stmt string) bool {
	for _, name := range append([]string{"session_fact_generation"}, sessionAnalysisTables...) {
		if strings.Contains(stmt, name) {
			return true
		}
	}
	return false
}

// The session analysis tables, their sequence and indexes, on the boot that
// creates them and on every boot after it.
//
// The container's database was empty when acquireTestStore migrated it -- a
// fresh install. This test then takes the objects away again, the state of a
// database from before them, and migrates twice. Migrate runs on every boot, so
// the second boot has to find everything in place and take nothing above ACCESS
// SHARE on these tables or on session_records, which their writers hold, and no
// ACCESS EXCLUSIVE anywhere: an ALTER takes it even when IF NOT EXISTS leaves it
// nothing to do, and an unguarded CREATE INDEX IF NOT EXISTS takes SHARE on its
// table before it looks at the name.
func TestSessionAnalysisSchemaMigration(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	indexTables := map[string]string{
		"idx_session_analysis_owner_user":    "session_analysis_facts",
		"idx_session_analysis_owner_profile": "session_analysis_facts",
		"idx_session_analysis_user_ids":      "session_analysis_facts",
		"idx_session_analysis_project_hash":  "session_analysis_facts",
		"idx_session_analysis_first_record":  "session_analysis_facts",
		"idx_session_turn_facts_started":     "session_turn_facts",
		"idx_session_segments_owner_user":    "session_activity_segments",
		"idx_session_segments_owner_profile": "session_activity_segments",
		"idx_session_segments_range":         "session_activity_segments",
		"idx_session_fact_dirty_order":       "session_fact_dirty",
		"idx_session_record_copies_session":  "session_record_copies",
		"idx_session_record_copies_original": "session_record_copies",
	}
	present := func(when string) {
		t.Helper()
		for _, table := range sessionAnalysisTables {
			var kind string
			if err := s.pool.QueryRow(ctx, `SELECT COALESCE(
				(SELECT relkind::text FROM pg_class WHERE oid = to_regclass($1)), '')`, table).Scan(&kind); err != nil {
				t.Fatalf("%s: read %s: %v", when, table, err)
			}
			if kind != "r" {
				t.Errorf("%s: %s has relkind %q, want a table", when, table, kind)
			}
		}
		// The queue draws its generation from the sequence; a sequence that exists
		// but is not the default is no sequence at all to the queue.
		var generationDefault string
		if err := s.pool.QueryRow(ctx, `SELECT COALESCE(
			(SELECT column_default FROM information_schema.columns
			 WHERE table_schema = current_schema() AND table_name = 'session_fact_dirty'
			   AND column_name = 'generation'), '')`).Scan(&generationDefault); err != nil {
			t.Fatalf("%s: read the generation default: %v", when, err)
		}
		if generationDefault != "nextval('session_fact_generation'::regclass)" {
			t.Errorf("%s: session_fact_dirty.generation defaults to %q, want nextval of session_fact_generation",
				when, generationDefault)
		}
		// By name and table: the guard, like IF NOT EXISTS, skips silently when
		// anything already has the name.
		for index, table := range indexTables {
			var on, def string
			err := s.pool.QueryRow(ctx, `SELECT tablename, indexdef FROM pg_indexes
				WHERE schemaname = current_schema() AND indexname = $1`, index).Scan(&on, &def)
			if errors.Is(err, pgx.ErrNoRows) {
				t.Errorf("%s: no index %s", when, index)
				continue
			}
			if err != nil {
				t.Fatalf("%s: read index %s: %v", when, index, err)
			}
			if on != table {
				t.Errorf("%s: %s is on %s, want %s", when, index, on, table)
			}
			// A one-line segment's range is empty under the default '[)'.
			if index == "idx_session_segments_range" &&
				!strings.Contains(def, "USING gist (tstzrange(seg_start, seg_end, '[]'::text))") {
				t.Errorf("%s: %s is %q, want a gist index over the closed range '[]'", when, index, def)
			}
		}
	}

	present("fresh install")

	// A database from before them. The sequence goes after the table whose default
	// names it.
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS ` + strings.Join(sessionAnalysisTables, ", "),
		`DROP SEQUENCE IF EXISTS session_fact_generation`,
	} {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	// A failure below can leave them dropped, and the tests after this one need
	// them. A pass leaves them as the boots made them.
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		if err := s.Migrate(ctx); err != nil {
			t.Logf("restore the session analysis tables: %v", err)
		}
	})
	for boot := 1; boot <= 2; boot++ {
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("Migrate (boot %d): %v", boot, err)
		}
		present(fmt.Sprintf("boot %d", boot))
	}

	// oneLine names a statement in a failure: a DO block's first line says nothing.
	oneLine := func(stmt string) string {
		line := strings.Join(strings.Fields(stmt), " ")
		if len(line) > 110 {
			line = line[:110] + " ..."
		}
		return line
	}
	// heldLocks runs stmt in a transaction it keeps open and lists, from a second
	// connection, the locks that transaction holds which a boot must not take on a
	// database already up to date: anything above ACCESS SHARE on these tables or
	// on session_records, and ACCESS EXCLUSIVE on anything.
	watched := append([]string{"session_records"}, sessionAnalysisTables...)
	heldLocks := func(stmt string) []string {
		t.Helper()
		conn, err := s.pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		defer conn.Release()
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck // the statement must not stay applied
		if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '10s'`); err != nil {
			t.Fatalf("set lock_timeout: %v", err)
		}
		var pid int
		if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatalf("read pid: %v", err)
		}
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("run %s: %v", oneLine(stmt), err)
		}
		rows, err := s.pool.Query(ctx, `SELECT l.relation::regclass::text || ' ' || l.mode FROM pg_locks l
			WHERE l.pid = $1 AND l.granted AND l.locktype = 'relation'
			  AND (l.mode = 'AccessExclusiveLock'
			       OR (l.mode <> 'AccessShareLock' AND l.relation = ANY($2::text[]::regclass[])))
			ORDER BY 1`, pid, watched)
		if err != nil {
			t.Fatalf("read pg_locks: %v", err)
		}
		locks, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("read pg_locks: %v", err)
		}
		return locks
	}

	// The probe has to be able to see what it is looking for: an ALTER with nothing
	// to add still takes ACCESS EXCLUSIVE, and an index statement that finds its
	// index still takes SHARE on the table.
	for _, control := range []string{
		`ALTER TABLE session_records ADD COLUMN IF NOT EXISTS message_id TEXT`,
		`CREATE INDEX IF NOT EXISTS idx_session_fact_dirty_order ON session_fact_dirty (priority, first_dirty_at)`,
	} {
		if locks := heldLocks(control); len(locks) == 0 {
			t.Fatalf("%s showed no lock; the probe cannot see one", control)
		}
	}

	// Every statement Migrate runs that names one of them, in each migration group.
	probed := 0
	for _, group := range []struct {
		name  string
		stmts []string
	}{{"dropDependentViews", dropDependentViews}, {"migrations", migrations}, {"sessionP4FactMigrations", sessionP4FactMigrations}, {"sessionProcessIOMigrations", sessionProcessIOMigrations}, {"dependentViews", dependentViews}} {
		for _, stmt := range group.stmts {
			if !namesSessionAnalysisRelation(stmt) {
				continue
			}
			probed++
			if locks := heldLocks(stmt); len(locks) > 0 {
				t.Errorf("%s: %s holds %v on a database that already has it", group.name, oneLine(stmt), locks)
			}
		}
	}
	// Seven tables, their twelve indexes and the queue's sequence. A statement added
	// that names them changes the count, so it cannot join them unprobed.
	if probed != 21 {
		t.Errorf("probed %d session analysis statements, want 21", probed)
	}
}

// Being on sessionDeletionTargets and being emptied are different claims, and
// the second is the one a privacy delete makes. This runs the real sweep against
// a row in each table for a deleted session and for one that stays.
func TestSweepEmptiesSessionAnalysisTables(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	seedSessionAnalysisRows(t, s, "facts-doomed", "", 1)
	seedSessionAnalysisRows(t, s, "facts-kept", "", 2)

	if _, err := s.DeleteSession(ctx, "facts-doomed", "admin@example.com", "privacy", false, false); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := s.SweepDeletedSessions(ctx, 25); err != nil {
		t.Fatalf("SweepDeletedSessions: %v", err)
	}
	for _, table := range sessionAnalysisTables {
		if n := countWhere(t, s, table, "session_id = 'facts-doomed'"); n != 0 {
			t.Errorf("%s still holds %d row(s) for the deleted session", table, n)
		}
		if n := countWhere(t, s, table, "session_id = 'facts-kept'"); n != 1 {
			t.Errorf("%s lost an unrelated session's row (kept = %d, want 1)", table, n)
		}
	}
}

// sessionAnalysisRowsFor counts a session's rows across the session analysis
// tables.
func sessionAnalysisRowsFor(t *testing.T, s *PgStore, sessionID string) int {
	t.Helper()
	n := 0
	for _, table := range sessionAnalysisTables {
		n += countWhere(t, s, table, "session_id = $1", sessionID)
	}
	return n
}

// A session whose only tie to a project is its analysis facts -- the
// project_hash they were computed with, outlasting the session's other rows --
// still belongs to that project: counting and deleting the project reach it, and
// the sweep then takes its rows.
func TestDeleteProject_reachesAnalysisFactsOnlySessions(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	seedSessionAnalysisRows(t, s, "facts-a", "-ph-facts", 1)
	seedSessionAnalysisRows(t, s, "facts-other", "-ph-other", 2)

	if n, err := s.ProjectSessionCount(ctx, "-ph-facts", ""); err != nil || n != 1 {
		t.Fatalf("ProjectSessionCount = (%d, %v), want 1", n, err)
	}
	res, err := s.DeleteProject(ctx, []string{"-ph-facts"}, "admin@example.com", "privacy", false)
	if err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if res.MarkedSessions != 1 {
		t.Fatalf("DeleteProject = %+v, want the facts-only session marked", res)
	}
	if _, err := s.SweepDeletedSessions(ctx, 25); err != nil {
		t.Fatalf("SweepDeletedSessions: %v", err)
	}
	if n := sessionAnalysisRowsFor(t, s, "facts-a"); n != 0 {
		t.Errorf("%d session analysis row(s) of the deleted project's session survived the sweep", n)
	}
	if n := sessionAnalysisRowsFor(t, s, "facts-other"); n != len(sessionAnalysisTables) {
		t.Errorf("another project's session has %d session analysis row(s), want %d", n, len(sessionAnalysisTables))
	}
}

// The periodic purge of blocked projects finds sessions the same way, so a
// session of a blocked project that has nothing left but its facts is tombstoned
// and swept too.
func TestPurgeBlockedProjects_sweepsAnalysisFactsOnlySessions(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if err := s.BlockProject(ctx, "-ph-blocked", "", "admin@example.com", "privacy"); err != nil {
		t.Fatal(err)
	}
	seedSessionAnalysisRows(t, s, "facts-blocked", "-ph-blocked", 1)
	seedSessionAnalysisRows(t, s, "facts-other", "-ph-other", 2)

	if n, err := s.PurgeBlockedProjects(ctx); err != nil || n != 1 {
		t.Fatalf("PurgeBlockedProjects = (%d, %v), want the facts-only session tombstoned", n, err)
	}
	if _, err := s.SweepDeletedSessions(ctx, 25); err != nil {
		t.Fatalf("SweepDeletedSessions: %v", err)
	}
	if n := sessionAnalysisRowsFor(t, s, "facts-blocked"); n != 0 {
		t.Errorf("%d session analysis row(s) of the blocked project's session survived", n)
	}
	if n := sessionAnalysisRowsFor(t, s, "facts-other"); n != len(sessionAnalysisTables) {
		t.Errorf("another project's session has %d session analysis row(s), want %d", n, len(sessionAnalysisTables))
	}
	if n, err := s.PurgeBlockedProjects(ctx); err != nil || n != 0 {
		t.Errorf("second purge = (%d, %v), want nothing left to do", n, err)
	}
}

// Deleting one session with its project in mind needs the session's project.
// The facts can supply it when nothing else does, and come last when something
// else does: their hash is derived, the one the session synced under is not.
func TestDeleteSession_resolvesProjectFromAnalysisFactsLast(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	seedSessionAnalysisRows(t, s, "facts-c", "-ph-c", 1)
	seedSessionAnalysisRows(t, s, "facts-c-sibling", "-ph-c", 2)

	res, err := s.DeleteSession(ctx, "facts-c", "admin@example.com", "privacy", true, true)
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if res.ProjectHash != "-ph-c" || !res.ProjectBlocked || res.PurgedSessions != 1 || res.MarkedSessions != 2 {
		t.Errorf("DeleteSession = %+v, want project -ph-c resolved, blocked, and its sibling purged", res)
	}

	// A session whose facts still carry an older project than its records.
	seedSessionAnalysisRows(t, s, "facts-stale", "-ph-stale", 3)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{Ts: time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC),
		SessionID: "facts-stale", RecordType: "user", UUID: "stale-1", ProfileEmail: "p@example.com",
		ProjectHash: "-ph-records", Raw: json.RawMessage(`{"text":"work"}`)}}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	res, err = s.DeleteSession(ctx, "facts-stale", "admin@example.com", "privacy", false, false)
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if res.ProjectHash != "-ph-records" {
		t.Errorf("DeleteSession resolved project %q, want the records' -ph-records over the facts' -ph-stale", res.ProjectHash)
	}
}

// Until the worker runs, every deployment has the tables it writes and nothing in
// them; only the queue fills, a row per session its writers touched. The
// deletion paths that reach them have to run over them so: the project lookups,
// the sweep's deletes and its check, and the verification pass.
func TestDeletionPathsRunOverEmptySessionAnalysisTables(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	seedTwoSessions(t, s, time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC))
	for _, table := range sessionAnalysisTables {
		want := 0
		if table == "session_fact_dirty" {
			want = 2 // doomed and kept, queued by the writers that seeded them
		}
		if n := countWhere(t, s, table, "true"); n != want {
			t.Fatalf("%s holds %d row(s); this test needs %d", table, n, want)
		}
	}

	if n, err := s.ProjectSessionCount(ctx, "ph-doomed", ""); err != nil || n != 1 {
		t.Fatalf("ProjectSessionCount = (%d, %v), want 1", n, err)
	}
	res, err := s.DeleteProject(ctx, []string{"ph-doomed"}, "admin@example.com", "privacy", true)
	if err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if res.MarkedSessions != 1 || !res.ProjectBlocked {
		t.Fatalf("DeleteProject = %+v, want the project's session marked and the project blocked", res)
	}
	if _, err := s.SweepDeletedSessions(ctx, 25); err != nil {
		t.Fatalf("SweepDeletedSessions: %v", err)
	}
	if n := countRows(t, s, "session_records", "doomed"); n != 0 {
		t.Errorf("session_records still holds %d row(s) of the deleted project's session", n)
	}
	if n := countRows(t, s, "session_records", "kept"); n == 0 {
		t.Error("another project's session lost its records")
	}
	if n, err := s.PurgeBlockedProjects(ctx); err != nil || n != 0 {
		t.Errorf("PurgeBlockedProjects = (%d, %v), want nothing left to do", n, err)
	}
	if requeued, err := s.VerifyDeletedSessions(ctx); err != nil || requeued != 0 {
		t.Errorf("VerifyDeletedSessions = (%d, %v), want nothing requeued", requeued, err)
	}
}
