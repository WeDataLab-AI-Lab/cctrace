package store

import (
	"context"
	"slices"
	"testing"
)

// seedQuotaSnapshot writes one quota_snapshots row as ingest would, so a test
// names the row's key and user_id and nothing else.
func seedQuotaSnapshot(t *testing.T, s *PgStore, email, userID string) {
	t.Helper()
	if err := s.UpsertQuotaSnapshot(context.Background(), &QuotaSnapshot{ProfileEmail: email, UserID: userID, FiveHourPct: 10}); err != nil {
		t.Fatalf("UpsertQuotaSnapshot(%s): %v", email, err)
	}
}

// A quota snapshot names an account by the address the client reported, and a
// privacy setting by the address the dashboard account signed in with. Neither
// is normalized on write, so the address a delete is given can differ from the
// stored one in case alone. Deleting a user's data has to reach both by either
// key the delete takes, and leave other accounts' rows alone.
func TestDeleteUserData_removesQuotaSnapshotsAndPrivacySettings(t *testing.T) {
	// Given
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	seedQuotaSnapshot(t, s, "Gone@Example.com", "u-gone")
	seedQuotaSnapshot(t, s, "by-id@example.com", "u-by-id")
	seedQuotaSnapshot(t, s, "kept@example.com", "u-kept")
	for _, p := range []struct{ userID, email, scopeType, scopeValue string }{
		{"u-gone", "Gone@Example.com", "user", ""},
		{"", "gone@example.com", "project", "p1"},
		{"u-by-id", "by-id@example.com", "user", ""},
		{"u-kept", "kept@example.com", "user", ""},
	} {
		if err := s.SetPrivacy(ctx, p.userID, p.email, p.scopeType, p.scopeValue); err != nil {
			t.Fatalf("SetPrivacy(%s): %v", p.email, err)
		}
	}

	// When / Then: by profile email, whatever the case the rows were stored in, ...
	if err := s.DeleteUserData(ctx, "gone@example.com", ""); err != nil {
		t.Fatalf("DeleteUserData: %v", err)
	}
	if n := countWhere(t, s, "quota_snapshots", "lower(profile_email) = 'gone@example.com'"); n != 0 {
		t.Errorf("the deleted profile's quota snapshot is still there (%d rows)", n)
	}
	if n := countWhere(t, s, "privacy_settings", "lower(profile_email) = 'gone@example.com'"); n != 0 {
		t.Errorf("the deleted profile's privacy settings are still there (%d rows)", n)
	}
	// ... and by user_id, the function's other key.
	if err := s.DeleteUserData(ctx, "", "u-by-id"); err != nil {
		t.Fatalf("DeleteUserData by user_id: %v", err)
	}
	if n := countWhere(t, s, "quota_snapshots", "user_id = 'u-by-id'"); n != 0 {
		t.Errorf("the deleted user_id's quota snapshot is still there (%d rows)", n)
	}
	if n := countWhere(t, s, "privacy_settings", "user_id = 'u-by-id'"); n != 0 {
		t.Errorf("the deleted user_id's privacy settings are still there (%d rows)", n)
	}
	if n := countWhere(t, s, "quota_snapshots", "profile_email = 'kept@example.com'"); n != 1 {
		t.Error("another account's quota snapshot was deleted")
	}
	if n := countWhere(t, s, "privacy_settings", "profile_email = 'kept@example.com'"); n != 1 {
		t.Error("another account's privacy settings were deleted")
	}
}

// quotaRows lists the profile_email of every quota_snapshots row, sorted, so a
// test can say which rows a merge left.
func quotaRows(t *testing.T, s *PgStore) []string {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), `SELECT profile_email FROM quota_snapshots`)
	if err != nil {
		t.Fatalf("read quota_snapshots: %v", err)
	}
	defer rows.Close()
	var emails []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			t.Fatalf("scan: %v", err)
		}
		emails = append(emails, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	slices.Sort(emails) // the database's collation is not byte order
	return emails
}

