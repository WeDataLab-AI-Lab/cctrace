package store

import (
	"bytes"
	"context"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// An autovacuum worker can hold a lock that conflicts with a migration's DDL,
// but it is never the reason a boot stays stuck -- Postgres cancels an
// autovacuum that blocks a competing lock request. The guide tells an operator
// to cancel what this report names, so a worker that is already yielding sends
// them after the wrong process.
//
// It slips past the state test rather than being caught by it: IS DISTINCT FROM
// exists so a redacted NULL state still yields a pid, and NULL IS DISTINCT FROM
// 'idle' is true. datname keeps the shared background processes out because
// theirs is NULL; an autovacuum worker has the database set.
//
// This asserts the contract rather than staging a worker: forcing autovacuum to
// run inside the window is timing-dependent, and a test that only sometimes
// exercises its subject is worse than one that always checks the property. Every
// pid the report names must belong to a client backend.
func TestLockReportNamesOnlyClientBackends(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()

	var captured bytes.Buffer
	restoreLog := swapLogOutput(&captured)
	defer restoreLog()

	_, tx := holdSessionRecordsLock(t, s)
	defer func() { _ = tx.Rollback(ctx) }()

	restore := shrinkMigrateBudget(t, s, 700*time.Millisecond, 1, time.Millisecond)
	s.migrateProgressEvery = 200 * time.Millisecond
	defer restore()

	if err := s.Migrate(ctx); err == nil {
		t.Fatal("Migrate returned nil while session_records was locked")
	}

	logged := captured.String()
	pids := map[int]bool{}
	for _, m := range regexp.MustCompile(`pid=(\d+)`).FindAllStringSubmatch(logged, -1) {
		pid, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("unparseable pid %q in:\n%s", m[1], logged)
		}
		pids[pid] = true
	}
	if len(pids) == 0 {
		t.Fatalf("no blocking backend reported at all:\n%s", logged)
	}

	for pid := range pids {
		var backendType *string
		err := s.pool.QueryRow(ctx,
			`SELECT backend_type FROM pg_stat_activity WHERE pid = $1`, pid).Scan(&backendType)
		if err != nil {
			// The backend ended between the report and this read. That is the
			// normal race and says nothing about the filter.
			continue
		}
		if backendType != nil && *backendType != "client backend" {
			t.Errorf("the report named pid=%d, a %q; an operator told to cancel it would be chasing the wrong process:\n%s",
				pid, *backendType, logged)
		}
	}
}

// The exclusion must not cost the report the backend that is actually holding
// the lock -- a filter that quietly drops the answer is worse than the noise it
// removes.
func TestLockReportStillNamesTheRealHolder(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()

	var captured bytes.Buffer
	restoreLog := swapLogOutput(&captured)
	defer restoreLog()

	_, tx := holdSessionRecordsLock(t, s)
	defer func() { _ = tx.Rollback(ctx) }()
	var blockerPID int
	if err := tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&blockerPID); err != nil {
		t.Fatalf("read blocking pid: %v", err)
	}

	restore := shrinkMigrateBudget(t, s, 700*time.Millisecond, 1, time.Millisecond)
	s.migrateProgressEvery = 200 * time.Millisecond
	defer restore()

	if err := s.Migrate(ctx); err == nil {
		t.Fatal("Migrate returned nil while session_records was locked")
	}

	if !regexp.MustCompile(`pid=` + strconv.Itoa(blockerPID) + `\b`).MatchString(captured.String()) {
		t.Errorf("the backend holding the lock (pid=%d) was never named:\n%s", blockerPID, captured.String())
	}
}
