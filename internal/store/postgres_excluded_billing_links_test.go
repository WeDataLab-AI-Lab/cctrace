package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// personalLinkSample is a Codex quota reading that names the login email behind a
// billing account -- the one place the server learns that an account id and an
// address are the same person.
func personalLinkSample(account, email string) *QuotaSample {
	q := sampleAt(0, account, "300")
	q.BillingProvider = "openai"
	q.LoginEmail = email
	return q
}

// countByAccount counts rows for the two seeded accounts in one view.
func countByAccount(t *testing.T, s *PgStore, view string) (personal, team int) {
	t.Helper()
	if err := s.pool.QueryRow(context.Background(),
		"SELECT count(*) FILTER (WHERE account_id = 'acct-personal'), count(*) FILTER (WHERE account_id = 'acct-team') FROM "+view,
	).Scan(&personal, &team); err != nil {
		t.Fatalf("count %s: %v", view, err)
	}
	return personal, team
}

// seedPersonalAndTeamCodex writes one Codex usage row for each account. Neither
// carries a login email: Codex rows never do at ingest, which is the whole gap.
func seedPersonalAndTeamCodex(t *testing.T, s *PgStore, ts time.Time) {
	t.Helper()
	ctx := context.Background()
	personal := codexUsageRecord("codex-personal", "gpt-5.1-codex-max", ts, 1000, 100, 0)
	personal.AccountID = "acct-personal"
	team := codexUsageRecord("codex-team", "gpt-5.1-codex-max", ts.Add(time.Minute), 1000, 100, 0)
	team.AccountID = "acct-team"
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{personal, team}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.RefreshCodexImputedCost(ctx); err != nil {
		t.Fatalf("RefreshCodexImputedCost: %v", err)
	}
}

// #715: an admin excluded a personal address, and that account's Codex sessions,
// conversation text and cost stayed on the dashboard. Every one of those rows
// carries the billing id and none carries the address, so an exclusion keyed on
// the address alone never reached them -- even though the server had already
// seen the two together in a quota reading.
func TestExcludedEmailHidesItsLinkedBillingAccount(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	seedPersonalAndTeamCodex(t, s, time.Now().UTC())
	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{personalLinkSample("acct-personal", "personal@example.test")}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}

	if _, err := s.ExcludeAccount(ctx, "personal@example.test", "non-company account", "admin"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}
	for _, view := range []string{"visible_events", "visible_session_records"} {
		personal, team := countByAccount(t, s, view)
		if personal != 0 {
			t.Errorf("%s still shows %d rows of the account linked to the excluded address", view, personal)
		}
		if team != 1 {
			t.Errorf("%s shows %d rows of the unrelated account, want 1", view, team)
		}
	}

	// Reversible, as every exclusion is: nothing was deleted.
	if err := s.RemoveExcludedAccount(ctx, "personal@example.test"); err != nil {
		t.Fatalf("RemoveExcludedAccount: %v", err)
	}
	for _, view := range []string{"visible_events", "visible_session_records"} {
		if personal, _ := countByAccount(t, s, view); personal != 1 {
			t.Errorf("%s shows %d rows after un-excluding, want the row back", view, personal)
		}
	}
}

// The link is often learned after the exclusion: the address was excluded weeks
// ago and a quota reading naming it arrives today. The periodic refresh has to
// pick that up, and report a change only when there is one, since a change is
// what triggers the rollup rebuild.
func TestLinkObservedAfterExclusionIsPickedUpByRefresh(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	seedPersonalAndTeamCodex(t, s, time.Now().UTC())
	if _, err := s.ExcludeAccount(ctx, "personal@example.test", "non-company account", "admin"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}
	if personal, _ := countByAccount(t, s, "visible_session_records"); personal != 1 {
		t.Fatalf("personal rows visible = %d before any link exists, want 1", personal)
	}

	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{personalLinkSample("acct-personal", "Personal@Example.test")}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
	changed, err := s.RefreshExcludedBillingLinks(ctx)
	if err != nil {
		t.Fatalf("RefreshExcludedBillingLinks: %v", err)
	}
	if !changed {
		t.Error("a newly observed link was not reported as a change")
	}
	for _, view := range []string{"visible_events", "visible_session_records"} {
		if personal, _ := countByAccount(t, s, view); personal != 0 {
			t.Errorf("%s still shows %d rows after the link was learned", view, personal)
		}
	}

	changed, err = s.RefreshExcludedBillingLinks(ctx)
	if err != nil {
		t.Fatalf("second RefreshExcludedBillingLinks: %v", err)
	}
	if changed {
		t.Error("an unchanged link set was reported as a change")
	}
}

