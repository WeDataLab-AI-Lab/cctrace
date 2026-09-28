package store

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// The boot diagnostic reports which session is holding the lock a migration is
// waiting on. It reads pg_stat_activity, and that view hides other sessions from
// a role without pg_read_all_stats: not just the query text, but `state`, which
// comes back NULL.
//
// The filter was `state <> 'idle'`. NULL <> 'idle' is NULL, which is not true,
// so every hidden session was dropped from the result -- and the diagnostic then
// reported no holder at all while a holder had the table locked. That is worse
// than losing the detail: #613 exists because a stuck boot gave no reason, and a
// confident "nobody is blocking" sends the next person somewhere else entirely.
//
// This matters now because #26 moves the app off the superuser role.
func TestLockDiagnosticKeepsSessionsItCannotRead(t *testing.T) {
	super := acquireTestStore(t)
	ctx := context.Background()

	// A role shaped like the one #26 introduces, deliberately without
	// pg_read_all_stats so other sessions are hidden from it.
	for _, stmt := range []string{
		`DROP ROLE IF EXISTS diagprobe`,
		`CREATE ROLE diagprobe LOGIN PASSWORD 'diagprobe' NOSUPERUSER`,
		`GRANT USAGE ON SCHEMA public TO diagprobe`,
	} {
		if _, err := super.pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		_, _ = super.pool.Exec(context.Background(), `DROP ROLE IF EXISTS diagprobe`)
	})

	dsn := strings.Replace(pgSharedDSN, "test:test@", "diagprobe:diagprobe@", 1)
	if dsn == pgSharedDSN {
		t.Fatalf("could not build a diagprobe DSN from %q", pgSharedDSN)
	}
	limited, err := NewPgStore(ctx, dsn)
	if err != nil {
		t.Fatalf("connect as the unprivileged role: %v", err)
	}
	t.Cleanup(func() { _ = limited.Close() })

	// Confirm the premise rather than assuming it: this role really cannot read
	// other sessions. Without this the test could pass on a build where the role
	// is privileged after all, proving nothing.
	var hidden bool
	if err := limited.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_stat_activity
		                WHERE pid <> pg_backend_pid() AND state IS NULL)`).Scan(&hidden); err != nil {
		t.Fatalf("probe visibility: %v", err)
	}
	if !hidden {
		t.Skip("this server does not hide other sessions from a plain role")
	}

	conn, tx := holdSessionRecordsLock(t, super)
	defer func() {
		_ = tx.Rollback(context.Background())
		conn.Release()
	}()

	// conn, not super.pool: the pool hands out whichever connection is free, so
	// asking it for a backend pid answers about some other session entirely. The
	// first version asked the pool, and the assertion below then pinned a pid
	// that had nothing to do with the lock.
	var holderPID int
	if err := conn.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatalf("read holder pid: %v", err)
	}

	got := limited.lockHoldersFor(ctx, 0, "ALTER TABLE session_records ADD COLUMN probe TEXT")
	if strings.TrimSpace(got) == "" {
		t.Fatal("the diagnostic reported no lock holder while a session held the table locked")
	}
	// The query text is genuinely unavailable to this role. The pid is not, and it
	// is what makes the report actionable -- it is what a person types into
	// pg_terminate_backend.
	if !strings.Contains(got, "pid="+strconv.Itoa(holderPID)) {
		t.Fatalf("the holder's pid %d is missing, so nothing can be looked up: %q", holderPID, got)
	}
}

// Keeping hidden sessions in the result is only half the job. To a role without
// pg_read_all_stats every hidden row has xact_start NULL, so the ORDER BY has
// nothing to rank them by, and LIMIT 5 can fill up with idle connections that
// hold nothing -- pushing the actual lock holder out of a report whose entire
// purpose is to name it.
func TestLockDiagnosticPrefersTheActualHolder(t *testing.T) {
	super := acquireTestStore(t)
	ctx := context.Background()

	for _, stmt := range []string{
		`DROP ROLE IF EXISTS diagcrowd`,
		`CREATE ROLE diagcrowd LOGIN PASSWORD 'diagcrowd' NOSUPERUSER`,
		`GRANT USAGE ON SCHEMA public TO diagcrowd`,
	} {
		if _, err := super.pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	t.Cleanup(func() { _, _ = super.pool.Exec(context.Background(), `DROP ROLE IF EXISTS diagcrowd`) })

	dsn := strings.Replace(pgSharedDSN, "test:test@", "diagcrowd:diagcrowd@", 1)
	limited, err := NewPgStore(ctx, dsn)
	if err != nil {
		t.Fatalf("connect as the unprivileged role: %v", err)
	}
	t.Cleanup(func() { _ = limited.Close() })

	var hidden bool
	if err := limited.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_stat_activity
		                WHERE pid <> pg_backend_pid() AND state IS NULL)`).Scan(&hidden); err != nil {
		t.Fatalf("probe visibility: %v", err)
	}
	if !hidden {
		t.Skip("this server does not hide other sessions from a plain role")
	}

	// More idle connections than the report has room for.
	for i := 0; i < 8; i++ {
		conn, err := super.pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("open idle connection %d: %v", i, err)
		}
		defer conn.Release()
		if _, err := conn.Exec(ctx, `SELECT 1`); err != nil {
			t.Fatal(err)
		}
	}

	conn, tx := holdSessionRecordsLock(t, super)
	defer func() {
		_ = tx.Rollback(context.Background())
		conn.Release()
	}()
	var holderPID int
	if err := conn.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatalf("read holder pid: %v", err)
	}

	got := limited.lockHolders(ctx, 0)
	if !strings.Contains(got, "pid="+strconv.Itoa(holderPID)) {
		t.Fatalf("the holder pid %d was crowded out by sessions that hold nothing:\n%s", holderPID, got)
	}
}

