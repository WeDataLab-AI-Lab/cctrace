package store

import (
	"context"
	"testing"
	"time"
)

// The case this table exists for: an account with no login email to exclude by.
// excluded_accounts is keyed on login_email and CHECKs it to look like an
// address, so a Codex account -- which carries none on any row -- can never be
// matched by it. Excluding by the billing id is the only key that reaches it.
func TestExcludeBillingAccount_hidesAnAccountWithNoLoginEmail(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	codex := sampleAt(0, "acct-codex", "300")
	codex.BillingProvider = "openai"
	other := sampleAt(60, "acct-other", "300")
	other.BillingProvider = "openai"

	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{codex, other}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}

	hidden, _, err := s.ExcludeBillingAccount(ctx, "openai", "acct-codex", "test", "tester")
	if err != nil {
		t.Fatalf("ExcludeBillingAccount: %v", err)
	}
	if hidden != 1 {
		t.Errorf("reported blast radius = %d, want 1", hidden)
	}

	got, err := s.ListQuotaSamples(ctx, QuotaSampleFilter{})
	if err != nil {
		t.Fatalf("ListQuotaSamples: %v", err)
	}
	if len(got) != 1 || got[0].AccountID != "acct-other" {
		t.Fatalf("returned %d rows (%+v), want only the account that was not excluded", len(got), got)
	}
}

// The same account id under a different provider is a different account. Keying
// on the id alone would exclude an account nobody asked to exclude.
func TestExcludeBillingAccount_isScopedToItsProvider(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	openai := sampleAt(0, "shared-id", "300")
	openai.BillingProvider = "openai"
	anthropic := sampleAt(60, "shared-id", "session")
	anthropic.BillingProvider = "anthropic"

	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{openai, anthropic}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
	if _, _, err := s.ExcludeBillingAccount(ctx, "openai", "shared-id", "", ""); err != nil {
		t.Fatalf("ExcludeBillingAccount: %v", err)
	}

	got, _ := s.ListQuotaSamples(ctx, QuotaSampleFilter{})
	if len(got) != 1 || got[0].BillingProvider != "anthropic" {
		t.Fatalf("returned %+v, want only the anthropic row", got)
	}
}

// Exclusion is reversible policy, not deletion: the rows were never touched.
func TestRemoveExcludedBillingAccount_bringsTheRowsBack(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	row := sampleAt(0, "acct-codex", "300")
	row.BillingProvider = "openai"
	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{row}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
	if _, _, err := s.ExcludeBillingAccount(ctx, "openai", "acct-codex", "", ""); err != nil {
		t.Fatalf("ExcludeBillingAccount: %v", err)
	}
	if got, _ := s.ListQuotaSamples(ctx, QuotaSampleFilter{}); len(got) != 0 {
		t.Fatalf("still returned %d rows while excluded", len(got))
	}

	if err := s.RemoveExcludedBillingAccount(ctx, "openai", "acct-codex"); err != nil {
		t.Fatalf("RemoveExcludedBillingAccount: %v", err)
	}
	if got, _ := s.ListQuotaSamples(ctx, QuotaSampleFilter{}); len(got) != 1 {
		t.Fatalf("returned %d rows after removal, want the row back", len(got))
	}
}

// An account id is an opaque provider token, so case is significant: folding it
// the way login_email is folded would exclude a different account.
func TestExcludeBillingAccount_keepsAccountIDCase(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	row := sampleAt(0, "AcctCodex", "300")
	row.BillingProvider = "openai"
	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{row}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
	if _, _, err := s.ExcludeBillingAccount(ctx, "openai", "acctcodex", "", ""); err != nil {
		t.Fatalf("ExcludeBillingAccount: %v", err)
	}

	if got, _ := s.ListQuotaSamples(ctx, QuotaSampleFilter{}); len(got) != 1 {
		t.Errorf("lowercased id excluded a different account: %d rows left, want 1", len(got))
	}
}

