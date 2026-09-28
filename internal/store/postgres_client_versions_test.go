package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestPgStore_ListClientVersions_includesUnreportedActiveAccounts pins the bug
// this query replaced: an account active in session_records or otel_events
// but with no client_versions row (a pre-v0.6.1 client, which cannot send the
// version header at all) must still appear, with an empty ClientVersion, not
// be silently dropped from the list.
func TestPgStore_ListClientVersions_includesUnreportedActiveAccounts(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC()

	const (
		reported     = "reported@example.com"
		unreportedSR = "unreported-session@example.com" // active via session_records only
		unreportedOE = "unreported-otel@example.com"    // active via otel_events only
		stale        = "stale@example.com"              // reported long ago, no recent activity
	)

	if err := s.UpsertClientVersion(ctx, ClientVersionUpdate{UserID: "u-reported", ProfileEmail: reported, ClientVersion: "v0.7.9"}); err != nil {
		t.Fatalf("UpsertClientVersion(reported): %v", err)
	}
	if err := s.UpsertClientVersion(ctx, ClientVersionUpdate{UserID: "u-stale", ProfileEmail: stale, ClientVersion: "v0.5.0"}); err != nil {
		t.Fatalf("UpsertClientVersion(stale): %v", err)
	}
	// Backdate the stale account's row past the activity window so it is
	// exercised only through client_versions bookkeeping, not activity.
	if _, err := s.pool.Exec(ctx,
		`UPDATE client_versions SET last_seen_at = $1 WHERE profile_email = $2`,
		now.Add(-60*24*time.Hour), stale); err != nil {
		t.Fatalf("backdate stale client_versions row: %v", err)
	}

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: now, SessionID: "s-reported", RecordType: "user", ProfileEmail: reported, UUID: "r1"},
		{Ts: now, SessionID: "s-unreported", RecordType: "user", ProfileEmail: unreportedSR, UUID: "u1"},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: now, EventName: "api_request", SessionID: "e-unreported", ProfileEmail: unreportedOE},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	records, err := s.ListClientVersions(ctx)
	if err != nil {
		t.Fatalf("ListClientVersions: %v", err)
	}

	byEmail := map[string]*ClientVersionRecord{}
	for _, r := range records {
		byEmail[r.ProfileEmail] = r
	}

	if _, ok := byEmail[stale]; ok {
		t.Fatalf("stale account with no recent activity should not appear: %+v", records)
	}

	got, ok := byEmail[reported]
	if !ok {
		t.Fatalf("reported active account missing from list: %+v", records)
	}
	if got.ClientVersion != "v0.7.9" {
		t.Fatalf("reported account ClientVersion = %q, want v0.7.9", got.ClientVersion)
	}

	for _, email := range []string{unreportedSR, unreportedOE} {
		got, ok := byEmail[email]
		if !ok {
			t.Fatalf("unreported active account %q missing from list — the exact regression this query fixes: %+v", email, records)
		}
		if got.ClientVersion != "" {
			t.Fatalf("unreported account %q ClientVersion = %q, want empty (means: confirmed below %s)", email, got.ClientVersion, ClientVersionHeaderSince)
		}
		if got.LastSeenAt.IsZero() {
			t.Fatalf("unreported account %q LastSeenAt is zero, want activity timestamp", email)
		}
	}
}

