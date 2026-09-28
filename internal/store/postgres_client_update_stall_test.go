package store

import (
	"context"
	"testing"
	"time"
)

// A client that cannot reach a new release records the failure locally and
// nowhere else, so the only server-side signal is a version that stops moving
// while the account keeps syncing (#750). These tests pin the reported form.

func TestPgStore_UpsertClientVersion_recordsUpdateStall(t *testing.T) {
	// Given
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	firstFailed := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Second)
	lastFailed := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)

	// When
	if err := s.UpsertClientVersion(ctx, ClientVersionUpdate{
		UserID: "stalled", ProfileEmail: "stalled@example.com", ClientVersion: "v0.7.14",
		UpdateStall: &ClientUpdateStall{
			TargetVersion: "v0.7.56",
			Consecutive:   9,
			FirstFailedAt: firstFailed,
			LastFailedAt:  lastFailed,
			Reason:        "rename /usr/local/bin/cctrace: permission denied",
		},
	}); err != nil {
		t.Fatalf("upsert stalled client: %v", err)
	}

	// Then
	var reported bool
	var target, reason string
	var count int
	var first, last time.Time
	if err := s.pool.QueryRow(ctx, `
		SELECT update_reported, update_target_version, update_fail_count,
		       update_first_failed_at, update_last_failed_at, update_fail_reason
		FROM client_versions WHERE profile_email = 'stalled@example.com'`,
	).Scan(&reported, &target, &count, &first, &last, &reason); err != nil {
		t.Fatalf("read update columns: %v", err)
	}
	if !reported {
		t.Fatal("update_reported = false, want true (this client told us about its update state)")
	}
	if target != "v0.7.56" || count != 9 {
		t.Fatalf("target/count = %q/%d, want v0.7.56/9", target, count)
	}
	if !first.UTC().Equal(firstFailed) || !last.UTC().Equal(lastFailed) {
		t.Fatalf("first/last = %v/%v, want %v/%v", first.UTC(), last.UTC(), firstFailed, lastFailed)
	}
	if reason != "rename /usr/local/bin/cctrace: permission denied" {
		t.Fatalf("reason = %q, want the client's own message", reason)
	}
}

// A client that reports "no failure" must clear what an earlier report left, or
// a resolved stall stays on the screen until someone asks the person about it.
func TestPgStore_UpsertClientVersion_clearsUpdateStallOnSuccess(t *testing.T) {
	// Given
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if err := s.UpsertClientVersion(ctx, ClientVersionUpdate{
		UserID: "fixed", ProfileEmail: "fixed@example.com", ClientVersion: "v0.7.14",
		UpdateStall: &ClientUpdateStall{
			TargetVersion: "v0.7.56", Consecutive: 4,
			FirstFailedAt: time.Now().UTC().Add(-48 * time.Hour),
			LastFailedAt:  time.Now().UTC().Add(-time.Hour),
			Reason:        "checksum mismatch",
		},
	}); err != nil {
		t.Fatalf("seed stall: %v", err)
	}

	// When: the update lands and the client reports a clean state.
	if err := s.UpsertClientVersion(ctx, ClientVersionUpdate{
		UserID: "fixed", ProfileEmail: "fixed@example.com", ClientVersion: "v0.7.56",
		UpdateStall: &ClientUpdateStall{},
	}); err != nil {
		t.Fatalf("upsert cleared stall: %v", err)
	}

	// Then
	var reported bool
	var target, reason string
	var count int
	var first, last *time.Time
	if err := s.pool.QueryRow(ctx, `
		SELECT update_reported, update_target_version, update_fail_count,
		       update_first_failed_at, update_last_failed_at, update_fail_reason
		FROM client_versions WHERE profile_email = 'fixed@example.com'`,
	).Scan(&reported, &target, &count, &first, &last, &reason); err != nil {
		t.Fatalf("read update columns: %v", err)
	}
	if !reported {
		t.Fatal("update_reported = false, want true (a clean report is still a report)")
	}
	if count != 0 || target != "" || reason != "" {
		t.Fatalf("count/target/reason = %d/%q/%q, want 0/\"\"/\"\" (the stall is over)", count, target, reason)
	}
	if first != nil || last != nil {
		t.Fatalf("first/last = %v/%v, want NULL", first, last)
	}
}