// Ranking by "holds any relation lock" was still too coarse. Every session that
// has read any table qualifies, so a handful of busy but unrelated connections
// refill the limit and push the holder out again -- the same omission as before,
// under a narrower condition.
//
// What separates a suspect from a bystander is the table the migration is
// actually waiting on, and the caller knows which one that is.
func TestLockDiagnosticPrefersTheHolderOfTheTargetTable(t *testing.T) {
	super := acquireTestStore(t)
	ctx := context.Background()

	for _, stmt := range []string{
		`DROP ROLE IF EXISTS diagtarget`,
		`CREATE ROLE diagtarget LOGIN PASSWORD 'diagtarget' NOSUPERUSER`,
		`GRANT USAGE ON SCHEMA public TO diagtarget`,
	} {
		if _, err := super.pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	t.Cleanup(func() { _, _ = super.pool.Exec(context.Background(), `DROP ROLE IF EXISTS diagtarget`) })

	dsn := strings.Replace(pgSharedDSN, "test:test@", "diagtarget:diagtarget@", 1)
	limited, err := NewPgStore(ctx, dsn)
	if err != nil {
		t.Fatalf("connect as the unprivileged role: %v", err)
	}
	t.Cleanup(func() { _ = limited.Close() })

	var hidden bool
	if err := limited.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_stat_activity
		                WHERE pid <> pg_backend_pid() AND state IS NULL)`).Scan(&hidden); err != nil {
		t.Fatalf("probe visibility: %v", err)
	}
	if !hidden {
		t.Skip("this server does not hide other sessions from a plain role")
	}

	// Sessions holding locks on other tables: busy, hidden, and irrelevant.
	for i := 0; i < 8; i++ {
		conn, err := super.pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("open decoy %d: %v", i, err)
		}
		defer conn.Release()
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, err := tx.Exec(ctx, `LOCK TABLE otel_events IN ACCESS SHARE MODE`); err != nil {
			t.Fatal(err)
		}
	}

	conn, tx := holdSessionRecordsLock(t, super)
	defer func() {
		_ = tx.Rollback(context.Background())
		conn.Release()
	}()
	var holderPID int
	if err := conn.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatalf("read holder pid: %v", err)
	}

	got := limited.lockHoldersFor(ctx, 0, "ALTER TABLE session_records ADD COLUMN probe TEXT")
	if !strings.Contains(got, "pid="+strconv.Itoa(holderPID)) {
		t.Fatalf("the holder of session_records was crowded out by locks on other tables:\n%s", got)
	}
}

// A report that silently drops candidates reads as a complete list. When it is
// not one, it has to say so -- otherwise the reader concludes the holder is not
// among them.
func TestLockDiagnosticSaysWhenItTruncated(t *testing.T) {
	super := acquireTestStore(t)
	ctx := context.Background()

	for i := 0; i < 9; i++ {
		conn, err := super.pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("open session %d: %v", i, err)
		}
		defer conn.Release()
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, err := tx.Exec(ctx, `LOCK TABLE otel_events IN ACCESS SHARE MODE`); err != nil {
			t.Fatal(err)
		}
	}

	got := super.lockHoldersFor(ctx, 0, "ALTER TABLE session_records ADD COLUMN probe TEXT")
	if !strings.Contains(got, "more") {
		t.Fatalf("nine candidates reported without saying any were left out:\n%s", got)
	}
}