// The link refresh names no address or account of its own, so the session list
// it rebuilds comes from what it changed: the linked account's sessions, and any
// session excluded_sessions picked up since the last tick -- here one that
// arrived under the excluded address with records not yet backfilled.
func TestLinkRefreshTakesItsSessionsOffTheList(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Now().UTC()

	seedPersonalAndTeamCodex(t, s, ts)
	if _, err := s.ExcludeAccount(ctx, "personal@example.test", "non-company account", "admin"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}
	if err := s.InsertEvents(ctx, []*OtelEvent{{Ts: ts, EventName: "api_request", SessionID: "late",
		LoginEmail: "personal@example.test", ProfileEmail: "p@example.test"}}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{Ts: ts, SessionID: "late", RecordType: "user",
		ProfileEmail: "p@example.test", UUID: "late-1", Raw: []byte(`{"text":"private prompt"}`)}}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if ids := sessionIDsInOverviews(t, s); !ids["codex-personal"] || !ids["late"] {
		t.Fatalf("precondition failed: list is %v before the link is learned", ids)
	}

	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{personalLinkSample("acct-personal", "personal@example.test")}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
	if _, err := s.RefreshExcludedBillingLinks(ctx); err != nil {
		t.Fatalf("RefreshExcludedBillingLinks: %v", err)
	}
	ids := sessionIDsInOverviews(t, s)
	if ids["codex-personal"] {
		t.Error("the linked account's session is still listed after the link was learned")
	}
	if ids["late"] {
		t.Error("a session excluded since the last tick is still listed after the refresh")
	}
	if !ids["codex-team"] {
		t.Error("the unrelated account's session left the list")
	}
}

// usage_hourly_rollups is built from visible_events but only its recent window is
// refreshed on a timer, so a row older than that window kept its pre-exclusion
// total forever: the personal account's 9/14 cost stayed on the trend chart.
// Every change in what is excluded has to rebuild the whole aggregate.
func TestExclusionRebuildsUsageRollupsBeyondTheRefreshWindow(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	old := time.Now().UTC().Add(-10 * 24 * time.Hour)
	seedPersonalAndTeamCodex(t, s, old)
	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{personalLinkSample("acct-personal", "personal@example.test")}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
	if err := s.RefreshUsageHourlyRollups(ctx, nil); err != nil {
		t.Fatalf("RefreshUsageHourlyRollups: %v", err)
	}
	before := codexRollupEvents(t, s)
	if before != 2 {
		t.Fatalf("rollup counts %d codex events before exclusion, want 2", before)
	}

	if _, err := s.ExcludeAccount(ctx, "personal@example.test", "non-company account", "admin"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}
	runPendingUsageRollupRebuild(t, s)
	if got := codexRollupEvents(t, s); got != 1 {
		t.Errorf("rollup counts %d codex events after excluding one account 10 days back, want 1", got)
	}
}

