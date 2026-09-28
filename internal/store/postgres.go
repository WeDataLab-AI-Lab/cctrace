package store

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PgStore implements Store with PostgreSQL/TimescaleDB.
type PgStore struct {
	pool       *pgxpool.Pool
	classifier TaskClassifier

	// How Migrate behaves when something else holds the lock it needs. Fields
	// rather than constants so the test that reproduces #613 does not have to
	// wait out the production budget; nothing outside this package sets them.
	migrateLockTimeout    time.Duration
	migrateLockAttempts   int
	migrateLockRetryDelay time.Duration
	migrateProgressEvery  time.Duration
	migrateLockWaitBudget time.Duration
}

// The lock budget for boot migrations, split into a short wait and many retries.
//
// A single long wait is the wrong shape. DDL like ADD COLUMN needs
// ACCESS EXCLUSIVE, which conflicts with the ROW EXCLUSIVE every ingest INSERT
// takes, so on a busy server the lock is routinely a second or two away -- and
// the queue matters as much as the wait: a waiting ACCESS EXCLUSIVE request
// blocks every transaction that queues behind it, so a long wait converts one
// stuck migration into a stalled database. Waiting briefly and stepping out of
// the queue between tries keeps ingest moving while the migration keeps asking.
//
// The attempt count is not a wall-clock budget. lock_timeout is applied per lock
// acquisition, and a hypertable statement takes one lock per chunk, so a single
// attempt against production session_records (41 chunks) can run far longer than
// the 3s below. totalMigrateLockWait is what bounds the clock; this bounds how
// many times to ask.
const (
	defaultMigrateLockTimeout    = 3 * time.Second
	defaultMigrateLockAttempts   = 24
	defaultMigrateLockRetryDelay = 2 * time.Second

	// How often a statement that has not returned yet says so.
	defaultMigrateProgressEvery = 15 * time.Second
)

// SetTaskClassifier injects the task_type classifier. Unset (nil), every user
// turn is left unclassified (task_type stays "") rather than panicking -- the
// same safe-degradation the classifier's own unknown-rule-match path already
// follows. Call this once, right after NewPgStore; see the isolation note on
// TaskClassifier for why only cmd/cctraced ever calls it.
func (s *PgStore) SetTaskClassifier(c TaskClassifier) {
	s.classifier = c
}

// NewPgStore creates a new PgStore from a connection string.
// Example DSN: "postgres://user:pass@localhost:5432/cctrace?sslmode=disable"
func NewPgStore(ctx context.Context, dsn string) (*PgStore, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.MaxConns = 10
	cfg.MinConns = 2

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &PgStore{
		pool:                  pool,
		migrateLockTimeout:    defaultMigrateLockTimeout,
		migrateLockAttempts:   defaultMigrateLockAttempts,
		migrateLockRetryDelay: defaultMigrateLockRetryDelay,
		migrateProgressEvery:  defaultMigrateProgressEvery,
		migrateLockWaitBudget: totalMigrateLockWait,
	}, nil
}

// Pool returns the underlying pgxpool for use by other packages (e.g., queue).
func (s *PgStore) Pool() *pgxpool.Pool {
	return s.pool
}

func (s *PgStore) Close() error {
	s.pool.Close()
	return nil
}

