package store

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// queueRow is a session's row in session_fact_dirty.
type queueRow struct {
	generation, removalGeneration int64
	scanFrom                      *int64
	firstDirtyAt, notBefore       time.Time
	priority                      int16
	reasons                       int32
	attempts                      int32
	retryAt                       *time.Time
}

func queueRows(t *testing.T, s *PgStore) map[string]queueRow {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), `SELECT session_id, generation, removal_generation,
		scan_from_id, first_dirty_at, not_before, priority, reasons, attempts, retry_at FROM session_fact_dirty`)
	if err != nil {
		t.Fatalf("read session_fact_dirty: %v", err)
	}
	defer rows.Close()
	out := map[string]queueRow{}
	for rows.Next() {
		var id string
		var r queueRow
		if err := rows.Scan(&id, &r.generation, &r.removalGeneration, &r.scanFrom, &r.firstDirtyAt,
			&r.notBefore, &r.priority, &r.reasons, &r.attempts, &r.retryAt); err != nil {
			t.Fatalf("scan session_fact_dirty: %v", err)
		}
		out[id] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read session_fact_dirty: %v", err)
	}
	return out
}

// markInTx runs one queue write in a transaction of its own and commits it.
func markInTx(t *testing.T, s *PgStore, ids []string, scanFrom []*int64, reason dirtyReason) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := markSessionFactsDirty(ctx, tx, ids, scanFrom, reason); err != nil {
		t.Fatalf("markSessionFactsDirty: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func int64Ptr(v int64) *int64 { return &v }

// clockRestarted reports, by the database's clock, whether a session's queue row
// has its first_dirty_at restarted within the last minute and not_before at the
// same instant -- what a removal and a clock restart write.
func clockRestarted(t *testing.T, s *PgStore, sessionID string) bool {
	t.Helper()
	var restarted bool
	if err := s.pool.QueryRow(context.Background(), `SELECT now() - first_dirty_at < interval '1 minute'
		AND not_before = first_dirty_at FROM session_fact_dirty WHERE session_id = $1`, sessionID).Scan(&restarted); err != nil {
		t.Fatalf("read the queue row of %s: %v", sessionID, err)
	}
	return restarted
}

func scanFromString(p *int64) string {
	if p == nil {
		return "NULL"
	}
	return strconv.FormatInt(*p, 10)
}

// One queue write, row by row, as p1b-design §5.1 has it: a new row takes its
// schedule from the reason table; a bump of a queued row ORs the reason in,
// keeps the lowest priority and scan_from_id, draws a new generation and leaves
// a failure's backoff alone; a removal records its own generation and restarts
// the row's clock; a clock restart restarts the clock and records no removal;
// both drop a retry_at already past and keep one still ahead; a tombstoned or
// empty session is never queued.
func TestMarkSessionFactsDirty_writesTheReasonTable(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	// A session named twice is one row with the least of its new ids.
	markInTx(t, s, []string{"a", "", "a", "b"}, []*int64{int64Ptr(5), int64Ptr(1), int64Ptr(3), nil}, dirtySync)
	rows := queueRows(t, s)
	if len(rows) != 2 {
		t.Fatalf("queued %d rows, want a and b (the empty id is no session)", len(rows))
	}
	a, b := rows["a"], rows["b"]
	if a.scanFrom == nil || *a.scanFrom != 3 || b.scanFrom != nil {
		t.Errorf("scan_from_id a=%s b=%s, want 3 and NULL", scanFromString(a.scanFrom), scanFromString(b.scanFrom))
	}
	if a.reasons != 1 || a.priority != 0 || a.removalGeneration != 0 || a.generation == 0 {
		t.Errorf("a = %+v, want reason sync, priority 0, no removal, a generation", a)
	}
	if d := a.notBefore.Sub(a.firstDirtyAt); d < 9*time.Second || d > 11*time.Second {
		t.Errorf("a not_before - first_dirty_at = %v, want the 10s sync debounce", d)
	}

	// A failure's backoff and a first_dirty_at old enough that the sync delay is
	// already past.
	if _, err := s.pool.Exec(ctx, `UPDATE session_fact_dirty SET attempts = 3,
		retry_at = now() + interval '1 hour', first_dirty_at = now() - interval '10 minutes' WHERE session_id = 'a'`); err != nil {
		t.Fatal(err)
	}
	before := queueRows(t, s)["a"]
	markInTx(t, s, []string{"a"}, []*int64{int64Ptr(9)}, dirtySync)
	a = queueRows(t, s)["a"]
	if !a.notBefore.Equal(before.firstDirtyAt.Add(2 * time.Minute)) {
		t.Errorf("a not_before = %v, want first_dirty_at + the 2m sync delay (%v)", a.notBefore, before.firstDirtyAt.Add(2*time.Minute))
	}
	markInTx(t, s, []string{"a"}, nil, dirtyImputed)
	a = queueRows(t, s)["a"]
	if a.reasons != 1|64 || a.priority != 0 {
		t.Errorf("a reasons=%d priority=%d, want sync|imputed and the live priority kept", a.reasons, a.priority)
	}
	if a.scanFrom == nil || *a.scanFrom != 3 {
		t.Errorf("a scan_from_id = %s, want 3: a bump never moves it past an unscanned row", scanFromString(a.scanFrom))
	}
	if a.generation <= before.generation {
		t.Errorf("a generation %d after bumps, was %d: a bump draws a new one", a.generation, before.generation)
	}
	if a.attempts != 3 || a.retryAt == nil || !a.retryAt.Equal(*before.retryAt) {
		t.Errorf("a attempts=%d retry_at=%v, want the backoff untouched (3, %v)", a.attempts, a.retryAt, before.retryAt)
	}
	if !a.firstDirtyAt.Equal(before.firstDirtyAt) {
		t.Errorf("a first_dirty_at moved on an ordinary bump")
	}
	// A lower scan_from_id wins, and a rescan reason makes it 0.
	markInTx(t, s, []string{"a"}, []*int64{int64Ptr(2)}, dirtySync)
	if got := queueRows(t, s)["a"].scanFrom; got == nil || *got != 2 {
		t.Errorf("a scan_from_id = %s, want 2", scanFromString(got))
	}
	markInTx(t, s, []string{"a", "c"}, []*int64{int64Ptr(7), int64Ptr(7)}, dirtyReenrich)
	rows = queueRows(t, s)
	if rows["a"].scanFrom == nil || *rows["a"].scanFrom != 0 || rows["c"].scanFrom == nil || *rows["c"].scanFrom != 0 {
		t.Errorf("after reenrich scan_from_id a=%s c=%s, want 0 for both", scanFromString(rows["a"].scanFrom), scanFromString(rows["c"].scanFrom))
	}
	if rows["c"].priority != 1 || rows["c"].reasons != 2 {
		t.Errorf("c = %+v, want reenrich at bulk priority", rows["c"])
	}

	// A removal records its own generation and restarts the clock. a's backoff is
	// still an hour ahead and stays; e's retry_at is an hour past, which holds
	// nothing back, and goes.
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty (session_id, attempts, retry_at)
		VALUES ('e', 2, now() - interval '1 hour')`); err != nil {
		t.Fatal(err)
	}
	markInTx(t, s, []string{"a", "d", "e"}, nil, dirtyDeletionUser)
	rows = queueRows(t, s)
	for _, id := range []string{"a", "d", "e"} {
		r := rows[id]
		if r.removalGeneration == 0 || r.removalGeneration != r.generation {
			t.Errorf("%s removal_generation=%d generation=%d, want the removal's own", id, r.removalGeneration, r.generation)
		}
		if r.reasons&8192 == 0 {
			t.Errorf("%s reasons=%d, want deletion_user", id, r.reasons)
		}
		if !clockRestarted(t, s, id) {
			t.Errorf("%s first_dirty_at=%v not_before=%v, want both reset to now", id, r.firstDirtyAt, r.notBefore)
		}
	}
	if r := rows["a"]; r.attempts != 3 || r.retryAt == nil || !r.retryAt.Equal(*before.retryAt) {
		t.Errorf("a attempts=%d retry_at=%v after a removal, want the live backoff kept (3, %v)", r.attempts, r.retryAt, before.retryAt)
	}
	if r := rows["e"]; r.attempts != 2 || r.retryAt != nil {
		t.Errorf("e attempts=%d retry_at=%v after a removal, want the past retry_at dropped and attempts 2", r.attempts, r.retryAt)
	}
	// An ordinary bump after the removal leaves removal_generation behind it.
	markInTx(t, s, []string{"a"}, nil, dirtyOtel)
	if r := queueRows(t, s)["a"]; r.removalGeneration >= r.generation {
		t.Errorf("a removal_generation=%d generation=%d after a later bump, want it behind", r.removalGeneration, r.generation)
	}

	// A clock restart restarts the clock as a removal does and records none. a is
	// live and c bulk, both queued 100 days ago; a's backoff is ahead and c's
	// retry_at past. Each keeps its priority and its removal_generation -- a's
	// is the removal above, c's none -- so no publish is held back for a deletion
	// that did not happen.
	if _, err := s.pool.Exec(ctx, `UPDATE session_fact_dirty SET
			first_dirty_at = now() - interval '100 days', not_before = now() - interval '100 days',
			retry_at = now() + CASE session_id WHEN 'a' THEN interval '1 hour' ELSE interval '-1 hour' END
		WHERE session_id IN ('a', 'c')`); err != nil {
		t.Fatal(err)
	}
	was := queueRows(t, s)
	if was["a"].priority != 0 || was["c"].priority != 1 || was["a"].removalGeneration == 0 {
		t.Fatalf("a = %+v, c = %+v, want a live with a removal recorded and c bulk", was["a"], was["c"])
	}
	markInTx(t, s, []string{"a", "c", "f"}, nil, dirtyClockRestart)
	rows = queueRows(t, s)
	for _, id := range []string{"a", "c"} {
		r, w := rows[id], was[id]
		if !clockRestarted(t, s, id) {
			t.Errorf("%s first_dirty_at=%v not_before=%v, want both reset to now", id, r.firstDirtyAt, r.notBefore)
		}
		if r.priority != w.priority || r.removalGeneration != w.removalGeneration {
			t.Errorf("%s priority=%d removal_generation=%d after a clock restart, want them kept (%d, %d)",
				id, r.priority, r.removalGeneration, w.priority, w.removalGeneration)
		}
		if r.reasons != w.reasons|131072 || r.generation <= w.generation {
			t.Errorf("%s reasons=%d generation=%d, want the clock restart ORed into %d and a generation past %d",
				id, r.reasons, r.generation, w.reasons, w.generation)
		}
		if r.attempts != w.attempts || scanFromString(r.scanFrom) != scanFromString(w.scanFrom) {
			t.Errorf("%s attempts=%d scan_from_id=%s after a clock restart, want them kept (%d, %s)",
				id, r.attempts, scanFromString(r.scanFrom), w.attempts, scanFromString(w.scanFrom))
		}
	}
	if r := rows["a"]; r.retryAt == nil || !r.retryAt.Equal(*was["a"].retryAt) {
		t.Errorf("a retry_at=%v after a clock restart, want the live backoff kept (%v)", r.retryAt, was["a"].retryAt)
	}
	if r := rows["c"]; r.retryAt != nil {
		t.Errorf("c retry_at=%v after a clock restart, want the past retry_at dropped", r.retryAt)
	}
	// A session it queues anew is a bulk row with no removal and nothing to scan.
	if f := rows["f"]; f.priority != 1 || f.reasons != 131072 || f.removalGeneration != 0 || f.scanFrom != nil ||
		!f.notBefore.Equal(f.firstDirtyAt) {
		t.Errorf("f = %+v, want a bulk row due at once with the clock restart alone", f)
	}

	// A tombstoned session is never queued, and a row it had is left to the
	// sweep, by a removal and by a clock restart alike.
	if _, err := s.pool.Exec(ctx, `INSERT INTO deleted_sessions (session_id) VALUES ('b'), ('gone')`); err != nil {
		t.Fatal(err)
	}
	beforeB := queueRows(t, s)["b"]
	for _, reason := range []dirtyReason{dirtyRetention, dirtyClockRestart} {
		markInTx(t, s, []string{"b", "gone"}, nil, reason)
		rows = queueRows(t, s)
		if _, ok := rows["gone"]; ok {
			t.Errorf("a tombstoned session was queued (reason bit %d)", dirtyReasons[reason].reason)
		}
		if b := rows["b"]; b.generation != beforeB.generation || b.reasons != beforeB.reasons ||
			b.removalGeneration != beforeB.removalGeneration || !b.firstDirtyAt.Equal(beforeB.firstDirtyAt) {
			t.Errorf("tombstoned b's row changed (reason bit %d): %+v, was %+v", dirtyReasons[reason].reason, b, beforeB)
		}
	}
}

// Until the worker runs, the queue is the only session analysis table that
// fills: writers and delete paths queue sessions and delete what nothing wrote.
// So the rollback of p1b-design §7.6 for a deployment that never turned the
// feature on is emptying the queue, and the runbook's check (appendix D-1) then
// reads zero rows and zero markers.
func TestSessionAnalysisRollbackEmptiesTheQueue(t *testing.T) {
	s := lifecycleStore(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	if err := s.InsertEvents(ctx, []*OtelEvent{{Ts: at, EventName: "api_request", SessionID: "rb-a", LoginEmail: "rb@example.com"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: at, SessionID: "rb-a", RecordType: "user", UUID: "rb1", ProfileEmail: "rb@example.com", Raw: []byte(`{}`)},
		{Ts: at, SessionID: "rb-b", RecordType: "user", UUID: "rb2", ProfileEmail: "gone@example.com", Raw: []byte(`{}`)},
	}); err != nil {
		t.Fatal(err)
	}
	ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{obsSnapshot("rb-c", "p@example.com", at, "x")}})
	if _, err := s.ExcludeAccount(ctx, "rb@example.com", "r", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUserData(ctx, "gone@example.com", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteSession(ctx, "rb-c", "admin@example.com", "privacy", false, false); err != nil {
		t.Fatal(err)
	}

	check := func(wantRows int) {
		t.Helper()
		var rows, markers int
		if err := s.pool.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM session_analysis_facts) + (SELECT count(*) FROM session_turn_facts)
			     + (SELECT count(*) FROM session_activity_segments) + (SELECT count(*) FROM session_fact_dirty)
			     + (SELECT count(*) FROM session_record_copies),
			       (SELECT count(*) FROM schema_backfills
			        WHERE name = 'session_fact_copies_v1' OR name LIKE 'session_facts_r%')`).Scan(&rows, &markers); err != nil {
			t.Fatal(err)
		}
		if rows != wantRows || markers != 0 {
			t.Errorf("p1b_rows=%d p1b_markers=%d, want %d and 0", rows, markers, wantRows)
		}
	}
	queue := len(queueRows(t, s))
	if queue == 0 {
		t.Fatal("nothing was queued; the check below would prove nothing")
	}
	check(queue)
	if _, err := s.pool.Exec(ctx, `DELETE FROM session_fact_dirty`); err != nil {
		t.Fatal(err)
	}
	check(0)
}

