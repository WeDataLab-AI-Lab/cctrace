package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// seedObservedAccounts gives user "u-me" (profile me@example.test) a Codex
// account in its session records and a Claude account only in quota readings,
// and gives someone else an account of their own.
func seedObservedAccounts(t *testing.T, s *PgStore) {
	t.Helper()
	ctx := context.Background()
	ts := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	rec := func(uuid, userID, profile, provider, account string) *SessionRecord {
		return &SessionRecord{
			Ts: ts, SessionID: "s-" + uuid, RecordType: "assistant", ProfileEmail: profile,
			UserID: userID, Agent: "codex", BillingProvider: provider, AccountID: account,
			Raw: json.RawMessage(`{"uuid":"` + uuid + `"}`), UUID: uuid,
		}
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		rec("r1", "u-me", "me@example.test", "openai", "acct-mine"),
		rec("r2", "u-me", "me@example.test", "openai", ""),
		rec("r3", "u-other", "other@example.test", "openai", "acct-theirs"),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	quota := sampleAt(0, "acct-claude", "session")
	quota.ProfileEmail = "me@example.test"
	theirs := sampleAt(0, "acct-theirs-quota", "session")
	theirs.ProfileEmail = "other@example.test"
	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{quota, theirs}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
}

func observedKeys(got []ObservedBillingAccount) map[string]ObservedBillingAccount {
	out := map[string]ObservedBillingAccount{}
	for _, a := range got {
		out[a.BillingProvider+"/"+a.AccountID] = a
	}
	return out
}

// A user may only ever pick from accounts their own data was billed to: the
// list is the ownership check the self-exclusion route relies on.
func TestListObservedBillingAccounts_onlyTheCallersOwnAccounts(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	seedObservedAccounts(t, s)
	ctx := context.Background()

	// By profile, the caller's quota readings are theirs: they are keyed on the
	// same profile_email. By user id they are not -- quota_samples carries no
	// user_id, and a profile name is whatever the client sent, so another user
	// reporting under the same name would leak in -- so only session records count.
	for name, tc := range map[string]struct {
		scope [2]string
		want  []string
	}{
		"by profile": {[2]string{"me@example.test", ""}, []string{"anthropic/acct-claude", "openai/acct-mine"}},
		"by user id": {[2]string{"", "u-me"}, []string{"openai/acct-mine"}},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := s.ListObservedBillingAccounts(ctx, tc.scope[0], tc.scope[1], "me@example.test")
			if err != nil {
				t.Fatalf("ListObservedBillingAccounts: %v", err)
			}
			keys := observedKeys(got)
			if len(keys) != len(tc.want) {
				t.Fatalf("got %+v, want exactly %v", got, tc.want)
			}
			for _, k := range tc.want {
				if _, ok := keys[k]; !ok {
					t.Errorf("%s is missing", k)
				}
			}
		})
	}

	got, err := s.ListObservedBillingAccounts(ctx, "", "", "me@example.test")
	if err != nil {
		t.Fatalf("ListObservedBillingAccounts: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("an empty scope listed %+v; it must match nothing", got)
	}
}