// Excluding by billing id changed the views and nothing else: the session list
// reads its own rollup, which was left as it was.
func TestBillingExclusionRebuildsRollups(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	seedPersonalAndTeamCodex(t, s, time.Now().UTC().Add(-10*24*time.Hour))
	if err := s.RefreshUsageHourlyRollups(ctx, nil); err != nil {
		t.Fatalf("RefreshUsageHourlyRollups: %v", err)
	}
	if err := s.BackfillSessionOverviewRollups(ctx); err != nil {
		t.Fatalf("BackfillSessionOverviewRollups: %v", err)
	}
	if !sessionIDsInOverviews(t, s)["codex-personal"] {
		t.Fatal("personal session missing from the list before exclusion")
	}

	if _, _, err := s.ExcludeBillingAccount(ctx, "openai", "acct-personal", "personal", "admin"); err != nil {
		t.Fatalf("ExcludeBillingAccount: %v", err)
	}
	if sessionIDsInOverviews(t, s)["codex-personal"] {
		t.Error("session list still shows the session of an excluded billing account")
	}
	runPendingUsageRollupRebuild(t, s)
	if got := codexRollupEvents(t, s); got != 1 {
		t.Errorf("rollup counts %d codex events after excluding one billing account, want 1", got)
	}

	if err := s.RemoveExcludedBillingAccount(ctx, "openai", "acct-personal"); err != nil {
		t.Fatalf("RemoveExcludedBillingAccount: %v", err)
	}
	if !sessionIDsInOverviews(t, s)["codex-personal"] {
		t.Error("session did not come back to the list after un-excluding")
	}
	runPendingUsageRollupRebuild(t, s)
	if got := codexRollupEvents(t, s); got != 2 {
		t.Errorf("rollup counts %d codex events after un-excluding, want 2", got)
	}
}

// The admin list names which billing accounts an excluded address reaches, so
// whoever excluded it can see what is actually hidden.
func TestListExcludedAccountsNamesLinkedBillingAccounts(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{personalLinkSample("acct-personal", "personal@example.test")}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
	if _, err := s.ExcludeAccount(ctx, "personal@example.test", "non-company account", "admin"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}
	accounts, err := s.ListExcludedAccounts(ctx)
	if err != nil {
		t.Fatalf("ListExcludedAccounts: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("listed %d excluded accounts, want 1", len(accounts))
	}
	links := accounts[0].LinkedBillingAccounts
	if len(links) != 1 || links[0].BillingProvider != "openai" || links[0].AccountID != "acct-personal" {
		t.Errorf("linked billing accounts = %+v, want the one openai account seen with this address", links)
	}
}

func codexRollupEvents(t *testing.T, s *PgStore) int64 {
	t.Helper()
	var n int64
	if err := s.pool.QueryRow(context.Background(),
		`SELECT COALESCE(sum(event_count), 0) FROM usage_hourly_rollups WHERE agent = 'codex'`).Scan(&n); err != nil {
		t.Fatalf("count rollup: %v", err)
	}
	return n
}

// A team or business plan bills several people under one account. Excluding one
// person's address must not hide everyone else on that account: a link is only
// drawn when every address ever seen with the account is excluded.
func TestSharedBillingAccountIsNotHiddenByOneExcludedAddress(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	seedPersonalAndTeamCodex(t, s, time.Now().UTC())
	colleague := personalLinkSample("acct-personal", "colleague@example.test")
	colleague.SampledAt = colleague.SampledAt.Add(time.Minute)
	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{
		personalLinkSample("acct-personal", "personal@example.test"),
		colleague,
	}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}

	if _, err := s.ExcludeAccount(ctx, "personal@example.test", "non-company account", "admin"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}
	if personal, _ := countByAccount(t, s, "visible_session_records"); personal != 1 {
		t.Errorf("a billing account shared with a non-excluded address was hidden (%d rows visible, want 1)", personal)
	}

	// Once every address on the account is excluded, the account goes too.
	if _, err := s.ExcludeAccount(ctx, "colleague@example.test", "left the company", "admin"); err != nil {
		t.Fatalf("ExcludeAccount colleague: %v", err)
	}
	if personal, _ := countByAccount(t, s, "visible_session_records"); personal != 0 {
		t.Errorf("account still visible (%d rows) after every address on it was excluded", personal)
	}
}

