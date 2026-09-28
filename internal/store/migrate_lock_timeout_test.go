package store

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestMigrateFailsInsteadOfWaitingForALock pins the behaviour that #613 is about.
//
// Boot ran Migrate with no lock_timeout, so a single long reader on
// session_records made the ALTER TABLE wait forever: the container stayed "Up"
// with its ports bound while every request went unanswered, and the log simply
// stopped. Waiting is the wrong answer here -- a deployment that cannot get its
// lock has to say so.
//
// The test holds ACCESS EXCLUSIVE on session_records from another session, which
// is what a long analytical query does to the ALTER, and asserts Migrate gives up
// with an error that names the statement and the backend that blocked it.
func TestMigrateFailsInsteadOfWaitingForALock(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()

	blocker, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire blocking connection: %v", err)
	}
	defer blocker.Release()

	tx, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatalf("begin blocking transaction: %v", err)
	}
	if _, err := tx.Exec(ctx, "LOCK TABLE session_records IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatalf("take blocking lock: %v", err)
	}
	var blockerPID int
	if err := tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&blockerPID); err != nil {
		t.Fatalf("read blocking pid: %v", err)
	}
	released := false
	defer func() {
		if !released {
			_ = tx.Rollback(ctx)
		}
	}()

	// Short enough that the test is not the slow one in the package, long enough
	// that a healthy lock is still acquired on the first try.
	s.migrateLockTimeout = 300 * time.Millisecond
	s.migrateLockAttempts = 2
	s.migrateLockRetryDelay = 100 * time.Millisecond
	defer func() {
		s.migrateLockTimeout = defaultMigrateLockTimeout
		s.migrateLockAttempts = defaultMigrateLockAttempts
		s.migrateLockRetryDelay = defaultMigrateLockRetryDelay
	}()

	budget := s.migrateLockTimeout*time.Duration(s.migrateLockAttempts) +
		s.migrateLockRetryDelay*time.Duration(s.migrateLockAttempts) + 30*time.Second
	deadlined, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	start := time.Now()
	err = s.Migrate(deadlined)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Migrate returned nil while session_records was locked; it waited for, or skipped, a lock it could not take")
	}
	if deadlined.Err() != nil {
		t.Fatalf("Migrate did not return on its own within %s: it is still waiting for the lock", budget)
	}
	if elapsed >= budget {
		t.Fatalf("Migrate took %s, at or past the %s budget", elapsed, budget)
	}

	// Named specifically. "session_records" alone would also be satisfied by the
	// DROP VIEW IF EXISTS visible_session_records in the first pass, so it would
	// keep passing even if the message stopped naming the statement that failed.
	msg := err.Error()
	if !strings.Contains(msg, "ON session_records") {
		t.Errorf("error does not name the statement that could not take its lock:\n%s", msg)
	}
	if !strings.Contains(msg, "lock timeout") {
		t.Errorf("error does not say the lock was the reason:\n%s", msg)
	}
	if !strings.Contains(msg, fmt.Sprintf("pid=%d", blockerPID)) {
		t.Errorf("error does not report the backend that was holding the lock (pid=%d):\n%s", blockerPID, msg)
	}

	// A failed migration is not inert. dropDependentViews is the first pass, so
	// giving up later leaves the dashboard's views dropped -- state the old
	// unbounded wait never produced, because it hung before dropping anything.
	// Recovery is the next successful boot, not the absence of a mess.
	var viewsPresent int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_views WHERE schemaname = 'public' AND viewname = ANY($1)`,
		[]string{"unified_events", "visible_events", "visible_session_records", "visible_metrics"},
	).Scan(&viewsPresent); err != nil {
		t.Fatalf("read view state after the failed migration: %v", err)
	}
	if viewsPresent != 0 {
		t.Logf("note: %d dependent views survived the failed migration", viewsPresent)
	}

	// Releasing the blocker has to be enough: the next run repairs everything the
	// failed one left behind, with no operator action beyond ending the query.
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("release blocking lock: %v", err)
	}
	released = true

	s.migrateLockTimeout = defaultMigrateLockTimeout
	s.migrateLockAttempts = defaultMigrateLockAttempts
	s.migrateLockRetryDelay = defaultMigrateLockRetryDelay
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate failed after the lock was released: %v", err)
	}

	// The repair is the part that matters, and it has to be complete: the rest of
	// the package reads these views, and so does the dashboard.
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_views WHERE schemaname = 'public' AND viewname = ANY($1)`,
		[]string{"unified_events", "visible_events", "visible_session_records", "visible_metrics"},
	).Scan(&viewsPresent); err != nil {
		t.Fatalf("read view state after recovery: %v", err)
	}
	if viewsPresent != 4 {
		t.Fatalf("only %d of the 4 dependent views came back after the recovering migration", viewsPresent)
	}
}