// The Clients tab lists profile emails, and an email is not who someone is.
// Working out which colleague an account belonged to meant querying
// dashboard_users by hand, so the name that account already carries is carried
// through the listing.
//
// Accounts with no dashboard_users row keep an empty Name rather than being
// dropped: the whole point of this listing is that the accounts furthest behind
// are the ones least likely to be fully registered, and an inner join would
// hide exactly them.
func TestPgStore_ListClientVersions_carriesDashboardUserName(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC()

	const (
		named   = "named@example.com"
		unnamed = "unnamed@example.com"
	)

	if _, err := s.CreateDashboardUser(ctx, &DashboardUser{
		Email:        named,
		PasswordHash: "x",
		Role:         "user",
		Name:         "Sam Carter",
		Team:         "Solution",
	}); err != nil {
		t.Fatalf("CreateDashboardUser: %v", err)
	}

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: now, SessionID: "s-named", RecordType: "user", ProfileEmail: named, UUID: "n1"},
		{Ts: now, SessionID: "s-unnamed", RecordType: "user", ProfileEmail: unnamed, UUID: "n2"},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	records, err := s.ListClientVersions(ctx)
	if err != nil {
		t.Fatalf("ListClientVersions: %v", err)
	}

	byEmail := map[string]*ClientVersionRecord{}
	for _, r := range records {
		byEmail[r.ProfileEmail] = r
	}

	if got := byEmail[named]; got == nil || got.Name != "Sam Carter" {
		t.Fatalf("named account Name = %q, want %q", nameOf(byEmail[named]), "Sam Carter")
	}
	if got := byEmail[unnamed]; got == nil {
		t.Fatalf("account without a dashboard user was dropped from the listing")
	} else if got.Name != "" {
		t.Fatalf("account without a dashboard user Name = %q, want empty", got.Name)
	}
}

func nameOf(r *ClientVersionRecord) string {
	if r == nil {
		return "<missing>"
	}
	return r.Name
}

// TestPgStore_UpsertClientVersion_keepsLastReportedVersionWhenHeaderMissing
// pins the overwrite policy: a sync that arrives without X-Cctrace-Version must
// not erase what the account last reported.
//
// A missing header is not evidence of an old client. internal/syncer/client.go
// omits the header entirely when its own version string is empty (a build made
// without the ldflags that inject it), so a current client can send headerless
// requests for a stretch — exactly the empty-version records observed in prod
// right after a v0.7.31 -> v0.7.32 swap. Overwriting on empty would take such
// an account and pin it at "below v0.6.1" on the Clients tab, which is the same
// class of lie #455 exists to remove.
func TestPgStore_UpsertClientVersion_keepsLastReportedVersionWhenHeaderMissing(t *testing.T) {
	// Given
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
		Ts: time.Now().UTC(), SessionID: "swap-session", RecordType: "user", ProfileEmail: "swap@example.com", UUID: "swap-1",
	}}); err != nil {
		t.Fatalf("seed active account: %v", err)
	}
	if err := s.UpsertClientVersion(ctx, ClientVersionUpdate{
		UserID: "swap", ProfileEmail: "swap@example.com", ClientVersion: "v0.7.31", ClientOS: "darwin", ClientArch: "arm64",
	}); err != nil {
		t.Fatalf("seed client version: %v", err)
	}
	var seededSeenAt time.Time
	if err := s.pool.QueryRow(ctx,
		`SELECT last_seen_at FROM client_versions WHERE profile_email = 'swap@example.com'`,
	).Scan(&seededSeenAt); err != nil {
		t.Fatalf("read seeded last_seen_at: %v", err)
	}

	// When: the same account syncs again with no version/os/arch header at all.
	if err := s.UpsertClientVersion(ctx, ClientVersionUpdate{
		UserID: "swap", ProfileEmail: "swap@example.com",
	}); err != nil {
		t.Fatalf("upsert headerless client version: %v", err)
	}

	// Then
	var version, clientOS, clientArch string
	var seenAt time.Time
	if err := s.pool.QueryRow(ctx,
		`SELECT client_version, client_os, client_arch, last_seen_at FROM client_versions WHERE profile_email = 'swap@example.com'`,
	).Scan(&version, &clientOS, &clientArch, &seenAt); err != nil {
		t.Fatalf("read client_versions row: %v", err)
	}
	if version != "v0.7.31" {
		t.Fatalf("client_version = %q, want v0.7.31 (a missing header must not erase the last reported version)", version)
	}
	if clientOS != "darwin" || clientArch != "arm64" {
		t.Fatalf("client platform = %q/%q, want darwin/arm64 (same rule as the version)", clientOS, clientArch)
	}
	if !seenAt.After(seededSeenAt) {
		t.Fatalf("last_seen_at = %v, want later than %v (the sync itself still happened)", seenAt, seededSeenAt)
	}
}