// Self-registered and admin-registered exclusions hide the same way, but only
// the caller's own registration is theirs to take back.
func TestSelfExcludeBillingAccount_isRecordedAsTheCallersOwn(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	seedObservedAccounts(t, s)
	ctx := context.Background()

	if err := s.SelfExcludeBillingAccount(ctx, "openai", "acct-mine", "personal", "me@example.test"); err != nil {
		t.Fatalf("SelfExcludeBillingAccount: %v", err)
	}
	if _, _, err := s.ExcludeBillingAccount(ctx, "anthropic", "acct-claude", "admin call", "admin@example.test"); err != nil {
		t.Fatalf("ExcludeBillingAccount: %v", err)
	}

	got, err := s.ListObservedBillingAccounts(ctx, "me@example.test", "", "me@example.test")
	if err != nil {
		t.Fatalf("ListObservedBillingAccounts: %v", err)
	}
	keys := observedKeys(got)
	if a := keys["openai/acct-mine"]; !a.Excluded || !a.SelfRegistered {
		t.Errorf("self-excluded account = %+v, want excluded and self-registered", a)
	}
	if a := keys["anthropic/acct-claude"]; !a.Excluded || a.SelfRegistered {
		t.Errorf("admin-excluded account = %+v, want excluded and not self-registered", a)
	}

	// The same hiding as an admin exclusion: the session record is gone from the view.
	var visible int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM visible_session_records WHERE account_id = 'acct-mine'`).Scan(&visible); err != nil {
		t.Fatalf("count visible: %v", err)
	}
	if visible != 0 {
		t.Errorf("%d self-excluded records still visible", visible)
	}

	admin, err := s.ListExcludedBillingAccounts(ctx)
	if err != nil {
		t.Fatalf("ListExcludedBillingAccounts: %v", err)
	}
	selfFlag := map[string]bool{}
	for _, a := range admin {
		selfFlag[a.AccountID] = a.SelfRegistered
	}
	if !selfFlag["acct-mine"] || selfFlag["acct-claude"] {
		t.Errorf("admin list self_registered = %v, want only acct-mine", selfFlag)
	}
}

// A self-registration never takes over an admin's entry -- otherwise a user
// could turn an admin exclusion into one they are allowed to remove.
func TestSelfExcludeBillingAccount_leavesAnAdminEntryAlone(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	seedObservedAccounts(t, s)
	ctx := context.Background()

	if _, _, err := s.ExcludeBillingAccount(ctx, "openai", "acct-mine", "admin call", "admin@example.test"); err != nil {
		t.Fatalf("ExcludeBillingAccount: %v", err)
	}
	if err := s.SelfExcludeBillingAccount(ctx, "openai", "acct-mine", "", "me@example.test"); err != nil {
		t.Fatalf("SelfExcludeBillingAccount: %v", err)
	}
	removed, err := s.RemoveSelfExcludedBillingAccount(ctx, "openai", "acct-mine", "me@example.test")
	if err != nil {
		t.Fatalf("RemoveSelfExcludedBillingAccount: %v", err)
	}
	if removed {
		t.Fatal("the user removed an admin's exclusion")
	}
	got, _ := s.ListObservedBillingAccounts(ctx, "me@example.test", "", "me@example.test")
	if a := observedKeys(got)["openai/acct-mine"]; !a.Excluded || a.SelfRegistered {
		t.Fatalf("account = %+v, want still excluded by the admin", a)
	}
}

// Removing is idempotent. On a production-sized copy a removal holds the request
// for over a minute, and a second DELETE that queued behind it found the entry
// already gone and answered 403 "only exclusions you registered yourself can be
// removed" -- shown to the user right after their own removal succeeded.
func TestRemoveSelfExcludedBillingAccount_isIdempotent(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	seedObservedAccounts(t, s)
	ctx := context.Background()

	if err := s.SelfExcludeBillingAccount(ctx, "openai", "acct-mine", "", "me@example.test"); err != nil {
		t.Fatalf("SelfExcludeBillingAccount: %v", err)
	}
	for i := 1; i <= 2; i++ {
		removed, err := s.RemoveSelfExcludedBillingAccount(ctx, "openai", "acct-mine", "me@example.test")
		if err != nil || !removed {
			t.Fatalf("removal %d: removed=%v err=%v, want success", i, removed, err)
		}
	}
}

// An admin re-registering a self entry makes it the admin's: the user can no
// longer take it back.
func TestExcludeBillingAccount_adminTakesOverASelfEntry(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	seedObservedAccounts(t, s)
	ctx := context.Background()

	if err := s.SelfExcludeBillingAccount(ctx, "openai", "acct-mine", "", "me@example.test"); err != nil {
		t.Fatalf("SelfExcludeBillingAccount: %v", err)
	}
	if _, _, err := s.ExcludeBillingAccount(ctx, "openai", "acct-mine", "admin call", "admin@example.test"); err != nil {
		t.Fatalf("ExcludeBillingAccount: %v", err)
	}
	if removed, err := s.RemoveSelfExcludedBillingAccount(ctx, "openai", "acct-mine", "me@example.test"); err != nil || removed {
		t.Fatalf("removed=%v err=%v, want the admin's entry kept", removed, err)
	}
	admin, err := s.ListExcludedBillingAccounts(ctx)
	if err != nil {
		t.Fatalf("ListExcludedBillingAccounts: %v", err)
	}
	if len(admin) != 1 || admin[0].SelfRegistered {
		t.Fatalf("admin list = %+v, want one entry no longer marked self-registered", admin)
	}
}

// Only the person who registered it can take a self exclusion back, and taking
// it back brings the rows straight back.
func TestRemoveSelfExcludedBillingAccount_onlyTheRegistrant(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	seedObservedAccounts(t, s)
	ctx := context.Background()

	if err := s.SelfExcludeBillingAccount(ctx, "openai", "acct-mine", "", "me@example.test"); err != nil {
		t.Fatalf("SelfExcludeBillingAccount: %v", err)
	}
	if removed, err := s.RemoveSelfExcludedBillingAccount(ctx, "openai", "acct-mine", "other@example.test"); err != nil || removed {
		t.Fatalf("someone else removed=%v err=%v, want refused", removed, err)
	}
	removed, err := s.RemoveSelfExcludedBillingAccount(ctx, "openai", "acct-mine", "me@example.test")
	if err != nil || !removed {
		t.Fatalf("registrant removed=%v err=%v, want removed", removed, err)
	}
	var visible int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM visible_session_records WHERE account_id = 'acct-mine'`).Scan(&visible); err != nil {
		t.Fatalf("count visible: %v", err)
	}
	if visible != 1 {
		t.Errorf("%d records visible after removal, want the 1 back", visible)
	}
}