// installQueueBarrier makes a transaction that set cctrace_test.queue_barrier to
// a session id stop just before it inserts that session's queue row -- after the
// row's defaults, its generation among them, are drawn -- until release is
// called. It returns release.
func installQueueBarrier(t *testing.T, s *PgStore) (release func()) {
	t.Helper()
	ctx := context.Background()
	const key = 7319001
	for _, stmt := range []string{
		`CREATE OR REPLACE FUNCTION test_queue_barrier() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.session_id = current_setting('cctrace_test.queue_barrier', true) THEN
				PERFORM pg_advisory_xact_lock_shared(7319001);
			END IF;
			RETURN NEW;
		END $$`,
		`CREATE TRIGGER test_queue_barrier BEFORE INSERT ON session_fact_dirty
			FOR EACH ROW EXECUTE FUNCTION test_queue_barrier()`,
	} {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("install barrier: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(ctx, `DROP TRIGGER IF EXISTS test_queue_barrier ON session_fact_dirty`)
		_, _ = s.pool.Exec(ctx, `DROP FUNCTION IF EXISTS test_queue_barrier()`)
	})
	holder, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire barrier holder: %v", err)
	}
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_lock($1)`, key); err != nil {
		holder.Release()
		t.Fatalf("close barrier: %v", err)
	}
	released := false
	release = func() {
		if released {
			return
		}
		released = true
		_, _ = holder.Exec(ctx, `SELECT pg_advisory_unlock($1)`, key)
		holder.Release()
	}
	t.Cleanup(release)
	return release
}

// backendPID is the server process of a transaction.
func backendPID(t *testing.T, tx pgx.Tx) int {
	t.Helper()
	var pid int
	if err := tx.QueryRow(context.Background(), `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatalf("read backend pid: %v", err)
	}
	return pid
}