// TestExceptionBlocksDoNotSwallowLockTimeouts covers the way the lock timeout and
// the migrations interact, which is not obvious from either one alone.
//
// Nine migrations wrap themselves in DO ... EXCEPTION WHEN others so an already
// configured TimescaleDB feature cannot stop a boot. PL/pgSQL's `others` catches
// lock_not_available too, so introducing lock_timeout quietly turned "wait for
// the lock" into "skip this migration and report success". Measured before the
// fix: the bare ALTER raised 55P03, the same statement inside the DO block
// returned nil after the same 500ms.
//
// Five of the nine are the ones a data-table lock can actually block -- the three
// create_hypertable calls and the two compression settings. add_retention_policy
// and add_compression_policy only touch Timescale's config catalog and returned
// immediately against a locked hypertable when measured. The carve-out is on all
// nine anyway: the cost is a line each, and the rule "a catch-all handler lets
// contention through" is easier to keep than a list of which ones need it.
//
// The test runs the real shape from migrations.go against a really held lock.
func TestExceptionBlocksDoNotSwallowLockTimeouts(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()

	blocker, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire blocking connection: %v", err)
	}
	defer blocker.Release()
	tx, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatalf("begin blocking transaction: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, "LOCK TABLE otel_events IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatalf("take blocking lock: %v", err)
	}

	victim, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire victim connection: %v", err)
	}
	defer victim.Release()
	if _, err := victim.Exec(ctx, "SET lock_timeout = 500"); err != nil {
		t.Fatalf("set lock_timeout: %v", err)
	}
	defer victim.Exec(ctx, "RESET lock_timeout") //nolint:errcheck

	// The carve-out has to actually reach the caller, not just be present. The
	// statement inside is one that really needs ACCESS EXCLUSIVE: the migrations'
	// own DO blocks are no-ops on an already-configured database and would pass
	// here without proving anything.
	const shaped = `DO $$ BEGIN
		ALTER TABLE otel_events ADD COLUMN IF NOT EXISTS lock_carveout_probe int;
	` + exceptionCarveOut + `
		RAISE NOTICE 'skipped: %', SQLERRM;
	END $$`
	if _, err := victim.Exec(ctx, shaped); err == nil {
		t.Error("a DO block shaped like the migrations reported success while otel_events was locked -- the statement was skipped, not retried")
	} else if !isLockTimeout(err) {
		t.Errorf("DO block failed with something other than a lock timeout: %v", err)
	}

	// And every catch-all handler has to carry it. This is the half that catches a
	// handler added later: the behaviour above says the shape works, this says the
	// shape is used.
	//
	// Counted, not merely present. The carve-out belongs to a handler, not to a
	// statement -- PL/pgSQL runs the innermost one -- so a statement with one
	// carve-out and one nested bare `WHEN others` still swallows, and asking only
	// whether the carve-out appears anywhere would pass it.
	//
	// Only WHEN others is checked. The other handlers here name the condition they
	// expect -- duplicate_column, undefined_column -- and a named handler cannot
	// absorb an error nobody had it in mind for. WHEN others is the one that
	// widens on its own every time a new failure becomes possible, which is
	// exactly what happened when lock_timeout was introduced.
	//
	// All three groups, because Migrate runs all three.
	catchAll := 0
	for name, group := range map[string][]string{
		"dropDependentViews": dropDependentViews,
		"migrations":         migrations,
		"dependentViews":     dependentViews,
	} {
		for _, ddl := range group {
			handlers := strings.Count(ddl, "WHEN others")
			if handlers == 0 {
				continue
			}
			catchAll += handlers
			if guarded := strings.Count(ddl, exceptionCarveOut); guarded != handlers {
				t.Errorf("%s: %d of %d catch-all handlers carry the carve-out, so a blocked run skips this silently:\n%s",
					name, guarded, handlers, ddl)
			}
		}
	}
	if catchAll == 0 {
		t.Fatal("no statement with a WHEN others handler found; this test no longer covers anything")
	}
}