// A billing account seen with more than one login address bills several people.
// Excluding it hides every one of them, so it is flagged shared and left to an
// admin -- the same rule excluded_billing_links applies to address exclusions.
func TestListObservedBillingAccounts_flagsSharedAccounts(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	seedObservedAccounts(t, s)
	ctx := context.Background()

	a := sampleAt(10, "acct-claude", "session")
	a.ProfileEmail, a.LoginEmail = "me@example.test", "me@example.test"
	b := sampleAt(20, "acct-claude", "session")
	b.ProfileEmail, b.LoginEmail = "other@example.test", "Other@example.test"
	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{a, b}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}

	got, err := s.ListObservedBillingAccounts(ctx, "me@example.test", "", "me@example.test")
	if err != nil {
		t.Fatalf("ListObservedBillingAccounts: %v", err)
	}
	keys := observedKeys(got)
	if !keys["anthropic/acct-claude"].Shared {
		t.Error("an account seen with two addresses is not flagged shared")
	}
	if keys["openai/acct-mine"].Shared {
		t.Error("an account seen with no second address is flagged shared")
	}
}

// A registration that changes nothing -- the account is already excluded --
// must not pay for the recompute and rollup rebuild, which rewrite derived
// tables for everyone. The recompute TRUNCATEs excluded_sessions, so a marker
// row there surviving is the evidence it did not run.
func TestSelfExcludeBillingAccount_skipsTheRecomputeWhenNothingChanged(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	seedObservedAccounts(t, s)
	ctx := context.Background()

	if _, _, err := s.ExcludeBillingAccount(ctx, "openai", "acct-mine", "admin call", "admin@example.test"); err != nil {
		t.Fatalf("ExcludeBillingAccount: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO excluded_sessions (session_id) VALUES ('recompute-marker')`); err != nil {
		t.Fatalf("insert marker: %v", err)
	}
	runPendingUsageRollupRebuild(t, s)
	if err := s.SelfExcludeBillingAccount(ctx, "openai", "acct-mine", "", "me@example.test"); err != nil {
		t.Fatalf("SelfExcludeBillingAccount: %v", err)
	}
	var marker int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM excluded_sessions WHERE session_id = 'recompute-marker'`).Scan(&marker); err != nil {
		t.Fatalf("count marker: %v", err)
	}
	if marker != 1 {
		t.Fatal("a registration that inserted nothing still ran the recompute")
	}
	if _, _, ok := pendingRebuildIdentities(t, s); ok {
		t.Error("a registration that inserted nothing queued a usage rebuild")
	}
}

// Dashboard addresses are compared case-folded everywhere else; a registrant
// whose address changed case must still own what they registered.
func TestSelfExcludedBillingAccount_registrantMatchesCaseInsensitively(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	seedObservedAccounts(t, s)
	ctx := context.Background()

	if err := s.SelfExcludeBillingAccount(ctx, "openai", "acct-mine", "", "Me@Example.test"); err != nil {
		t.Fatalf("SelfExcludeBillingAccount: %v", err)
	}
	got, err := s.ListObservedBillingAccounts(ctx, "me@example.test", "", "me@example.test")
	if err != nil {
		t.Fatalf("ListObservedBillingAccounts: %v", err)
	}
	if !observedKeys(got)["openai/acct-mine"].SelfRegistered {
		t.Error("the registrant's own entry is not listed as theirs when the case differs")
	}
	removed, err := s.RemoveSelfExcludedBillingAccount(ctx, "openai", "acct-mine", "me@example.test")
	if err != nil || !removed {
		t.Fatalf("removed=%v err=%v, want the registrant to remove it", removed, err)
	}
}

func insertAccountRecord(t *testing.T, s *PgStore, uuid, userID, profile, account string) {
	t.Helper()
	if err := s.InsertSessionRecords(context.Background(), []*SessionRecord{{
		Ts: time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC), SessionID: "s-" + uuid, RecordType: "assistant",
		ProfileEmail: profile, UserID: userID, Agent: "codex", BillingProvider: "openai", AccountID: account,
		Raw: json.RawMessage(`{"uuid":"` + uuid + `"}`), UUID: uuid,
	}}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
}

var bothScopes = map[string][2]string{"by profile": {"me@example.test", ""}, "by user id": {"", "u-me"}}

// A team account whose quota readings carry no login address still bills other
// people, and their session records say so. One of them must not be able to
// hide everyone else's data by excluding it.
func TestBillingAccountUsedByOthers_anotherUsersRecords(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	seedObservedAccounts(t, s)
	ctx := context.Background()

	for name, scope := range bothScopes {
		used, err := s.BillingAccountUsedByOthers(ctx, "openai", "acct-mine", scope[0], scope[1])
		if err != nil || used {
			t.Fatalf("%s: a personal account reported used by others (%v, %v)", name, used, err)
		}
	}

	insertAccountRecord(t, s, "team-1", "u-other", "other@example.test", "acct-mine")
	for name, scope := range bothScopes {
		used, err := s.BillingAccountUsedByOthers(ctx, "openai", "acct-mine", scope[0], scope[1])
		if err != nil || !used {
			t.Errorf("%s: an account on another user's records not reported used by others (%v, %v)", name, used, err)
		}
	}
}

// A row synced before user_id or profile_email existed carries it empty. That
// is not a second owner, the same rule SessionOwner applies.
func TestBillingAccountUsedByOthers_ignoresRowsWithoutAnOwner(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	seedObservedAccounts(t, s)
	insertAccountRecord(t, s, "legacy-1", "", "", "acct-mine")

	for name, scope := range bothScopes {
		used, err := s.BillingAccountUsedByOthers(context.Background(), "openai", "acct-mine", scope[0], scope[1])
		if err != nil || used {
			t.Fatalf("%s: an ownerless legacy row counted as another user (%v, %v)", name, used, err)
		}
	}
}

// /api/sync stores the identity the client sends, so a user can plant a record
// carrying someone else's account id and have it "seen in their data". The
// victim's own records are already there, so the account reads as used by
// others and the plant buys nothing.
func TestBillingAccountUsedByOthers_aPlantedRecordDoesNotMakeAnAccountYours(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	seedObservedAccounts(t, s)
	ctx := context.Background()
	insertAccountRecord(t, s, "planted-1", "u-me", "me@example.test", "acct-theirs")

	got, err := s.ListObservedBillingAccounts(ctx, "", "u-me", "me@example.test")
	if err != nil {
		t.Fatalf("ListObservedBillingAccounts: %v", err)
	}
	if _, ok := observedKeys(got)["openai/acct-theirs"]; !ok {
		t.Fatal("precondition: the planted account should be listed as seen")
	}
	for name, scope := range bothScopes {
		used, err := s.BillingAccountUsedByOthers(ctx, "openai", "acct-theirs", scope[0], scope[1])
		if err != nil || !used {
			t.Errorf("%s: a planted account is not reported used by its real owner (%v, %v)", name, used, err)
		}
	}
}

// Quota readings from another profile show the account bills someone else even
// when they carry no login address -- Codex readings mostly do not.
func TestListObservedBillingAccounts_anotherProfilesQuotaMakesItShared(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	seedObservedAccounts(t, s)
	ctx := context.Background()

	theirs := sampleAt(30, "acct-claude", "session")
	theirs.ProfileEmail = "other@example.test"
	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{theirs}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
	got, err := s.ListObservedBillingAccounts(ctx, "me@example.test", "", "me@example.test")
	if err != nil {
		t.Fatalf("ListObservedBillingAccounts: %v", err)
	}
	keys := observedKeys(got)
	if !keys["anthropic/acct-claude"].Shared {
		t.Error("an account another profile reports quota for is not flagged shared")
	}
	if keys["openai/acct-mine"].Shared {
		t.Error("an account only the caller reports is flagged shared")
	}
}
