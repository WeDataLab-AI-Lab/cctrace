package store

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
)

// A signed policy names its recipients one by one, as (installation id, profile)
// pairs, and a client rejects a policy whose `from` list does not hold the
// endpoints it uses now. Both values lived only on the device, so an operator had
// to collect them by hand before writing a policy and could not see afterwards
// who had applied it. These tests pin what the server keeps of a device's report.

func TestPgStore_UpsertClientPolicyReport_keepsOneRowPerTarget(t *testing.T) {
	// Given
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	id := uuid.NewString()

	// When: the device reports while its policy is deferred, ...
	if err := s.UpsertClientPolicyReport(ctx, ClientPolicyReport{
		InstallationID: id, Profile: "work", ProfileEmail: "stalled@example.com", UserID: "stalled",
		SyncEndpoint: "http://cctrace.company.example:4318", OTELEndpoint: "http://cctrace.company.example:4317", Protocol: "grpc",
		CheckResult: "deferred", CheckSequence: 3, CheckReason: `shared Codex settings with profile "home"; policy deferred`,
		ClientVersion: "v0.7.62",
	}); err != nil {
		t.Fatalf("first report: %v", err)
	}
	// (age the row so the second write is visibly later without sleeping)
	if _, err := s.pool.Exec(ctx, `UPDATE client_policy_reports
		SET first_seen_at = now() - interval '1 day', last_seen_at = now() - interval '1 day'`); err != nil {
		t.Fatalf("age row: %v", err)
	}
	// ... and again after the policy applied. The second report carries no version
	// header, as a request from a build with an empty version string would.
	if err := s.UpsertClientPolicyReport(ctx, ClientPolicyReport{
		InstallationID: id, Profile: "work", ProfileEmail: "stalled@example.com", UserID: "stalled",
		SyncEndpoint: "https://cctrace.company.example:8443", OTELEndpoint: "https://cctrace.company.example:4318", Protocol: "http/protobuf",
		AppliedSequence: 3, CheckResult: "ok",
	}); err != nil {
		t.Fatalf("second report: %v", err)
	}

	// Then: one row, holding the latest state and the original first-seen time.
	var rows int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM client_policy_reports`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Fatalf("rows = %d, want 1 (same installation id and profile)", rows)
	}
	var syncEndpoint, otelEndpoint, protocol, result, reason, version string
	var applied, attempted int64
	var first, last time.Time
	if err := s.pool.QueryRow(ctx, `SELECT sync_endpoint, otel_endpoint, protocol, applied_sequence,
			check_result, check_sequence, check_reason, client_version, first_seen_at, last_seen_at
		FROM client_policy_reports WHERE installation_id = $1 AND profile = 'work'`, id).
		Scan(&syncEndpoint, &otelEndpoint, &protocol, &applied, &result, &attempted, &reason, &version, &first, &last); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if syncEndpoint != "https://cctrace.company.example:8443" || otelEndpoint != "https://cctrace.company.example:4318" || protocol != "http/protobuf" {
		t.Errorf("endpoints = %q %q %q, want the second report's", syncEndpoint, otelEndpoint, protocol)
	}
	if applied != 3 || result != "ok" || attempted != 0 || reason != "" {
		t.Errorf("state = applied %d, %q, attempted %d, reason %q; want 3, ok, 0, empty", applied, result, attempted, reason)
	}
	// An absent header says nothing about the build, so it must not erase what the
	// device reported before -- the rule client_versions follows for the same header.
	if version != "v0.7.62" {
		t.Errorf("client_version = %q, want the earlier v0.7.62 kept", version)
	}
	if age := time.Since(first); age < 23*time.Hour {
		t.Errorf("first_seen_at moved to %v ago; a later report must not reset it", age)
	}
	if age := time.Since(last); age > time.Hour {
		t.Errorf("last_seen_at is %v old; a report must refresh it", age)
	}
}

func TestPgStore_UpsertClientPolicyReport_separatesProfilesOfOneInstall(t *testing.T) {
	// Given: one device, whose installation id is shared by all of its profiles.
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	id := uuid.NewString()

	// When: the default profile (empty name) and a named one report.
	for _, profile := range []string{"", "work"} {
		if err := s.UpsertClientPolicyReport(ctx, ClientPolicyReport{
			InstallationID: id, Profile: profile, ProfileEmail: "stalled@example.com",
			SyncEndpoint: "http://cctrace.company.example:4318", OTELEndpoint: "http://cctrace.company.example:4317", Protocol: "grpc",
			CheckResult: "ok",
		}); err != nil {
			t.Fatalf("report for profile %q: %v", profile, err)
		}
	}

	// Then: each is its own policy target.
	var rows int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM client_policy_reports WHERE installation_id = $1`, id).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 2 {
		t.Fatalf("rows = %d, want 2 (one per profile)", rows)
	}
}