// A link, once drawn, holds while its address stays excluded. If a later reading
// shows another address on the account, dropping the link on the next tick would
// silently bring back every hidden row -- a personal account's whole history --
// with nothing but a log line to say so. Taking a link away is an admin decision:
// un-exclude the address.
func TestLinkIsStickyWhileItsAddressStaysExcluded(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	seedPersonalAndTeamCodex(t, s, time.Now().UTC())
	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{personalLinkSample("acct-personal", "personal@example.test")}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
	if _, err := s.ExcludeAccount(ctx, "personal@example.test", "non-company account", "admin"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}

	later := personalLinkSample("acct-personal", "other@example.test")
	later.SampledAt = later.SampledAt.Add(time.Hour)
	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{later}); err != nil {
		t.Fatalf("InsertQuotaSamples later: %v", err)
	}
	if _, err := s.RefreshExcludedBillingLinks(ctx); err != nil {
		t.Fatalf("RefreshExcludedBillingLinks: %v", err)
	}
	if personal, _ := countByAccount(t, s, "visible_session_records"); personal != 0 {
		t.Errorf("a periodic refresh dropped an existing link: %d rows visible again", personal)
	}

	if err := s.RemoveExcludedAccount(ctx, "personal@example.test"); err != nil {
		t.Fatalf("RemoveExcludedAccount: %v", err)
	}
	if personal, _ := countByAccount(t, s, "visible_session_records"); personal != 1 {
		t.Errorf("un-excluding the address left %d rows visible, want the row back", personal)
	}
}

// An inferred reading was attributed from which account was logged in around it,
// not observed. It is not evidence that an address and an account are one person,
// in either direction.
func TestInferredSampleIsNotLinkEvidence(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	seedPersonalAndTeamCodex(t, s, time.Now().UTC())
	inferred := personalLinkSample("acct-personal", "personal@example.test")
	inferred.Attribution = AttributionInferred
	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{inferred}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
	if _, err := s.ExcludeAccount(ctx, "personal@example.test", "non-company account", "admin"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}
	if personal, _ := countByAccount(t, s, "visible_session_records"); personal != 1 {
		t.Errorf("an inferred reading linked the account (%d rows visible, want 1)", personal)
	}
}

// Un-excluding has to bring old usage back to the charts, not only recent usage.
func TestRemovingAnExclusionRestoresOldUsageBuckets(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	seedPersonalAndTeamCodex(t, s, time.Now().UTC().Add(-10*24*time.Hour))
	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{personalLinkSample("acct-personal", "personal@example.test")}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
	if err := s.RefreshUsageHourlyRollups(ctx, nil); err != nil {
		t.Fatalf("RefreshUsageHourlyRollups: %v", err)
	}
	if _, err := s.ExcludeAccount(ctx, "personal@example.test", "non-company account", "admin"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}
	runPendingUsageRollupRebuild(t, s)
	if got := codexRollupEvents(t, s); got != 1 {
		t.Fatalf("rollup counts %d codex events after exclusion, want 1", got)
	}
	if err := s.RemoveExcludedAccount(ctx, "personal@example.test"); err != nil {
		t.Fatalf("RemoveExcludedAccount: %v", err)
	}
	runPendingUsageRollupRebuild(t, s)
	if got := codexRollupEvents(t, s); got != 2 {
		t.Errorf("rollup counts %d codex events after un-excluding, want 2", got)
	}
}

// Ingest refuses an excluded account's data before it is stored (#715). The
// blocklist it reads has to carry every key an account can be excluded by: the
// billing ids an admin excluded, the billing ids an excluded address reaches, and
// the addresses themselves.
func TestIngestBlocklistCarriesExclusions(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{personalLinkSample("acct-personal", "personal@example.test")}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
	if _, err := s.ExcludeAccount(ctx, "Personal@Example.test", "non-company account", "admin"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}
	if _, _, err := s.ExcludeBillingAccount(ctx, "anthropic", "acct-direct", "personal", "admin"); err != nil {
		t.Fatalf("ExcludeBillingAccount: %v", err)
	}

	bl, err := s.LoadIngestBlocklist(ctx)
	if err != nil {
		t.Fatalf("LoadIngestBlocklist: %v", err)
	}
	for _, want := range []BillingAccountRef{
		{BillingProvider: "openai", AccountID: "acct-personal"},
		{BillingProvider: "anthropic", AccountID: "acct-direct"},
	} {
		found := false
		for _, got := range bl.ExcludedAccounts {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("blocklist accounts %+v miss %+v", bl.ExcludedAccounts, want)
		}
	}
	if len(bl.ExcludedEmails) != 1 || bl.ExcludedEmails[0] != "personal@example.test" {
		t.Errorf("blocklist emails = %v, want the one excluded address, lowercased", bl.ExcludedEmails)
	}
}