// TestPgStore_UpsertClientVersion_createsRowForMissingVersionHeader guards the
// other half of the rule: keeping the last non-empty value must not stop a
// brand-new account from getting a row at all. This is regression cover, not a
// fail-first test — the INSERT path already stores an empty version — and it is
// what makes "no row" mean "never synced" once the handler stops filtering
// headerless syncs out.
//
// The check reads client_versions directly: ListClientVersions synthesises rows
// from recent activity, so it cannot tell an existing empty row apart from a
// missing one.
func TestPgStore_UpsertClientVersion_createsRowForMissingVersionHeader(t *testing.T) {
	// Given
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	// When
	if err := s.UpsertClientVersion(ctx, ClientVersionUpdate{
		UserID: "headerless", ProfileEmail: "headerless@example.com",
	}); err != nil {
		t.Fatalf("upsert headerless client version: %v", err)
	}

	// Then
	var version, userID string
	if err := s.pool.QueryRow(ctx,
		`SELECT client_version, user_id FROM client_versions WHERE profile_email = 'headerless@example.com'`,
	).Scan(&version, &userID); err != nil {
		t.Fatalf("read client_versions row (a headerless sync must still leave a row): %v", err)
	}
	if version != "" {
		t.Fatalf("client_version = %q, want empty", version)
	}
	if userID != "headerless" {
		t.Fatalf("user_id = %q, want headerless", userID)
	}
}

// A daemon replaced on disk while an old resident process keeps running writes
// both versions at once. client_versions holds one row per account and keeps only
// the newest string, so the account reads as up to date while half its records
// come from the stale binary -- invisible for a week while two clients downloaded
// the same artifact hundreds of times a day (#623).
func TestListClientVersions_CountsVersionsSeenTogether(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC().Add(-time.Hour)

	insert := func(email, version string, n int, at time.Time) {
		t.Helper()
		records := []*SessionRecord{}
		for i := 0; i < n; i++ {
			records = append(records, &SessionRecord{
				Ts: at.Add(time.Duration(i) * time.Second), SessionID: "s-" + email + version,
				UUID: fmt.Sprintf("u-%s-%s-%d", email, version, i), RecordType: "user",
				ProfileEmail: email, UserID: email, ProjectHash: "p",
				CctraceVersion: version,
				Raw:            []byte(`{"message":{"role":"user","content":"work"}}`),
			})
		}
		if err := s.InsertSessionRecords(ctx, records); err != nil {
			t.Fatalf("insert %s %s: %v", email, version, err)
		}
	}

	// One account writing from two binaries at the same time.
	insert("stuck@example.test", "v0.7.41", 3, now)
	insert("stuck@example.test", "v0.7.50", 3, now.Add(time.Minute))
	// And one behaving normally.
	insert("fine@example.test", "v0.7.50", 3, now)

	records, err := s.ListClientVersions(ctx)
	if err != nil {
		t.Fatalf("ListClientVersions: %v", err)
	}
	seen := map[string]int{}
	for _, r := range records {
		seen[r.ProfileEmail] = r.ConcurrentVersions
	}
	if seen["stuck@example.test"] != 2 {
		t.Fatalf("stuck account = %d versions, want 2", seen["stuck@example.test"])
	}
	// The ordinary account must not be flagged; a signal that fires on everyone
	// says nothing.
	if seen["fine@example.test"] != 1 {
		t.Fatalf("ordinary account = %d versions, want 1", seen["fine@example.test"])
	}
}