func TestPgStore_UpsertClientPolicyReport_refusesSequenceBeyondBigint(t *testing.T) {
	// Given
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	// When: a sequence that does not fit the column arrives.
	err := s.UpsertClientPolicyReport(ctx, ClientPolicyReport{
		InstallationID: uuid.NewString(), CheckResult: "ok", AppliedSequence: math.MaxInt64 + 1,
	})

	// Then: it is an error here rather than a wrapped negative number in the row.
	if err == nil {
		t.Fatal("upsert accepted a sequence above the BIGINT range")
	}
}

// A report names an account and carries the client's own error text, which names
// local paths. Deleting a user's data has to reach it by either key the delete
// takes, and leave other accounts' targets alone.
func TestDeleteUserData_removesClientPolicyReports(t *testing.T) {
	// Given
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	report := func(email, userID string) string {
		t.Helper()
		id := uuid.NewString()
		if err := s.UpsertClientPolicyReport(ctx, ClientPolicyReport{
			InstallationID: id, ProfileEmail: email, UserID: userID,
			CheckResult: "failed", CheckReason: "open /Users/alice/.cctrace/config.json: permission denied",
		}); err != nil {
			t.Fatalf("report for %q: %v", email, err)
		}
		return id
	}
	gone, kept, orphan := report("gone@example.com", "u-gone"), report("kept@example.com", "u-kept"), report("", "u-orphan")
	left := func(id string) int {
		t.Helper()
		return countWhere(t, s, "client_policy_reports", "installation_id = $1", id)
	}

	// When / Then: by profile email, ...
	if err := s.DeleteUserData(ctx, "gone@example.com", ""); err != nil {
		t.Fatalf("DeleteUserData: %v", err)
	}
	if left(gone) != 0 {
		t.Error("the deleted profile's report is still there")
	}
	// ... and by user_id, the function's other key, which also reaches a report
	// that named no profile.
	if err := s.DeleteUserData(ctx, "", "u-orphan"); err != nil {
		t.Fatalf("DeleteUserData by user_id: %v", err)
	}
	if left(orphan) != 0 {
		t.Error("the deleted user_id's report is still there")
	}
	if left(kept) != 1 {
		t.Error("another account's report was deleted")
	}
}

// The key is the target, not the account, so a merge only renames the account a
// report names and leaves every other column as it was. The user_id form claims
// only reports with no profile yet, as it does in every other table.
func TestMergeUsers_movesClientPolicyReports(t *testing.T) {
	// Given
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	report := func(email, userID string, sequence uint64) string {
		t.Helper()
		id := uuid.NewString()
		if err := s.UpsertClientPolicyReport(ctx, ClientPolicyReport{
			InstallationID: id, Profile: "work", ProfileEmail: email, UserID: userID,
			SyncEndpoint: "http://cctrace.company.example:4318", OTELEndpoint: "http://cctrace.company.example:4317", Protocol: "grpc",
			AppliedSequence: sequence, CheckResult: "deferred", CheckSequence: sequence + 1, CheckReason: "from " + email,
			ClientVersion: "v0.7.62",
		}); err != nil {
			t.Fatalf("report for %q: %v", email, err)
		}
		return id
	}
	old, target := report("old@example.com", "u-1", 1), report("new@example.com", "u-2", 2)
	orphan, elsewhere := report("", "u-orphan", 3), report("elsewhere@example.com", "u-orphan", 4)
	// Every column but the one a merge may change.
	row := func(id string) string {
		t.Helper()
		var r string
		if err := s.pool.QueryRow(ctx, `SELECT ROW(installation_id, profile, user_id, sync_endpoint, otel_endpoint,
				protocol, applied_sequence, check_result, check_sequence, check_reason, client_version,
				first_seen_at, last_seen_at)::text
			FROM client_policy_reports WHERE installation_id = $1`, id).Scan(&r); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		return r
	}
	email := func(id string) string {
		t.Helper()
		var e string
		if err := s.pool.QueryRow(ctx, `SELECT profile_email FROM client_policy_reports
			WHERE installation_id = $1`, id).Scan(&e); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		return e
	}
	ids := []string{old, target, orphan, elsewhere}
	before := map[string]string{}
	for _, id := range ids {
		before[id] = row(id)
	}

	// When
	if err := s.MergeUsers(ctx, "old@example.com", "", "new@example.com"); err != nil {
		t.Fatalf("MergeUsers: %v", err)
	}
	if err := s.MergeUsers(ctx, "", "u-orphan", "new@example.com"); err != nil {
		t.Fatalf("MergeUsers by user_id: %v", err)
	}

	// Then
	for id, want := range map[string]string{
		old:       "new@example.com",
		orphan:    "new@example.com",
		elsewhere: "elsewhere@example.com", // already named a profile
		target:    "new@example.com",
	} {
		if got := email(id); got != want {
			t.Errorf("report %s is under %q, want %q", before[id], got, want)
		}
	}
	for _, id := range ids {
		if got := row(id); got != before[id] {
			t.Errorf("the merge changed more than the account:\n got %s\nwant %s", got, before[id])
		}
	}
}