// exceptionCarveOut is the handler prefix every error-tolerating migration must
// open with. Kept here rather than in migrations.go so the test asserts against a
// literal instead of against whatever the source currently says.
const exceptionCarveOut = `EXCEPTION
	WHEN lock_not_available OR deadlock_detected OR object_in_use THEN
		RAISE;
	WHEN others THEN`

// TestTotalLockWaitBoundsTheWholeBoot covers the gap the per-statement count
// leaves open: 24 attempts bounds one statement at two minutes, but 209
// statements that each retry short of exhaustion is hours of a boot that never
// reports a problem. The whole-boot budget is what makes the retry policy an
// answer to "when does it give up" rather than only "how hard does it try".
func TestTotalLockWaitBoundsTheWholeBoot(t *testing.T) {
	w := &lockWait{budget: 10 * time.Second}

	// Nine statements, each waiting well short of its own per-statement limit.
	for i := 0; i < 9; i++ {
		w.add(time.Second)
		if w.exhausted() {
			t.Fatalf("budget reported exhausted after %s of %s", w.spent, w.budget)
		}
	}
	w.add(time.Second)
	if !w.exhausted() {
		t.Errorf("budget not exhausted at %s of %s: the boot can keep waiting forever", w.spent, w.budget)
	}
}

// TestTotalLockWaitOutlastsOneStatement keeps the two limits in the order that
// makes them mean different things. If the whole-boot budget were the smaller
// one, a single blocked statement would hit it first and the per-statement retry
// count would never be reached -- the boot would give up sooner than the policy
// it documents.
func TestTotalLockWaitOutlastsOneStatement(t *testing.T) {
	oneStatement := time.Duration(defaultMigrateLockAttempts) * (defaultMigrateLockTimeout + defaultMigrateLockRetryDelay)
	if totalMigrateLockWait <= oneStatement {
		t.Errorf("whole-boot budget %s does not outlast one statement's %s", totalMigrateLockWait, oneStatement)
	}
}

// TestMigrateActuallyRetries pins the retry loop itself.
//
// Every other assertion in this file is satisfied by an implementation that gives
// up on the first lock timeout: the error text, the elapsed bound, the view
// repair are all identical whether it tried once or twenty times. The retrying is
// the point of the change, so something has to fail when it stops happening.
func TestMigrateActuallyRetries(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()

	_, tx := holdSessionRecordsLock(t, s)

	restore := shrinkMigrateBudget(t, s, 200*time.Millisecond, 5, 50*time.Millisecond)
	defer restore()

	err := s.Migrate(ctx)
	if err == nil {
		t.Fatal("Migrate succeeded while session_records was locked")
	}
	if !strings.Contains(err.Error(), "after 5 attempts") {
		t.Errorf("Migrate gave up without using its retries -- the error should report 5 attempts:\n%s", err)
	}

	_ = tx.Rollback(ctx)
	restore()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate failed after the lock was released: %v", err)
	}
}