// The listing reports what each entry hides, so an exclusion cannot quietly
// shrink a reported figure with nothing able to explain the gap.
func TestListExcludedBillingAccounts_reportsWhatItHides(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	for i := range 3 {
		row := sampleAt(i*60, "acct-codex", "300")
		row.BillingProvider = "openai"
		if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{row}); err != nil {
			t.Fatalf("InsertQuotaSamples: %v", err)
		}
	}
	if _, _, err := s.ExcludeBillingAccount(ctx, "openai", "acct-codex", "left the org", "admin"); err != nil {
		t.Fatalf("ExcludeBillingAccount: %v", err)
	}

	list, err := s.ListExcludedBillingAccounts(ctx)
	if err != nil {
		t.Fatalf("ListExcludedBillingAccounts: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("listed %d entries, want 1", len(list))
	}
	if list[0].SampleCount != 3 {
		t.Errorf("sample_count = %d, want 3 -- the listing must show its own blast radius", list[0].SampleCount)
	}
	if list[0].Reason != "left the org" || list[0].CreatedBy != "admin" {
		t.Errorf("entry = %+v, want the reason and actor preserved", list[0])
	}
}

// Excluding by billing id must hide the account from every screen, not only the
// quota chart. The reason this table exists -- Codex rows carry no login_email --
// applies just as much to the session and cost paths: those read visible_events
// and visible_session_records, and neither consulted this table, so an excluded
// personal Codex account kept its sessions, tokens and cost on every list while
// only its burn line disappeared.
// gjc and omo rows also carry the billing id on session_records; the events
// view must reach them with the same key, or the session list and the cost
// totals would disagree about whether the account is hidden.
func TestExcludeBillingAccount_hidesGjcEventsByBillingID(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	rec := &SessionRecord{
		Ts: now, SessionID: "gjc-personal", RecordType: "assistant", ProfileEmail: "gjc-profile",
		UserID: "uid-gjc", Model: "claude-opus-5", InputTokens: ptrInt(100), OutputTokens: ptrInt(10),
		Agent: "gjc", BillingProvider: "anthropic", AccountID: "acct-gjc-personal",
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{rec}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if _, events, err := s.ExcludeBillingAccount(ctx, "anthropic", "acct-gjc-personal", "", ""); err != nil {
		t.Fatalf("ExcludeBillingAccount: %v", err)
	} else if events != 1 {
		t.Errorf("reported %d hidden events, want 1", events)
	}

	var visible int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM visible_events WHERE agent = 'gjc'").Scan(&visible); err != nil {
		t.Fatalf("count: %v", err)
	}
	if visible != 0 {
		t.Errorf("visible_events still shows %d gjc rows for the excluded account", visible)
	}
	accounts, err := s.ListExcludedBillingAccounts(ctx)
	if err != nil {
		t.Fatalf("ListExcludedBillingAccounts: %v", err)
	}
	if len(accounts) != 1 || accounts[0].EventCount != 1 {
		t.Fatalf("listed %+v, want one account hiding 1 event", accounts)
	}
}

func TestExcludeBillingAccount_hidesEventsAndSessions(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	personal := codexUsageRecord("codex-personal", "gpt-5.1-codex-max", now, 1000, 100, 0)
	personal.AccountID = "acct-personal"
	team := codexUsageRecord("codex-team", "gpt-5.1-codex-max", now.Add(time.Minute), 1000, 100, 0)
	team.AccountID = "acct-team"
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{personal, team}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.RefreshCodexImputedCost(ctx); err != nil {
		t.Fatalf("RefreshCodexImputedCost: %v", err)
	}

	_, events, err := s.ExcludeBillingAccount(ctx, "openai", "acct-personal", "personal login", "tester")
	if err != nil {
		t.Fatalf("ExcludeBillingAccount: %v", err)
	}
	if events != 1 {
		t.Errorf("reported %d hidden events at exclusion time, want 1", events)
	}

	for _, view := range []string{"visible_events", "visible_session_records"} {
		var hidden, kept int
		if err := s.pool.QueryRow(ctx,
			"SELECT count(*) FILTER (WHERE account_id = 'acct-personal'), count(*) FILTER (WHERE account_id = 'acct-team') FROM "+view,
		).Scan(&hidden, &kept); err != nil {
			t.Fatalf("count %s: %v", view, err)
		}
		if hidden != 0 {
			t.Errorf("%s still shows %d rows for the excluded account", view, hidden)
		}
		if kept != 1 {
			t.Errorf("%s shows %d rows for the account that was not excluded, want 1", view, kept)
		}
	}

	accounts, err := s.ListExcludedBillingAccounts(ctx)
	if err != nil {
		t.Fatalf("ListExcludedBillingAccounts: %v", err)
	}
	if len(accounts) != 1 || accounts[0].EventCount != 1 {
		t.Fatalf("listed %+v, want one account hiding 1 event", accounts)
	}
}
