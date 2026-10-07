package store

import (
	"context"
	"testing"
	"time"
)

// Migrate runs on every boot. A worker rebuilding holds a row lock on the queue
// table for ~100s on production data; an ALTER on that table, even one with
// nothing to change, takes ACCESS EXCLUSIVE and waits it out -- past the boot's
// lock budget. An already-migrated table must not be locked at all.
func TestMigrateDoesNotLockAnUpToDateRebuildQueue(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if err := requestUsageRollupRebuild(ctx, s.pool, nil, []BillingAccountRef{{BillingProvider: "openai", AccountID: "acct-a"}}); err != nil {
		t.Fatalf("requestUsageRollupRebuild: %v", err)
	}

	holder, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer holder.Rollback(ctx) //nolint:errcheck // released below
	// What RunPendingUsageRollupRebuild's clearing DELETE holds until it commits.
	if _, err := holder.Exec(ctx, `DELETE FROM usage_rollup_rebuild_requests`); err != nil {
		t.Fatalf("hold the queue row: %v", err)
	}

	// Several attempts, not one: the lock wait covers every table Migrate touches,
	// and a transient lock conflict on a session_records chunk (e.g. autovacuum, or
	// a lock held by another test on the same DB) blocks CREATE INDEX there. A retry
	// rides that out; the queue lock held above does not release, so a Migrate that
	// waited on it fails every attempt. Only conflicts that clear within ~6s
	// (20 x 300ms) are absorbed; why autovacuum is not cancelled sooner (the 300ms
	// lock_timeout is under the default 1s deadlock_timeout) is an unmeasured hypothesis.
	restore := shrinkMigrateBudget(t, s, 300*time.Millisecond, 20, 10*time.Millisecond)
	defer restore()
	if err := s.Migrate(ctx); err != nil {
		t.Errorf("Migrate waited on the rebuild queue while a rebuild held it: %v", err)
	}
}

// A database that ran the build before release (dev) has the queue keyed on a
// start time. The guarded ALTERs have to bring it to the current shape, keeping
// its row for the worker.
func TestMigrateUpgradesTheFirstRebuildQueue(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	for _, stmt := range []string{
		`DROP TABLE usage_rollup_rebuild_requests`,
		`CREATE TABLE usage_rollup_rebuild_requests (
			id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
			from_ts TIMESTAMPTZ NOT NULL,
			generation BIGINT NOT NULL DEFAULT 1)`,
		`INSERT INTO usage_rollup_rebuild_requests (from_ts) VALUES (now())`,
	} {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	emails, accounts, ok := pendingRebuildIdentities(t, s)
	if !ok || len(emails) != 0 || len(accounts) != 0 {
		t.Errorf("queue after upgrade = %v %v (row kept %v), want the old row with no identities", emails, accounts, ok)
	}
	if err := requestUsageRollupRebuild(ctx, s.pool, nil, []BillingAccountRef{{BillingProvider: "openai", AccountID: "acct-a"}}); err != nil {
		t.Errorf("queueing on the upgraded table: %v", err)
	}
}