// Every writer of usage_hourly_rollups deletes a range and reinserts it. Two of
// them overlapping collide on the primary key: the second DELETE cannot see rows
// the first inserted after its snapshot, and its INSERT then fails 23505 -- which,
// inside an exclusion change, rolled the whole change back. Writers have to take
// turns.
func TestUsageRollupWritersTakeTurns(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	seedPersonalAndTeamCodex(t, s, time.Now().UTC())

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // committed below on the happy path
	if err := refreshUsageHourlyRollups(ctx, tx, nil); err != nil {
		t.Fatalf("first writer: %v", err)
	}

	second := make(chan error, 1)
	go func() {
		recent := time.Now().UTC().Add(-2 * time.Hour)
		second <- s.RefreshUsageHourlyRollups(ctx, &recent)
	}()
	select {
	case err := <-second:
		t.Fatalf("second writer finished (err=%v) while the first still held its rows", err)
	case <-time.After(500 * time.Millisecond):
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit first writer: %v", err)
	}
	select {
	case err := <-second:
		if err != nil {
			t.Errorf("second writer failed after the first committed: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("second writer never ran after the first committed")
	}
}

// The recompute runs for minutes on production data -- it rebuilds every session
// overview rollup in the same transaction. It used to empty excluded_sessions with
// TRUNCATE, whose AccessExclusive lock blocked every dashboard read of the
// visible_* views for that whole time: measured on a production copy, the
// dashboard stopped for five minutes after a deploy. Readers must keep seeing
// the previous exclusion set until the new one commits.
func TestRecomputeDoesNotBlockDashboardReads(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	seedPersonalAndTeamCodex(t, s, time.Now().UTC())

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := s.recomputeExcludedSessionsTx(ctx, tx, nil, nil, true); err != nil {
		t.Fatalf("recomputeExcludedSessionsTx: %v", err)
	}

	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SET lock_timeout = '1s'`); err != nil {
		t.Fatalf("set lock_timeout: %v", err)
	}
	for _, view := range []string{"visible_session_records", "visible_events"} {
		var n int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+view).Scan(&n); err != nil {
			t.Errorf("reading %s while a recompute is open: %v", view, err)
		}
	}
}

// The scoped refresh used to take one advisory lock per affected session on top
// of the exclusive maintenance lock the recompute already holds. Excluding an
// account with a few thousand sessions would then exhaust the shared lock table
// (max_locks_per_transaction is 128 on the production server, ~3,200 slots) and
// roll the whole change back with "out of shared memory". Under the exclusive
// lock no other writer can touch the overview, so the per-session locks buy
// nothing.
func TestRecomputeDoesNotTakeALockPerSession(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	var recs []*SessionRecord
	for i := 0; i < 50; i++ {
		r := codexUsageRecord(fmt.Sprintf("codex-personal-%02d", i), "gpt-5.1-codex-max", now.Add(time.Duration(i)*time.Second), 1000, 100, 0)
		r.AccountID = "acct-personal"
		recs = append(recs, r)
	}
	if err := s.InsertSessionRecords(ctx, recs); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO excluded_billing_accounts (billing_provider, account_id, reason, created_by) VALUES ('openai','acct-personal','','admin')`); err != nil {
		t.Fatalf("seed exclusion: %v", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := s.recomputeExcludedSessionsTx(ctx, tx, nil, []BillingAccountRef{{BillingProvider: "openai", AccountID: "acct-personal"}}, false); err != nil {
		t.Fatalf("recomputeExcludedSessionsTx: %v", err)
	}
	var held int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND pid = pg_backend_pid()`).Scan(&held); err != nil {
		t.Fatalf("count locks: %v", err)
	}
	if held > 5 {
		t.Errorf("the recompute holds %d advisory locks for 50 affected sessions; it must not take one per session", held)
	}
}