func (s *PgStore) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// Migrate creates tables and hypertables. Safe to call multiple times.
//
// The visible_* views are dropped first and recreated last. They are defined with
// SELECT *, which freezes the column list of what they read, so leaving them in
// place makes a later ADD/RENAME COLUMN fail at boot — and recreating them before
// the rest of the migrations run would leave them a boot behind the new columns.
//
// unified_events is dropped in that same first pass but recreated in the middle
// one, not last: it is what visible_events reads, so it has to exist again before
// the final pass. It is dropped rather than replaced in place because
// CREATE OR REPLACE VIEW only ever appends columns at the end, so any change to
// its column list other than an append fails on an existing database.
//
// Every statement runs on one connection held for the whole migration, because
// lock_timeout is session state: setting it on a pooled connection per statement
// would leave the next statement on whichever connection the pool handed out.
func (s *PgStore) Migrate(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migration: acquire connection: %w", err)
	}
	defer func() {
		// The connection goes back into the pool serving ordinary queries, and
		// none of them asked for a lock timeout. Reset on a fresh context so a
		// cancelled migration still hands back a clean session.
		resetCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(resetCtx, "RESET lock_timeout"); err != nil {
			// A connection that cannot be cleaned is worse than one fewer
			// connection: destroy it rather than return it carrying the setting.
			conn.Hijack().Close(resetCtx)
			return
		}
		conn.Release()
	}()

	// A zero field is not "no opinion" here: Postgres reads lock_timeout = 0 as
	// wait forever, which is exactly the bug. Any PgStore built without
	// NewPgStore gets the real budget rather than the old unbounded wait.
	timeout, attempts, retryDelay := s.migrateLockBudget()

	if _, err := conn.Exec(ctx, fmt.Sprintf("SET lock_timeout = %d", timeout.Milliseconds())); err != nil {
		return fmt.Errorf("migration: set lock_timeout: %w", err)
	}

	// The diagnostics run on their own connection, so pg_backend_pid() there no
	// longer excludes this one. Without this the migration lists itself among the
	// backends that might be holding the lock -- and it looks the most guilty of
	// them: active, running a large DDL, while the real holder sits in
	// "idle in transaction" showing whatever it last executed. The guide tells
	// operators to cancel the pid the log names, and cancelling this one raises
	// query_canceled, which is not retried, so the boot dies on the spot.
	var selfPID int
	if err := conn.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&selfPID); err != nil {
		return fmt.Errorf("migration: read backend pid: %w", err)
	}

	// Spent across every statement, not per statement. Twenty-four attempts each
	// bounds one statement at two minutes but bounds the migration at nothing:
	// 209 statements that each retry 23 times and then succeed is 6.7 hours of a
	// boot that never reports a problem. The per-statement count says how hard to
	// try; this says how long the whole boot is allowed to spend waiting.
	budget := s.migrateLockWaitBudget
	if budget <= 0 {
		budget = totalMigrateLockWait
	}
	spent := &lockWait{budget: budget}

	for _, group := range [][]string{dropDependentViews, migrations, dependentViews} {
		for _, ddl := range group {
			if err := s.execMigration(ctx, conn, ddl, timeout, attempts, retryDelay, spent, selfPID); err != nil {
				return err
			}
		}
	}
	return nil
}

// totalMigrateLockWait caps the lock waiting summed over the whole migration.
// Deliberately not a deadline on the migration itself: a first migration on a
// large database legitimately spends a long time building indexes, and cutting
// that off would break the case this code is not about.
const totalMigrateLockWait = 5 * time.Minute

// lockWait accumulates time lost to lock contention.
type lockWait struct {
	budget time.Duration
	spent  time.Duration
}

func (w *lockWait) add(d time.Duration) { w.spent += d }
func (w *lockWait) exhausted() bool     { return w.spent >= w.budget }

// migrateLockBudget answers with the configured budget, substituting the default
// for any field left at zero.
func (s *PgStore) migrateLockBudget() (timeout time.Duration, attempts int, retryDelay time.Duration) {
	timeout, attempts, retryDelay = s.migrateLockTimeout, s.migrateLockAttempts, s.migrateLockRetryDelay
	// Below a millisecond the value rounds to zero on the way into SET, and
	// Postgres reads a zero lock_timeout as wait forever. Fail closed there too.
	if timeout < time.Millisecond {
		timeout = defaultMigrateLockTimeout
	}
	if attempts <= 0 {
		attempts = defaultMigrateLockAttempts
	}
	if retryDelay <= 0 {
		retryDelay = defaultMigrateLockRetryDelay
	}
	return timeout, attempts, retryDelay
}