// The snapshot is the latest reading, written again every few minutes, so a
// merge does not carry the source's row over: it would only put a second row,
// under whatever case the merge target was typed in, beside the one the target's
// own reports keep filling. It deletes the source's row, in any case, and leaves
// the target's.
func TestMergeUsers_removesSourceQuotaSnapshot(t *testing.T) {
	// Given
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	seedQuotaSnapshot(t, s, "Old@Example.com", "u-old")
	seedQuotaSnapshot(t, s, "new@example.com", "u-new")
	seedQuotaSnapshot(t, s, "kept@example.com", "u-kept")
	if _, err := s.pool.Exec(ctx, `UPDATE quota_snapshots SET five_hour_pct = 77 WHERE profile_email = 'new@example.com'`); err != nil {
		t.Fatalf("mark the target's row: %v", err)
	}

	// When
	if err := s.MergeUsers(ctx, "old@example.com", "", "new@example.com"); err != nil {
		t.Fatalf("MergeUsers: %v", err)
	}

	// Then
	if got, want := quotaRows(t, s), []string{"kept@example.com", "new@example.com"}; !slices.Equal(got, want) {
		t.Fatalf("quota_snapshots rows = %v, want %v", got, want)
	}
	if n := countWhere(t, s, "quota_snapshots", "profile_email = 'new@example.com' AND five_hour_pct = 77"); n != 1 {
		t.Error("the merge changed the target's own snapshot")
	}

	// And a merge into the same address is no merge, whatever case it is in.
	if err := s.MergeUsers(ctx, "new@example.com", "", "new@example.com"); err != nil {
		t.Fatalf("MergeUsers into itself: %v", err)
	}
	if n := countWhere(t, s, "quota_snapshots", "profile_email = 'new@example.com'"); n != 1 {
		t.Error("merging an address into itself deleted its snapshot")
	}
}

// The user_id form claims only the rows with no profile yet, as it does in every
// other table.
func TestMergeUsers_removesQuotaSnapshotByUserIDOnlyWithoutProfile(t *testing.T) {
	// Given
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	seedQuotaSnapshot(t, s, "", "u-orphan")
	seedQuotaSnapshot(t, s, "elsewhere@example.com", "u-orphan")

	// When
	if err := s.MergeUsers(ctx, "", "u-orphan", "new@example.com"); err != nil {
		t.Fatalf("MergeUsers by user_id: %v", err)
	}

	// Then
	if got, want := quotaRows(t, s), []string{"elsewhere@example.com"}; !slices.Equal(got, want) {
		t.Fatalf("quota_snapshots rows = %v, want %v", got, want)
	}
}

