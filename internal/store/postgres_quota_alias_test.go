package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// The API server resolves aliases from a cache refreshed every 30 seconds, so a
// report that arrives just after a merge still carries the address the merge
// removed. The write looks the alias up itself, in the table the merge wrote it
// to, and puts the reading under the target's row.
func TestUpsertQuotaSnapshot_writesUnderTheAliasTarget(t *testing.T) {
	// Given: a merge that has just run, and the target's own earlier reading.
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `INSERT INTO quota_snapshots (profile_email, user_id, five_hour_pct)
		VALUES ('new@example.com', 'u-new', 10)`); err != nil {
		t.Fatalf("seed the target's reading: %v", err)
	}
	if err := s.MergeUsers(ctx, "old@example.com", "", "new@example.com"); err != nil {
		t.Fatalf("MergeUsers: %v", err)
	}

	// When: the merged-away profile reports again, in another case, and another
	// profile, with no alias, reports as it always has.
	if err := s.UpsertQuotaSnapshot(ctx, &QuotaSnapshot{ProfileEmail: "Old@Example.com", UserID: "u-old", FiveHourPct: 55}); err != nil {
		t.Fatalf("UpsertQuotaSnapshot: %v", err)
	}
	if err := s.UpsertQuotaSnapshot(ctx, &QuotaSnapshot{ProfileEmail: "Plain@Example.com", UserID: "u-plain", FiveHourPct: 5}); err != nil {
		t.Fatalf("UpsertQuotaSnapshot (no alias): %v", err)
	}

	// Then: the reading replaced the target's row, and the other one kept its
	// address as received.
	rows, err := s.pool.Query(ctx, `SELECT profile_email, user_id, five_hour_pct FROM quota_snapshots
		ORDER BY profile_email COLLATE "C"`)
	if err != nil {
		t.Fatalf("read quota_snapshots: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var email, userID string
		var pct float64
		if err := rows.Scan(&email, &userID, &pct); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, fmt.Sprintf("%s|%s|%v", email, userID, pct))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"Plain@Example.com|u-plain|5", "new@example.com|u-old|55"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("quota_snapshots rows = %v, want %v", got, want)
	}
}

// A merge deletes the source's reading and writes the alias in one transaction. A
// report that read the alias table just before that commit would write the
// source's row back after the delete, so the write waits for the merge, as the
// other writers of the data a merge moves do.
func TestUpsertQuotaSnapshot_waitsForAMergeInProgress(t *testing.T) {
	// Given: a merge that holds its lock and has written the alias, not committed.
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	merge, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer merge.Rollback(context.Background()) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, merge, true); err != nil {
		t.Fatal(err)
	}
	if _, err := merge.Exec(ctx, `INSERT INTO user_aliases (from_profile_email, to_profile_email)
		VALUES ('old@example.com', 'new@example.com')`); err != nil {
		t.Fatalf("write the alias: %v", err)
	}

	// When: the merged-away profile reports.
	done := make(chan error, 1)
	go func() {
		done <- s.UpsertQuotaSnapshot(ctx, &QuotaSnapshot{ProfileEmail: "Old@Example.com", UserID: "u-old", FiveHourPct: 55})
	}()
	for {
		var waiting int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND wait_event = 'advisory'
			AND query LIKE '%pg_advisory_xact_lock_shared%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("the report did not wait for the merge: %v", err)
		case <-ctx.Done():
			t.Fatal("the report never waited")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := merge.Commit(ctx); err != nil {
		t.Fatalf("commit the merge: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("UpsertQuotaSnapshot: %v", err)
	}

	// Then: it read the alias the merge committed.
	var old, target int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE lower(profile_email) = 'old@example.com'),
		count(*) FILTER (WHERE profile_email = 'new@example.com') FROM quota_snapshots`).Scan(&old, &target); err != nil {
		t.Fatal(err)
	}
	if old != 0 || target != 1 {
		t.Fatalf("rows under the merged-away address = %d, under the target = %d; want 0 and 1", old, target)
	}
}