// execMigration runs one statement, retrying while the only thing wrong is that
// someone else holds the lock.
func (s *PgStore) execMigration(ctx context.Context, conn *pgxpool.Conn, ddl string, timeout time.Duration, attempts int, retryDelay time.Duration, spent *lockWait, selfPID int) error {
	migrationStart := time.Now()
	for attempt := 1; ; attempt++ {
		started := time.Now()
		stopWatch := s.watchStatement(ctx, ddl, attempt, attempts, selfPID)
		_, err := conn.Exec(ctx, ddl)
		stopWatch()
		if err == nil {
			return nil
		}
		if !isContendedLockError(err) {
			return fmt.Errorf("migration: %w\nSQL: %s", err, ddl)
		}
		spent.add(time.Since(started))

		switch {
		case attempt >= attempts:
			return fmt.Errorf("migration: gave up after %d attempts over %s: %w\nSQL: %s\n%s",
				attempt, time.Since(migrationStart).Round(time.Second), err, ddl, s.lockHoldersFor(ctx, selfPID, ddl))
		case spent.exhausted():
			return fmt.Errorf("migration: this boot has spent %s of its %s lock-wait budget: %w\nSQL: %s\n%s",
				spent.spent.Round(time.Second), spent.budget, err, ddl, s.lockHoldersFor(ctx, selfPID, ddl))
		}

		log.Printf("[store] migration could not take a lock (attempt %d/%d, %s of the %s boot budget spent, retrying in %s): %v\n  SQL: %s\n%s",
			attempt, attempts, spent.spent.Round(time.Second), spent.budget, retryDelay, err, firstLine(ddl), s.lockHoldersFor(ctx, selfPID, ddl))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retryDelay):
		}
		spent.add(retryDelay)
	}
}

// watchStatement reports on a statement while it is still running, and returns
// the function that stops it.
//
// Without this the log can go silent for minutes. lock_timeout is applied per
// lock acquisition, not per statement, and a hypertable statement takes one lock
// per chunk: production session_records has 41 chunks, so a single ALTER can
// spend 42 x 3s and a CREATE INDEX 84 x 3s before the first attempt even fails.
// The retry line only prints after that. A boot that says "running database
// migrations" and then nothing for four minutes is the symptom this whole change
// exists to remove, so the progress report cannot wait for the first failure.
//
// It also covers the case that has nothing to do with locks: a first migration
// on a large database legitimately spends minutes building an index, and saying
// so beats looking stalled.
func (s *PgStore) watchStatement(ctx context.Context, ddl string, attempt, attempts int, selfPID int) (stop func()) {
	// Read here, not in the goroutine. The caller owns these fields and sets them
	// between migrations in tests, so a goroutine that outlives the call and then
	// reads them is a data race -- one go test -race reports and go test does not.
	every := s.migrateProgressEvery
	if every <= 0 {
		every = defaultMigrateProgressEvery
	}

	done, exited := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(exited)
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		started := time.Now()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				log.Printf("[store] migration statement still running after %s (attempt %d/%d)\n  SQL: %s\n%s",
					time.Since(started).Round(time.Second), attempt, attempts, firstLine(ddl), s.lockHoldersFor(ctx, selfPID, ddl))
			}
		}
	}()
	// Waits, so a watchdog caught mid-report cannot print "still running" after
	// the statement returned, and so the caller's next write to those fields
	// happens after this goroutine is done with them.
	return func() {
		close(done)
		<-exited
	}
}

// isLockTimeout reports whether lock_timeout expired (SQLSTATE 55P03,
// lock_not_available), as opposed to any other contention.
func isLockTimeout(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "55P03"
}

// contendedLockCodes are the failures that say "someone else had it, ask again"
// rather than "this statement is wrong". Each one is both retried here and
// re-raised out of the migrations' own EXCEPTION handlers; the two have to agree,
// because a code that escapes a handler without being retried fails the boot on
// something the handler used to tolerate.
//
//	55P03 lock_not_available  lock_timeout expired
//	40P01 deadlock_detected   Postgres picked us as the victim. Retrying is the
//	                          documented response, and TimescaleDB's retention and
//	                          compression jobs touch the same catalogs a migration
//	                          does, in their own order.
//	55006 object_in_use       the view or table is held by another session; the
//	                          DROP/ALTER passes once it lets go.
var contendedLockCodes = map[string]bool{
	"55P03": true,
	"40P01": true,
	"55006": true,
}

