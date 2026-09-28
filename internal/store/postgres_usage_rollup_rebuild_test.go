package store

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
	"time"
)

// runPendingUsageRollupRebuild stands in for the server's rebuild worker: an
// exclusion change only queues the usage rebuild, so a test that checks the
// charts after one has to run it.
func runPendingUsageRollupRebuild(t *testing.T, s *PgStore) {
	t.Helper()
	if _, err := s.RunPendingUsageRollupRebuild(context.Background()); err != nil {
		t.Fatalf("RunPendingUsageRollupRebuild: %v", err)
	}
}

// pendingRebuildIdentities reads the queued request: the addresses and billing
// accounts whose usage has to be rebuilt. ok is false when nothing is queued.
func pendingRebuildIdentities(t *testing.T, s *PgStore) (emails []string, accounts []BillingAccountRef, ok bool) {
	t.Helper()
	ctx := context.Background()
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM usage_rollup_rebuild_requests`).Scan(&n); err != nil {
		t.Fatalf("count rebuild requests: %v", err)
	}
	if n == 0 {
		return nil, nil, false
	}
	var raw []byte
	if err := s.pool.QueryRow(ctx,
		`SELECT emails, accounts FROM usage_rollup_rebuild_requests`).Scan(&emails, &raw); err != nil {
		t.Fatalf("read rebuild request: %v", err)
	}
	if err := json.Unmarshal(raw, &accounts); err != nil {
		t.Fatalf("decode accounts %s: %v", raw, err)
	}
	sort.Strings(emails)
	sort.Slice(accounts, func(i, j int) bool {
		return accounts[i].BillingProvider+"/"+accounts[i].AccountID < accounts[j].BillingProvider+"/"+accounts[j].AccountID
	})
	return emails, accounts, true
}

func seedOldRollups(t *testing.T, s *PgStore) {
	t.Helper()
	ctx := context.Background()
	seedPersonalAndTeamCodex(t, s, time.Now().UTC().Add(-10*24*time.Hour))
	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{personalLinkSample("acct-personal", "personal@example.test")}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
	if err := s.RefreshUsageHourlyRollups(ctx, nil); err != nil {
		t.Fatalf("RefreshUsageHourlyRollups: %v", err)
	}
}

// The rebuild took ~100s on production data and ran inside the exclude request:
// a client that gave up cancelled it after the exclusion had committed, and the
// old buckets kept their pre-exclusion cost. The request now only records what
// has to be rebuilt.
func TestExclusionQueuesTheUsageRebuildInsteadOfRunningIt(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	seedOldRollups(t, s)

	if _, err := s.ExcludeAccount(ctx, "personal@example.test", "non-company account", "admin"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}
	if got := codexRollupEvents(t, s); got != 2 {
		t.Errorf("rollup counts %d codex events right after the exclusion, want the untouched 2", got)
	}
	pending, err := s.UsageRollupRebuildPending(ctx)
	if err != nil {
		t.Fatalf("UsageRollupRebuildPending: %v", err)
	}
	if !pending {
		t.Error("no usage rebuild is pending after an exclusion change")
	}
}

// Finding where the rebuild starts is a full scan of unified_events -- tens of
// seconds on production data, and the request has to answer inside the server's
// 30s write timeout. So the change records who is affected, not from when: the
// queued row holds the identities, the changed address and the account it links.
func TestExclusionQueuesIdentitiesNotATimestamp(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	seedOldRollups(t, s)

	if _, err := s.ExcludeAccount(ctx, "Personal@Example.test", "non-company account", "admin"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}
	emails, accounts, ok := pendingRebuildIdentities(t, s)
	if !ok {
		t.Fatal("no rebuild request queued")
	}
	if len(emails) != 1 || emails[0] != "personal@example.test" {
		t.Errorf("queued emails = %v, want the excluded address, lowercased", emails)
	}
	if len(accounts) != 1 || accounts[0] != (BillingAccountRef{BillingProvider: "openai", AccountID: "acct-personal"}) {
		t.Errorf("queued accounts = %+v, want the billing account the address links", accounts)
	}
}

// Requests merge into one row holding the union of what each named.
func TestUsageRebuildRequestsMergeIntoTheUnionOfIdentities(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	a := BillingAccountRef{BillingProvider: "openai", AccountID: "acct-a"}
	b := BillingAccountRef{BillingProvider: "anthropic", AccountID: "acct-b"}
	for _, req := range []struct {
		emails   []string
		accounts []BillingAccountRef
	}{
		{[]string{"one@example.test"}, nil},
		{[]string{"Two@example.test", "one@example.test"}, []BillingAccountRef{a}},
		{nil, []BillingAccountRef{a, b}},
	} {
		if err := requestUsageRollupRebuild(ctx, s.pool, req.emails, req.accounts); err != nil {
			t.Fatalf("requestUsageRollupRebuild: %v", err)
		}
	}
	emails, accounts, ok := pendingRebuildIdentities(t, s)
	if !ok {
		t.Fatal("no rebuild request queued")
	}
	if len(emails) != 2 || emails[0] != "one@example.test" || emails[1] != "two@example.test" {
		t.Errorf("queued emails = %v, want each address once, lowercased", emails)
	}
	if len(accounts) != 2 || accounts[0] != b || accounts[1] != a {
		t.Errorf("queued accounts = %+v, want each account once", accounts)
	}
}

// The worker finds where the rebuild starts from the queued identities -- the
// excluded rows are ten days old, past the periodic window -- rebuilds from
// there, and leaves nothing pending behind it.
func TestRunningThePendingUsageRebuildFixesOldBucketsAndClearsIt(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	seedOldRollups(t, s)

	if _, err := s.ExcludeAccount(ctx, "personal@example.test", "non-company account", "admin"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}
	ran, err := s.RunPendingUsageRollupRebuild(ctx)
	if err != nil {
		t.Fatalf("RunPendingUsageRollupRebuild: %v", err)
	}
	if !ran {
		t.Error("the pending rebuild did not run")
	}
	if got := codexRollupEvents(t, s); got != 1 {
		t.Errorf("rollup counts %d codex events after the rebuild, want 1", got)
	}
	if pending, err := s.UsageRollupRebuildPending(ctx); err != nil || pending {
		t.Errorf("pending = %v (%v) after the rebuild ran, want false", pending, err)
	}
	if ran, err := s.RunPendingUsageRollupRebuild(ctx); err != nil || ran {
		t.Errorf("second run = %v (%v), want nothing left to do", ran, err)
	}
}

// Several cctraced instances each run the worker. One already rebuilding holds
// the writers' lock; a second must not queue up behind it to repeat the same
// ~100s rebuild, and must leave the request for whoever finishes.
func TestUsageRebuildSkipsWhileAnotherWriterHoldsTheLock(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	seedOldRollups(t, s)
	if _, err := s.ExcludeAccount(ctx, "personal@example.test", "non-company account", "admin"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}

	holder, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer holder.Rollback(ctx) //nolint:errcheck // released below
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('usage_hourly_rollups_writers'))`); err != nil {
		t.Fatalf("take writers lock: %v", err)
	}

	type result struct {
		ran bool
		err error
	}
	done := make(chan result, 1)
	go func() {
		ran, err := s.RunPendingUsageRollupRebuild(ctx)
		done <- result{ran, err}
	}()
	select {
	case r := <-done:
		if r.err != nil || r.ran {
			t.Errorf("run while another writer holds the lock = %v (%v), want a skip", r.ran, r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the run waited for the other writer instead of skipping")
	}
	if pending, err := s.UsageRollupRebuildPending(ctx); err != nil || !pending {
		t.Errorf("pending = %v (%v) after a skipped run, want the request kept", pending, err)
	}

	if err := holder.Rollback(ctx); err != nil {
		t.Fatalf("release writers lock: %v", err)
	}
	if ran, err := s.RunPendingUsageRollupRebuild(ctx); err != nil || !ran {
		t.Errorf("run after the lock was released = %v (%v), want it to run", ran, err)
	}
}

// A change that commits while a rebuild is running may not be in what that
// rebuild read. Clearing the request at the end would drop it for good.
func TestUsageRebuildRequestArrivingDuringARunIsKept(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	seedOldRollups(t, s)

	personal := []BillingAccountRef{{BillingProvider: "openai", AccountID: "acct-personal"}}
	if err := requestUsageRollupRebuild(ctx, s.pool, nil, personal); err != nil {
		t.Fatalf("requestUsageRollupRebuild: %v", err)
	}

	// Block the rebuild's writes so the run stops inside its rebuild, after it
	// has taken the writers' lock and read the request.
	holder, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer holder.Rollback(ctx) //nolint:errcheck // released below
	if _, err := holder.Exec(ctx, `LOCK TABLE usage_hourly_rollups IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatalf("lock usage_hourly_rollups: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.RunPendingUsageRollupRebuild(ctx)
		done <- err
	}()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var waiting bool
		if err := s.pool.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM pg_locks
			               WHERE relation = 'usage_hourly_rollups'::regclass AND NOT granted)`).Scan(&waiting); err != nil {
			t.Fatalf("read pg_locks: %v", err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("the run finished (err=%v) without reaching its rebuild", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the run never reached its rebuild")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := requestUsageRollupRebuild(ctx, s.pool, []string{"later@example.test"}, nil); err != nil {
		t.Fatalf("requestUsageRollupRebuild during the run: %v", err)
	}
	if err := holder.Rollback(ctx); err != nil {
		t.Fatalf("release usage_hourly_rollups: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunPendingUsageRollupRebuild: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the run never finished")
	}

	pending, err := s.UsageRollupRebuildPending(ctx)
	if err != nil {
		t.Fatalf("UsageRollupRebuildPending: %v", err)
	}
	if !pending {
		t.Error("a request made during a run was cleared by that run")
	}
}

// A row queued by a build before release carries no identities. Clearing it
// without a rebuild would drop the rebuild it stood for; without knowing whose,
// only a full rebuild is sure to reach every bucket.
func TestRebuildRequestNamingNoOneRebuildsEverything(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	seedOldRollups(t, s)
	if _, err := s.ExcludeAccount(ctx, "personal@example.test", "non-company account", "admin"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE usage_rollup_rebuild_requests SET emails = '{}', accounts = '[]'`); err != nil {
		t.Fatalf("empty the queued identities: %v", err)
	}

	runPendingUsageRollupRebuild(t, s)
	if got := codexRollupEvents(t, s); got != 1 {
		t.Errorf("rollup counts %d codex events after running a request that names no one, want 1", got)
	}
	if pending, err := s.UsageRollupRebuildPending(ctx); err != nil || pending {
		t.Errorf("pending = %v (%v) after the run, want false", pending, err)
	}
}