// waitUntilBlocked returns once the backend pid waits for a lock.
func waitUntilBlocked(t *testing.T, pool *pgxpool.Pool, pid int, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(context.Background(),
			`SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid = $1 AND NOT granted)`, pid).Scan(&waiting); err != nil {
			t.Fatalf("read pg_locks: %v", err)
		}
		if waiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never blocked", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// F2 (p1b-design §5.1, §9.2): T1 queues a new session S and stops after drawing
// the row's default generation, before its insert meets the row a removal (T2)
// inserts and commits meanwhile. T1's conflict update then has to draw a
// generation after T2's, or the session's generation falls behind its
// removal_generation and the worker's publish rolls back for good.
//
// Mutation: take EXCLUDED.generation in the conflict update -- the value drawn
// before the row lock -- and T1 leaves generation below removal_generation.
// Exercise the queue writer directly for T1: the outer writer first locks the
// source revision row, which serializes T2 before it can reach this queue race.
// The overlapping-writers test below covers that complete writer path.
func TestMarkSessionFactsDirty_generationFollowsCommitOrder(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	release := installQueueBarrier(t, s)

	t1, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer t1.Rollback(ctx) //nolint:errcheck // test transaction
	if _, err := t1.Exec(ctx, `SELECT set_config('cctrace_test.queue_barrier', 'S', true)`); err != nil {
		t.Fatal(err)
	}
	pid1 := backendPID(t, t1)
	done := make(chan error, 1)
	go func() { done <- queueSessionFactsDirty(ctx, t1, []string{"S"}, nil, dirtySync) }()
	waitUntilBlocked(t, s.pool, pid1, "T1 at the barrier")

	markInTx(t, s, []string{"S"}, nil, dirtyDeletionUser)
	removal := queueRows(t, s)["S"]
	if removal.removalGeneration != removal.generation {
		t.Fatalf("T2's removal wrote removal_generation %d over generation %d", removal.removalGeneration, removal.generation)
	}

	release()
	if err := <-done; err != nil {
		t.Fatalf("T1: %v", err)
	}
	if err := t1.Commit(ctx); err != nil {
		t.Fatalf("T1 commit: %v", err)
	}
	final := queueRows(t, s)["S"]
	if final.generation <= removal.generation {
		t.Errorf("generation %d after T1 committed second, want above T2's %d", final.generation, removal.generation)
	}
	if final.removalGeneration != removal.removalGeneration || final.generation < final.removalGeneration {
		t.Errorf("removal_generation %d, generation %d: want removal_generation <= generation", final.removalGeneration, final.generation)
	}
	if final.reasons != 1|8192 {
		t.Errorf("reasons = %d, want sync|deletion_user", final.reasons)
	}
}

// F3 (p1b-design §5.1, §9.2): T1 queues a and b and stops before b's row with
// a's already in; T2 queues b and a, given in that order. Every queue write locks
// its rows in one order, so T2 waits behind T1 on a instead of taking b first,
// and both commit.
//
// Mutation: lock the rows in the order given (ORDER BY array_position($1, u.s))
// and T2 takes b, then waits on a while T1 waits on b: deadlock_detected.
func TestMarkSessionFactsDirty_overlappingWritersDoNotDeadlock(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	release := installQueueBarrier(t, s)

	t1, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer t1.Rollback(ctx) //nolint:errcheck // test transaction
	if _, err := t1.Exec(ctx, `SELECT set_config('cctrace_test.queue_barrier', 'b', true)`); err != nil {
		t.Fatal(err)
	}
	pid1 := backendPID(t, t1)
	done1 := make(chan error, 1)
	go func() { done1 <- markSessionFactsDirty(ctx, t1, []string{"a", "b"}, nil, dirtySync) }()
	waitUntilBlocked(t, s.pool, pid1, "T1 at the barrier")

	t2, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer t2.Rollback(ctx) //nolint:errcheck // test transaction
	pid2 := backendPID(t, t2)
	done2 := make(chan error, 1)
	go func() { done2 <- markSessionFactsDirty(ctx, t2, []string{"b", "a"}, nil, dirtyOtel) }()
	waitUntilBlocked(t, s.pool, pid2, "T2 behind T1")

	release()
	if err := <-done1; err != nil {
		t.Fatalf("T1: %v", err)
	}
	if err := t1.Commit(ctx); err != nil {
		t.Fatalf("T1 commit: %v", err)
	}
	if err := <-done2; err != nil {
		t.Fatalf("T2: %v", err)
	}
	if err := t2.Commit(ctx); err != nil {
		t.Fatalf("T2 commit: %v", err)
	}
	for id, r := range queueRows(t, s) {
		if r.reasons != 1|4 {
			t.Errorf("%s reasons = %d, want both writers' sync|otel", id, r.reasons)
		}
		var revision int64
		if err := s.pool.QueryRow(ctx, `SELECT revision FROM session_timeline_revisions WHERE session_id=$1`, id).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		if revision != 2 {
			t.Errorf("%s source revision = %d, want both writers' increments", id, revision)
		}
	}
}

// Every queue write locks its rows in byte order, the order
// markSessionFactsDirtyInOrder sorts its groups in. Production's database
// already collates by bytes (Alpine, musl), as this container's does, so no
// ordering test can tell the clause from its absence; the plan can, because a
// collation other than the column's default is printed in the Sort Key. The
// sort that orders the rows written is the outermost one, the first the plan
// prints; a sort further in, under a grouping or a join, is the planner's own
// and may come and go.
//
// Mutation: drop COLLATE "C" and the first Sort Key reads the session id alone.
func TestMarkSessionFactsDirty_sortsInByteOrder(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	rows, err := s.pool.Query(ctx, `EXPLAIN (VERBOSE, COSTS OFF) `+markSessionFactsDirtySQL,
		[]string{"b", "a"}, []*int64{nil, nil}, 10.0, int16(0), int32(1), 120.0)
	if err != nil {
		t.Fatalf("explain the queue write: %v", err)
	}
	plan, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("explain the queue write: %v", err)
	}
	var sortKeys []string
	for _, line := range plan {
		if strings.Contains(line, "Sort Key:") {
			sortKeys = append(sortKeys, strings.TrimSpace(line))
		}
	}
	if len(sortKeys) == 0 || !strings.Contains(sortKeys[0], `COLLATE "C"`) {
		t.Errorf("Sort Key lines %q, want the first to sort by COLLATE \"C\"; plan:\n%s", sortKeys, strings.Join(plan, "\n"))
	}
}