func isContendedLockError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && contendedLockCodes[pgErr.Code]
}

// lockHolders describes the backends that could be holding the lock, so the log
// says who to talk to instead of only that the wait happened. Reported
// best-effort: a migration must not fail because its own diagnostics did.
//
// It lists long-running and in-transaction backends rather than resolving the
// exact blocker: pg_blocking_pids only answers for a backend that is still
// waiting, and by the time lock_timeout has fired this one no longer is.
//
// It takes its own connection rather than reusing the migration's: the progress
// watchdog runs while that one is mid-statement, and a pgx connection is not
// safe for concurrent use.
// lockHolders reports without a target table, for callers that have no DDL to
// name. Prefer lockHoldersFor: naming the table is what separates the session
// blocking this migration from one that merely happens to be busy.
func (s *PgStore) lockHolders(ctx context.Context, selfPID int) string {
	return s.lockHoldersFor(ctx, selfPID, "")
}

// lockHoldersFor ranks candidates by whether they hold a lock on the table ddl
// is waiting for.
//
// "Holds any relation lock" was the first attempt and is too coarse: every
// session that has read any table qualifies, so a few busy but unrelated
// connections refill the limit and push the holder out -- the omission this
// function exists to prevent, under a narrower condition. To a role without
// pg_read_all_stats there is nothing else left to rank by, because every hidden
// row's xact_start is NULL.
func (s *PgStore) lockHoldersFor(ctx context.Context, selfPID int, ddl string) string {
	target := tableNameIn(ddl)
	const q = `
		SELECT pid,
		       state,
		       COALESCE(EXTRACT(EPOCH FROM (now() - xact_start))::bigint, 0),
		       left(regexp_replace(query, '\s+', ' ', 'g'), 160)
		       ,EXISTS (SELECT 1 FROM pg_locks l
		                WHERE l.pid = a.pid AND l.granted
		                  AND l.relation = to_regclass($2)) AS holds_the_target
		       ,EXISTS (SELECT 1 FROM pg_locks l
		                WHERE l.pid = a.pid AND l.granted
		                  AND l.relation IS NOT NULL) AS holds_a_relation_lock
		FROM pg_stat_activity a
		WHERE datname = current_database()
		  AND pid <> pg_backend_pid()
		  AND pid <> $1
		  -- IS DISTINCT FROM, not <>: a role without pg_read_all_stats sees NULL
		  -- for another session's state, and NULL <> 'idle' is NULL, which drops
		  -- the row. The report then says no backend is active while one holds the
		  -- lock -- the opposite of the truth, from the diagnostic that exists
		  -- because a stuck boot gave no reason at all (#613). The pid survives
		  -- even when the query text does not, and the pid is what a person needs.
		  AND state IS DISTINCT FROM 'idle'
		  -- An autovacuum worker can hold a lock that conflicts with this DDL, but
		  -- it is never the reason a boot stays stuck: Postgres cancels an
		  -- autovacuum that blocks a competing lock request. The guide tells an
		  -- operator to cancel what this report names, so naming a worker that is
		  -- already yielding sends them after the wrong process -- and leaves the
		  -- one actually holding the lock unread.
		  --
		  -- This slips past the state test rather than being caught by it: the
		  -- IS DISTINCT FROM above exists so a redacted NULL state still reports
		  -- a pid, and NULL IS DISTINCT FROM 'idle' is true, so a worker with no
		  -- readable state qualifies. datname excludes the shared background
		  -- processes (checkpointer, walwriter) because theirs is NULL; an
		  -- autovacuum worker has the database set.
		  --
		  -- IS DISTINCT FROM, not = 'client backend'. Measured: a role without
		  -- pg_read_all_stats reads backend_type as NULL for another session,
		  -- exactly as it reads state. A positive test would therefore drop the
		  -- backend actually holding the lock and report that nothing was
		  -- active -- the #613 failure again, from the fix for it. Written this
		  -- way the filter fails open: when backend_type cannot be read the row
		  -- survives, so the report is noisier rather than wrong. That case is
		  -- not hypothetical, since #26 moves cctraced onto a non-superuser role.
		  AND backend_type IS DISTINCT FROM 'autovacuum worker'
		-- pg_locks is the only column that still ranks these rows once
		-- pg_stat_activity is redacted, and the target table is what makes the
		-- ranking mean something. to_regclass returns NULL for an unknown or
		-- empty name, which matches nothing, so a caller with no DDL falls back
		-- to the coarse ordering rather than erroring.
		ORDER BY holds_the_target DESC, holds_a_relation_lock DESC, xact_start NULLS LAST
		LIMIT 6`

	queryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	conn, err := s.pool.Acquire(queryCtx)
	if err != nil {
		return fmt.Sprintf("  (could not read pg_stat_activity: %v)", err)
	}
	defer conn.Release()

	rows, err := conn.Query(queryCtx, q, selfPID, target)
	if err != nil {
		return fmt.Sprintf("  (could not read pg_stat_activity: %v)", err)
	}
	defer rows.Close()

	var b strings.Builder
	shown, truncated := 0, false
	for rows.Next() {
		var pid int
		// Nullable on purpose. The same role that cannot read another session's
		// state gets NULL rather than a masked string, and a non-nullable
		// destination turns that into a scan error -- which this function reports
		// as "could not read pg_stat_activity", losing the pid it did read.
		var state, query *string
		var ageSeconds int64
		var holdsTarget, holdsLock bool
		if err := rows.Scan(&pid, &state, &ageSeconds, &query, &holdsTarget, &holdsLock); err != nil {
			return fmt.Sprintf("  (could not read pg_stat_activity: %v)", err)
		}
		// The sixth row is read only to learn that it exists. Reporting it would
		// widen the list; saying nothing would make a truncated report read as a
		// complete one, and a reader who does not find the holder in a complete
		// list concludes it is not there.
		shown++
		if shown > lockHolderReportLimit {
			truncated = true
			continue
		}
		// Says what each row holds, because in a report made entirely of hidden
		// rows that is the only thing separating a suspect from a bystander.
		holds := "no"
		if holdsTarget {
			holds = "the table this waits on"
		} else if holdsLock {
			holds = "some other relation"
		}
		fmt.Fprintf(&b, "  blocked by? pid=%d holds=%s state=%s txn_age=%ds query=%s\n",
			pid, holds, orHidden(state), ageSeconds, orHidden(query))
	}
	if err := rows.Err(); err != nil {
		return fmt.Sprintf("  (could not read pg_stat_activity: %v)", err)
	}
	if b.Len() == 0 {
		return "  (no other active backend on this database -- the holder may have finished, or it is idle in a transaction on another database)"
	}
	out := strings.TrimRight(b.String(), "\n")
	if truncated {
		out += "\n  (more candidates not listed)"
	}
	return out
}

// lockHolderReportLimit is how many candidates the report names. One more than
// this is fetched, so the report can say it was truncated.
const lockHolderReportLimit = 5

// tableNameIn pulls the table a DDL statement operates on, or "" when it names
// none. Only the shapes internal/store/migrations.go actually uses.
func tableNameIn(ddl string) string {
	fields := strings.Fields(strings.ReplaceAll(ddl, "\n", " "))
	for i, f := range fields {
		switch strings.ToUpper(f) {
		case "TABLE", "INDEX", "VIEW":
			for _, next := range fields[i+1:] {
				upper := strings.ToUpper(next)
				if upper == "IF" || upper == "NOT" || upper == "EXISTS" || upper == "CONCURRENTLY" || upper == "ONLY" {
					continue
				}
				return strings.Trim(next, `"();,`)
			}
		}
	}
	return ""
}

// orHidden renders a column pg_stat_activity withheld. It says which it is
// rather than printing an empty field, because "state=" reads as a value the
// server gave and "(hidden)" reads as a grant this role is missing.
func orHidden(v *string) string {
	if v == nil {
		return "(hidden)"
	}
	return *v
}

// firstLine keeps the retry log readable: the statement is repeated in full only
// once, in the error that ends the migration.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " ..."
	}
	return s
}
