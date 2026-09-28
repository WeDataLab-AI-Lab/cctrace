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

	restore := shrinkMigrateBudget(t, s, 300*time.Millisecond, 1, 10*time.Millisecond)
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