// A transaction that queues sessions under several reasons -- the removals of a
// deletion and the neighbours it found -- still locks every row in one order,
// across the groups. The insert order is read off a trigger.
//
// Mutation: queue the groups one after the other and the neighbours a, c and e
// come after the removals b and d.
func TestMarkSessionFactsDirtyInOrder_locksAcrossGroupsInOneOrder(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	for _, stmt := range []string{
		`CREATE TABLE test_queue_order (n serial PRIMARY KEY, session_id text NOT NULL)`,
		`CREATE OR REPLACE FUNCTION test_queue_order() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN INSERT INTO test_queue_order (session_id) VALUES (NEW.session_id); RETURN NEW; END $$`,
		`CREATE TRIGGER test_queue_order BEFORE INSERT ON session_fact_dirty
			FOR EACH ROW EXECUTE FUNCTION test_queue_order()`,
	} {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(ctx, `DROP TRIGGER IF EXISTS test_queue_order ON session_fact_dirty`)
		_, _ = s.pool.Exec(ctx, `DROP FUNCTION IF EXISTS test_queue_order()`)
		_, _ = s.pool.Exec(ctx, `DROP TABLE IF EXISTS test_queue_order`)
	})

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := markSessionFactsDirtyInOrder(ctx, tx,
		dirtyGroup{ids: []string{"d", "b"}, reason: dirtyDeletionUser},
		dirtyGroup{ids: []string{"e", "a", "c"}, reason: dirtyDeletionPeer}); err != nil {
		t.Fatalf("markSessionFactsDirtyInOrder: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := s.pool.Query(ctx, `SELECT session_id FROM test_queue_order ORDER BY n`)
	if err != nil {
		t.Fatal(err)
	}
	order, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if got := len(order); got != 5 || order[0] != "a" || order[1] != "b" || order[2] != "c" || order[3] != "d" || order[4] != "e" {
		t.Errorf("rows inserted in order %v, want [a b c d e]", order)
	}
	queued := queueRows(t, s)
	for _, id := range []string{"b", "d"} {
		if r := queued[id]; r.reasons != 8192 || r.removalGeneration != r.generation {
			t.Errorf("%s = %+v, want a deletion_user removal", id, r)
		}
	}
	for _, id := range []string{"a", "c", "e"} {
		if r := queued[id]; r.reasons != 256 || r.removalGeneration != 0 || r.scanFrom == nil || *r.scanFrom != 0 {
			t.Errorf("%s = %+v, want a peer rescanned whole", id, r)
		}
	}
}