// The trap #455 left behind: a client too old to report anything must not read
// as "reported no failure". Silence and a clean report are different facts, and
// only one of them means the account is fine.
func TestPgStore_UpsertClientVersion_leavesUpdateStallAloneWhenUnreported(t *testing.T) {
	// Given
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if err := s.UpsertClientVersion(ctx, ClientVersionUpdate{
		UserID: "quiet", ProfileEmail: "quiet@example.com", ClientVersion: "v0.7.14",
		UpdateStall: &ClientUpdateStall{
			TargetVersion: "v0.7.56", Consecutive: 3,
			FirstFailedAt: time.Now().UTC().Add(-24 * time.Hour),
			LastFailedAt:  time.Now().UTC().Add(-time.Hour),
			Reason:        "checksum mismatch",
		},
	}); err != nil {
		t.Fatalf("seed stall: %v", err)
	}

	// When: a sync arrives from a build that does not know about this field.
	if err := s.UpsertClientVersion(ctx, ClientVersionUpdate{
		UserID: "quiet", ProfileEmail: "quiet@example.com", ClientVersion: "v0.7.14",
	}); err != nil {
		t.Fatalf("upsert without a report: %v", err)
	}

	// Then
	var target string
	var count int
	if err := s.pool.QueryRow(ctx,
		`SELECT update_target_version, update_fail_count FROM client_versions WHERE profile_email = 'quiet@example.com'`,
	).Scan(&target, &count); err != nil {
		t.Fatalf("read update columns: %v", err)
	}
	if target != "v0.7.56" || count != 3 {
		t.Fatalf("target/count = %q/%d, want v0.7.56/3 (a silent sync erases nothing)", target, count)
	}

	// And a fresh account that never reports stays marked as never having reported.
	if err := s.UpsertClientVersion(ctx, ClientVersionUpdate{
		UserID: "old", ProfileEmail: "old@example.com", ClientVersion: "v0.6.9",
	}); err != nil {
		t.Fatalf("upsert old client: %v", err)
	}
	var reported bool
	if err := s.pool.QueryRow(ctx,
		`SELECT update_reported FROM client_versions WHERE profile_email = 'old@example.com'`,
	).Scan(&reported); err != nil {
		t.Fatalf("read update_reported: %v", err)
	}
	if reported {
		t.Fatal("update_reported = true for a client that said nothing, want false")
	}
}

// The screen reads ListClientVersions, so the stall has to survive that query
// too -- the columns existing is not the same as anyone being able to see them.
func TestPgStore_ListClientVersions_carriesUpdateStall(t *testing.T) {
	// Given
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
		Ts: time.Now().UTC(), SessionID: "stall-session", RecordType: "user",
		ProfileEmail: "listed@example.com", UUID: "stall-1",
	}}); err != nil {
		t.Fatalf("seed active account: %v", err)
	}
	firstFailed := time.Now().UTC().Add(-96 * time.Hour).Truncate(time.Second)
	if err := s.UpsertClientVersion(ctx, ClientVersionUpdate{
		UserID: "listed", ProfileEmail: "listed@example.com", ClientVersion: "v0.7.42",
		UpdateStall: &ClientUpdateStall{
			TargetVersion: "v0.7.56", Consecutive: 12,
			FirstFailedAt: firstFailed,
			LastFailedAt:  time.Now().UTC().Add(-time.Hour).Truncate(time.Second),
			Reason:        "open /usr/local/bin/cctrace: read-only file system",
		},
	}); err != nil {
		t.Fatalf("seed stall: %v", err)
	}

	// When
	rows, err := s.ListClientVersions(ctx)
	if err != nil {
		t.Fatalf("ListClientVersions: %v", err)
	}

	// Then
	var got *ClientVersionRecord
	for _, r := range rows {
		if r.ProfileEmail == "listed@example.com" {
			got = r
		}
	}
	if got == nil {
		t.Fatal("listed@example.com missing from ListClientVersions")
	}
	if !got.UpdateReported {
		t.Fatal("UpdateReported = false, want true")
	}
	if got.UpdateTargetVersion != "v0.7.56" || got.UpdateFailCount != 12 {
		t.Fatalf("target/count = %q/%d, want v0.7.56/12", got.UpdateTargetVersion, got.UpdateFailCount)
	}
	if got.UpdateFirstFailedAt == nil || !got.UpdateFirstFailedAt.UTC().Equal(firstFailed) {
		t.Fatalf("UpdateFirstFailedAt = %v, want %v", got.UpdateFirstFailedAt, firstFailed)
	}
	if got.UpdateFailReason != "open /usr/local/bin/cctrace: read-only file system" {
		t.Fatalf("UpdateFailReason = %q, want the client's own message", got.UpdateFailReason)
	}
}