// TestMigrateStopsAtTheWholeBootBudget exercises the whole-boot cap through the
// real Migrate path. The struct-level test proves lockWait counts; this proves
// execMigration feeds it and honours it, which is the part a refactor drops.
func TestMigrateStopsAtTheWholeBootBudget(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()

	_, tx := holdSessionRecordsLock(t, s)

	// Attempts high enough that only the whole-boot budget can end this.
	restore := shrinkMigrateBudget(t, s, 200*time.Millisecond, 100, 50*time.Millisecond)
	defer restore()
	s.migrateLockWaitBudget = time.Second

	err := s.Migrate(ctx)
	if err == nil {
		t.Fatal("Migrate succeeded while session_records was locked")
	}
	if !strings.Contains(err.Error(), "lock-wait budget") {
		t.Errorf("Migrate ran out of attempts rather than out of its whole-boot budget:\n%s", err)
	}

	_ = tx.Rollback(ctx)
	s.migrateLockWaitBudget = totalMigrateLockWait
	restore()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate failed after the lock was released: %v", err)
	}
}

// TestMigrateReportsAStatementThatHasNotReturned covers the log gap.
//
// lock_timeout is applied per lock acquisition, not per statement, and hypertable
// DDL takes one lock per chunk, so the first failure -- and therefore the first
// retry line -- can be minutes away on production-sized tables. A boot that says
// "running database migrations" and then nothing is the symptom this change
// exists to remove, so progress cannot wait for a failure.
func TestMigrateReportsAStatementThatHasNotReturned(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()

	var captured bytes.Buffer
	restoreLog := swapLogOutput(&captured)
	defer restoreLog()

	_, tx := holdSessionRecordsLock(t, s)

	// One attempt, and a lock wait long enough for the watchdog to speak twice
	// before the statement fails.
	restore := shrinkMigrateBudget(t, s, 700*time.Millisecond, 1, time.Millisecond)
	defer restore()
	s.migrateProgressEvery = 200 * time.Millisecond
	defer func() { s.migrateProgressEvery = defaultMigrateProgressEvery }()

	if err := s.Migrate(ctx); err == nil {
		t.Fatal("Migrate succeeded while session_records was locked")
	}

	logged := captured.String()
	if !strings.Contains(logged, "still running") {
		t.Errorf("nothing was logged while the statement was in flight; a long first attempt is silent:\n%s", logged)
	}
	if !strings.Contains(logged, "ON session_records") {
		t.Errorf("the progress line does not say which statement is stuck:\n%s", logged)
	}

	_ = tx.Rollback(ctx)
	restore()
	s.migrateProgressEvery = defaultMigrateProgressEvery
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate failed after the lock was released: %v", err)
	}
}

// TestMigrateDoesNotReportItselfAsABlocker guards what the operator is told to
// kill.
//
// The diagnostics run on their own connection, so the query's pg_backend_pid()
// filter excludes the observer, not the migration. Left alone, the migration
// appears in its own "blocked by?" list -- and it is the entry that looks guilty:
// active, running a large DDL, while the real holder sits in "idle in
// transaction" showing whatever it last ran. The deployment guide tells operators
// to cancel the pid the log names; cancelling this one raises query_canceled,
// which is not retried, so the boot dies rather than recovers.
func TestMigrateDoesNotReportItselfAsABlocker(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()

	var captured bytes.Buffer
	restoreLog := swapLogOutput(&captured)
	defer restoreLog()

	_, tx := holdSessionRecordsLock(t, s)
	var blockerPID int
	if err := tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&blockerPID); err != nil {
		t.Fatalf("read blocking pid: %v", err)
	}

	// The progress log is the surface that shows this. By the time the failure
	// message is built the statement has returned, so the migration's backend is
	// idle and the query filters it out anyway; while it is running it is active,
	// and that is when it would name itself.
	restore := shrinkMigrateBudget(t, s, 700*time.Millisecond, 1, time.Millisecond)
	s.migrateProgressEvery = 200 * time.Millisecond

	if err := s.Migrate(ctx); err == nil {
		t.Fatal("Migrate succeeded while session_records was locked")
	}

	logged := captured.String()
	pids := map[int]bool{}
	for _, m := range regexp.MustCompile(`pid=(\d+)`).FindAllStringSubmatch(logged, -1) {
		pid, convErr := strconv.Atoi(m[1])
		if convErr != nil {
			t.Fatalf("unparseable pid %q in:\n%s", m[1], logged)
		}
		pids[pid] = true
	}
	if len(pids) == 0 {
		t.Fatalf("no blocking backend reported at all:\n%s", logged)
	}
	if !pids[blockerPID] {
		t.Errorf("the backend actually holding the lock (pid=%d) was never named:\n%s", blockerPID, logged)
	}
	delete(pids, blockerPID)
	if len(pids) != 0 {
		t.Errorf("the migration named %d backend(s) besides the real holder; the guide tells operators to cancel what is named here:\n%s", len(pids), logged)
	}

	_ = tx.Rollback(ctx)
	restore()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate failed after the lock was released: %v", err)
	}
}