// privacySettingRows lists the rows of privacy_settings as "user_id|profile_email|
// scope_type|scope_value", in byte order.
func privacySettingRows(t *testing.T, s *PgStore) []string {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), `
		SELECT user_id || '|' || profile_email || '|' || scope_type || '|' || scope_value
		FROM privacy_settings`)
	if err != nil {
		t.Fatalf("read privacy_settings: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	slices.Sort(out) // the database's collation is not byte order
	return out
}

// A setting with no user_id is found by the address alone, so a merge moves it to
// the target, whatever case it was stored in. The target's own setting of the
// same scope stays, and two source rows that differ in case only become one. A
// setting with a user_id is found by that id after the merge as before, so it
// stays where it is, and so does every other account's.
func TestMergeUsers_movesPrivacySettingsWithoutUserID(t *testing.T) {
	// Given
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	for _, p := range []struct{ userID, email, scopeType, scopeValue string }{
		{"", "Old@Example.com", "user", ""},
		{"", "old@example.com", "project", "p1"},
		{"", "OLD@example.com", "project", "p1"}, // the same scope, in another case
		{"", "old@example.com", "session", "s1"}, // the target has this one
		{"", "new@example.com", "session", "s1"},
		{"u-old", "old@example.com", "session", "s2"}, // found by its user_id
		{"", "kept@example.com", "user", ""},
	} {
		if err := s.SetPrivacy(ctx, p.userID, p.email, p.scopeType, p.scopeValue); err != nil {
			t.Fatalf("SetPrivacy(%s): %v", p.email, err)
		}
	}
	var targetID int64
	if err := s.pool.QueryRow(ctx, `SELECT id FROM privacy_settings WHERE profile_email = 'new@example.com'`).Scan(&targetID); err != nil {
		t.Fatalf("read the target's row: %v", err)
	}

	// When
	if err := s.MergeUsers(ctx, "old@example.com", "", "new@example.com"); err != nil {
		t.Fatalf("MergeUsers: %v", err)
	}

	// Then
	want := []string{
		"u-old|old@example.com|session|s2",
		"|kept@example.com|user|",
		"|new@example.com|project|p1",
		"|new@example.com|session|s1",
		"|new@example.com|user|",
	}
	if got := privacySettingRows(t, s); !slices.Equal(got, want) {
		t.Fatalf("privacy_settings rows = %q, want %q", got, want)
	}
	if n := countWhere(t, s, "privacy_settings", "id = $1", targetID); n != 1 {
		t.Error("the target's own setting was replaced by the moved one")
	}

	// And merging an address into itself changes nothing.
	if err := s.MergeUsers(ctx, "new@example.com", "", "new@example.com"); err != nil {
		t.Fatalf("MergeUsers into itself: %v", err)
	}
	if got := privacySettingRows(t, s); !slices.Equal(got, want) {
		t.Fatalf("privacy_settings rows after a merge into itself = %q, want %q", got, want)
	}

	// The user_id form has no setting to claim: they all name a profile.
	if err := s.MergeUsers(ctx, "", "u-old", "new@example.com"); err != nil {
		t.Fatalf("MergeUsers by user_id: %v", err)
	}
	if got := privacySettingRows(t, s); !slices.Equal(got, want) {
		t.Fatalf("privacy_settings rows after a user_id merge = %q, want %q", got, want)
	}
}

// The dashboard lists and removes an account's settings by its cctrace user ID
// when it has one (ListPrivacySettings, RemovePrivacy). A setting a merge moves
// to the target therefore carries the target account's ID, or its owner could
// neither see nor remove it.
func TestMergeUsers_movedPrivacySettingsBelongToTheTargetAccount(t *testing.T) {
	// Given: a target with a dashboard account that has a cctrace user ID, and a
	// source with settings of its own.
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `INSERT INTO dashboard_users (email, password_hash, role, name, cctrace_user_id)
		VALUES ('New@Example.com', 'test-hash', 'user', 'New', 'u-new')`); err != nil {
		t.Fatalf("insert the target's dashboard account: %v", err)
	}
	for _, p := range []struct{ scopeType, scopeValue string }{{"user", ""}, {"project", "p1"}} {
		if err := s.SetPrivacy(ctx, "", "Old@Example.com", p.scopeType, p.scopeValue); err != nil {
			t.Fatalf("SetPrivacy(%s): %v", p.scopeType, err)
		}
	}

	// When
	if err := s.MergeUsers(ctx, "old@example.com", "", "new@example.com"); err != nil {
		t.Fatalf("MergeUsers: %v", err)
	}

	// Then: the account lists them as it lists its own, and can remove one.
	listed, err := s.ListPrivacySettings(ctx, "u-new", "new@example.com")
	if err != nil {
		t.Fatalf("ListPrivacySettings: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("the target account lists %d settings, want the 2 moved ones", len(listed))
	}
	if err := s.RemovePrivacy(ctx, "u-new", "new@example.com", "project", "p1"); err != nil {
		t.Fatalf("RemovePrivacy: %v", err)
	}
	if n := countWhere(t, s, "privacy_settings", "scope_type = 'project'"); n != 0 {
		t.Error("the target account could not remove a moved setting")
	}
}

// An address merged into itself in another case is still no merge: the rows under
// the target's own spelling stay, and the source's other spelling moves to it.
func TestMergeUsers_intoTheSameAddressInAnotherCaseKeepsRows(t *testing.T) {
	// Given
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	seedQuotaSnapshot(t, s, "old@example.com", "u-old")
	for _, p := range []struct{ email, scopeType, scopeValue string }{
		{"old@example.com", "user", ""},
		{"Old@Example.com", "project", "p1"},
	} {
		if err := s.SetPrivacy(ctx, "", p.email, p.scopeType, p.scopeValue); err != nil {
			t.Fatalf("SetPrivacy(%s): %v", p.email, err)
		}
	}

	// When
	if err := s.MergeUsers(ctx, "Old@Example.com", "", "old@example.com"); err != nil {
		t.Fatalf("MergeUsers: %v", err)
	}

	// Then
	if got, want := quotaRows(t, s), []string{"old@example.com"}; !slices.Equal(got, want) {
		t.Errorf("quota_snapshots rows = %v, want %v", got, want)
	}
	want := []string{"|old@example.com|project|p1", "|old@example.com|user|"}
	if got := privacySettingRows(t, s); !slices.Equal(got, want) {
		t.Errorf("privacy_settings rows = %q, want %q", got, want)
	}
}