// holdSessionRecordsLock takes ACCESS EXCLUSIVE from another session, which is
// what a long analytical query does to a migration's DDL.
func holdSessionRecordsLock(t *testing.T, s *PgStore) (*pgxpool.Conn, pgx.Tx) {
	t.Helper()
	ctx := context.Background()
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire blocking connection: %v", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		conn.Release()
		t.Fatalf("begin blocking transaction: %v", err)
	}
	if _, err := tx.Exec(ctx, "LOCK TABLE session_records IN ACCESS EXCLUSIVE MODE"); err != nil {
		conn.Release()
		t.Fatalf("take blocking lock: %v", err)
	}
	// The shared store is a package singleton, and a test that dies before its
	// own recovery leaves the next one without the dependent views. Cleanup runs
	// either way.
	//
	// Release is in here too, not in a defer at the call site: defers run before
	// Cleanup, and releasing a connection with an open transaction makes the pool
	// destroy it, which would leave the rollback below talking to a dead
	// connection. The lock does come off either way -- the backend closes -- but
	// only one of the two orders says what it means.
	t.Cleanup(func() {
		_ = tx.Rollback(ctx)
		conn.Release()
		if err := s.Migrate(ctx); err != nil {
			t.Errorf("could not restore the shared store after the test: %v", err)
		}
	})
	return conn, tx
}

// shrinkMigrateBudget makes the shared store give up quickly, and registers the
// restore so a t.Fatal between here and the caller's own restore cannot leave the
// package singleton on a test's budget.
func shrinkMigrateBudget(t *testing.T, s *PgStore, timeout time.Duration, attempts int, retryDelay time.Duration) (restore func()) {
	t.Helper()
	s.migrateLockTimeout, s.migrateLockAttempts, s.migrateLockRetryDelay = timeout, attempts, retryDelay
	restore = func() {
		s.migrateLockTimeout = defaultMigrateLockTimeout
		s.migrateLockAttempts = defaultMigrateLockAttempts
		s.migrateLockRetryDelay = defaultMigrateLockRetryDelay
		s.migrateLockWaitBudget = totalMigrateLockWait
		s.migrateProgressEvery = defaultMigrateProgressEvery
	}
	t.Cleanup(restore)
	return restore
}

func swapLogOutput(w io.Writer) (restore func()) {
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(w)
	return func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	}
}

// TestContendedCodesAreRetriedAndReRaisedTogether keeps the two halves of the
// contention contract in agreement.
//
// A code that the migrations' handlers re-raise but execMigration does not retry
// fails the boot on something the handler used to tolerate. A code that is
// retried but stays swallowed never reaches the retry at all. Both lists have to
// name the same errors, and the test states them so a change to one is visible.
func TestContendedCodesAreRetriedAndReRaisedTogether(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()

	victim, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer victim.Release()

	for code := range contendedLockCodes {
		// Raised through the shape the migrations use, so this measures the
		// handler rather than a paraphrase of it.
		ddl := `DO $$ BEGIN
			RAISE EXCEPTION 'synthetic' USING ERRCODE = '` + code + `';
		` + exceptionCarveOut + `
			RAISE NOTICE 'skipped: %', SQLERRM;
		END $$`
		_, err := victim.Exec(ctx, ddl)
		if err == nil {
			t.Errorf("SQLSTATE %s is retried by execMigration but swallowed by the migrations' handlers, so it never reaches the retry", code)
			continue
		}
		if !isContendedLockError(err) {
			t.Errorf("SQLSTATE %s came back as %v, which execMigration will not retry", code, err)
		}
	}

	// The other direction: an error that is genuinely about the statement must
	// still be tolerated by the handler, or every plain-Postgres install breaks.
	tolerated := `DO $$ BEGIN
		RAISE EXCEPTION 'synthetic feature missing' USING ERRCODE = '0A000';
	` + exceptionCarveOut + `
		RAISE NOTICE 'skipped: %', SQLERRM;
	END $$`
	if _, err := victim.Exec(ctx, tolerated); err != nil {
		t.Errorf("the carve-out now lets a feature-unsupported error through, which stops a boot that used to survive it: %v", err)
	}
}

// TestMigrateLockBudgetRefusesSubMillisecond covers the other way a timeout
// reaches Postgres as zero. SET lock_timeout takes milliseconds, so anything
// under 1ms rounds down to 0, and 0 means wait forever -- the original bug,
// reached through a value that looks set.
func TestMigrateLockBudgetRefusesSubMillisecond(t *testing.T) {
	timeout, _, _ := (&PgStore{migrateLockTimeout: 999 * time.Microsecond}).migrateLockBudget()
	if timeout.Milliseconds() == 0 {
		t.Errorf("timeout %s reaches Postgres as lock_timeout = 0, which waits forever", timeout)
	}
	if timeout != defaultMigrateLockTimeout {
		t.Errorf("sub-millisecond timeout became %s, want the default %s", timeout, defaultMigrateLockTimeout)
	}
}

// TestMigrateLockBudgetRefusesZero pins the fail-closed reading of an unset
// budget. Postgres treats lock_timeout = 0 as "wait forever", so a PgStore built
// without NewPgStore -- postgres_setup_isolation_test.go does exactly that --
// would otherwise reintroduce #613 while looking configured.
func TestMigrateLockBudgetRefusesZero(t *testing.T) {
	timeout, attempts, retryDelay := (&PgStore{}).migrateLockBudget()
	if timeout != defaultMigrateLockTimeout {
		t.Errorf("zero timeout became %s, want the default %s", timeout, defaultMigrateLockTimeout)
	}
	if attempts != defaultMigrateLockAttempts {
		t.Errorf("zero attempts became %d, want the default %d", attempts, defaultMigrateLockAttempts)
	}
	if retryDelay != defaultMigrateLockRetryDelay {
		t.Errorf("zero retry delay became %s, want the default %s", retryDelay, defaultMigrateLockRetryDelay)
	}

	configured := &PgStore{migrateLockTimeout: time.Second, migrateLockAttempts: 2, migrateLockRetryDelay: time.Millisecond}
	timeout, attempts, retryDelay = configured.migrateLockBudget()
	if timeout != time.Second || attempts != 2 || retryDelay != time.Millisecond {
		t.Errorf("configured budget was overridden: %s / %d / %s", timeout, attempts, retryDelay)
	}
}

// TestMigrateLeavesNoLockTimeoutOnPooledConnections guards the way the timeout is
// applied. lock_timeout is session state and the migration connection goes back
// into the pool, so setting it without clearing it would hand every later query a
// timeout that nothing in the code asked for.
func TestMigrateLeavesNoLockTimeoutOnPooledConnections(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Every connection the pool can hand out, so a single dirty one cannot hide
	// behind a clean one.
	for i := 0; i < int(s.pool.Config().MaxConns); i++ {
		conn, err := s.pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire connection %d: %v", i, err)
		}
		defer conn.Release()
		var setting string
		if err := conn.QueryRow(ctx, "SHOW lock_timeout").Scan(&setting); err != nil {
			t.Fatalf("read lock_timeout on connection %d: %v", i, err)
		}
		if setting != "0" {
			t.Errorf("connection %d came out of the pool with lock_timeout=%q, want the server default 0", i, setting)
		}
	}
}
