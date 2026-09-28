package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

var codexQuotaBase = time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)

// codexRec is one seeded session_records row. login_email_source and
// account_id_source are stamped after the insert because nothing on the write
// path accepts them: they are provenance the server decides, and a test that
// needs a row already carrying one is describing a state some earlier pass left
// behind.
type codexRec struct {
	uuid       string
	session    string
	account    string
	email      string
	source     string
	accountSrc string
	agent      string // default "codex"
	provider   string // default "openai"
	// recordType defaults to "user". A test only names it when the row has to be
	// visible to something that filters on it -- codex_imputed_cost takes usage
	// rows and nothing else.
	recordType string
	at         time.Time
}

func seedCodexRecords(t *testing.T, s *PgStore, recs ...codexRec) {
	t.Helper()
	ctx := context.Background()
	out := make([]*SessionRecord, 0, len(recs))
	for i, r := range recs {
		agent := r.agent
		if agent == "" {
			agent = "codex"
		}
		provider := r.provider
		if provider == "" {
			provider = "openai"
		}
		recordType := r.recordType
		if recordType == "" {
			recordType = "user"
		}
		at := r.at
		if at.IsZero() {
			at = codexQuotaBase.Add(time.Duration(i) * time.Minute)
		}
		out = append(out, &SessionRecord{
			Ts: at, SessionID: r.session, RecordType: recordType, UUID: r.uuid,
			UserID: "u-codex", LoginEmail: r.email, AccountID: r.account,
			Agent: agent, BillingProvider: provider, Raw: json.RawMessage(`{}`),
		})
	}
	if err := s.InsertSessionRecords(ctx, out); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	for _, r := range recs {
		if r.source == "" && r.accountSrc == "" {
			continue
		}
		if _, err := s.pool.Exec(ctx,
			`UPDATE session_records SET login_email_source = $2, account_id_source = $3 WHERE uuid = $1`,
			r.uuid, r.source, r.accountSrc); err != nil {
			t.Fatalf("stamp provenance on %s: %v", r.uuid, err)
		}
	}
}

// codexSample is a Codex quota reading. Readings backfilled out of a session log
// carry source_session_id; live ones do not, which is what separates the two
// mappings this file tests.
func codexSample(min int, account, email, sourceSession, attribution string) *QuotaSample {
	wm := 300
	return &QuotaSample{
		BillingProvider: "openai",
		AccountID:       account,
		WindowKey:       "300",
		SampledAt:       codexQuotaBase.Add(time.Duration(min) * time.Minute),
		UsedPct:         float64(min),
		WindowMinutes:   &wm,
		LoginEmail:      email,
		Attribution:     attribution,
		SourceSessionID: sourceSession,
	}
}

func seedCodexSamples(t *testing.T, s *PgStore, samples ...*QuotaSample) {
	t.Helper()
	if _, err := s.InsertQuotaSamples(context.Background(), samples); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
}

func codexAccountOf(t *testing.T, s *PgStore, uuid string) (string, string) {
	t.Helper()
	var account, source string
	if err := s.pool.QueryRow(context.Background(),
		`SELECT account_id, account_id_source FROM session_records WHERE uuid = $1`, uuid).
		Scan(&account, &source); err != nil {
		t.Fatalf("query %s: %v", uuid, err)
	}
	return account, source
}

func rollupLoginScopeRows(t *testing.T, s *PgStore, email string) int64 {
	t.Helper()
	var n int64
	if err := s.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM session_overview_rollups WHERE scope_type = 'login' AND scope_value = $1`,
		email).Scan(&n); err != nil {
		t.Fatalf("count login rollups: %v", err)
	}
	return n
}

func rollupRowsForSession(t *testing.T, s *PgStore, sessionID string) int64 {
	t.Helper()
	var n int64
	if err := s.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM session_overview_rollups WHERE session_id = $1`, sessionID).Scan(&n); err != nil {
		t.Fatalf("count session rollups: %v", err)
	}
	return n
}

// The client resolved which account was logged in when it read the session log
// and recorded that on the sample. Joining it back to the records of the same
// session carries its judgement over; it invents nothing.
//
// Mutation: drop the source_session_id join (attribute account_id from the
// account-level mapping instead) and there is nothing left to key on, because a
// record with no account_id is exactly what this fills.
func TestFillCodexAccountFromQuota_carriesClientResolutionOntoRecords(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s, codexRec{uuid: "c1", session: "sess-a"})
	seedCodexSamples(t, s, codexSample(0, "acct-a", "wedataopenai@example.com", "sess-a", AttributionObserved))

	n, err := s.FillCodexAccountFromQuota(ctx, time.Time{})
	if err != nil {
		t.Fatalf("FillCodexAccountFromQuota: %v", err)
	}
	if n != 1 {
		t.Fatalf("filled %d rows, want 1", n)
	}
	if acct, src := codexAccountOf(t, s, "c1"); acct != "acct-a" || src != "quota" {
		t.Fatalf("c1 = (%q, %q), want (acct-a, quota)", acct, src)
	}
}

// A sample whose own account attribution was inferred cannot make what is derived
// from it observed. The weaker link decides.
//
// Mutation: stamp 'quota' unconditionally and this row claims a measurement that
// was never taken -- and the address derived from it later inherits the claim.
func TestFillCodexAccountFromQuota_inheritsInferredEvidence(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s, codexRec{uuid: "c1", session: "sess-a"})
	seedCodexSamples(t, s,
		codexSample(0, "acct-a", "wedataopenai@example.com", "sess-a", AttributionObserved),
		codexSample(5, "acct-a", "wedataopenai@example.com", "sess-a", AttributionInferred))

	if _, err := s.FillCodexAccountFromQuota(ctx, time.Time{}); err != nil {
		t.Fatalf("FillCodexAccountFromQuota: %v", err)
	}
	if acct, src := codexAccountOf(t, s, "c1"); acct != "acct-a" || src != "quota-inferred" {
		t.Fatalf("c1 = (%q, %q), want (acct-a, quota-inferred)", acct, src)
	}
}

// Samples of one session naming two accounts mean the client's own resolution
// disagreed with itself. There is no majority rule here: the session is refused
// and counted, because a number in AmbiguousSessions is a defect to investigate,
// not work left to do.
//
// Mutation: drop the accounts = 1 filter and MIN(account_id) silently picks the
// alphabetically first of two -- an answer with no evidence behind it.
func TestFillCodexAccountFromQuota_refusesAmbiguousSession(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s, codexRec{uuid: "c1", session: "sess-a"})
	seedCodexSamples(t, s,
		codexSample(0, "acct-a", "one@example.test", "sess-a", AttributionObserved),
		codexSample(5, "acct-b", "two@example.test", "sess-a", AttributionObserved))

	preview, err := s.PreviewCodexAccountFill(ctx, time.Time{})
	if err != nil {
		t.Fatalf("PreviewCodexAccountFill: %v", err)
	}
	if preview.AmbiguousSessions != 1 || preview.FillableRows != 0 {
		t.Fatalf("preview = %+v, want 1 ambiguous session and nothing fillable", preview)
	}
	n, err := s.FillCodexAccountFromQuota(ctx, time.Time{})
	if err != nil {
		t.Fatalf("FillCodexAccountFromQuota: %v", err)
	}
	if n != 0 {
		t.Fatalf("filled %d rows, want 0", n)
	}
	if acct, _ := codexAccountOf(t, s, "c1"); acct != "" {
		t.Fatalf("c1 account = %q, want it left unattributed", acct)
	}
}

// The client read this account id out of auth.json while the session was running.
// A derivation from readings taken around it never overwrites a measurement taken
// during it.
//
// Mutation: drop the empty-account_id clause and one inferred sample of this
// session demotes a value that was observed, which is the confusion
// account_id_source exists to prevent.
func TestFillCodexAccountFromQuota_neverOverwritesClientObservation(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s, codexRec{uuid: "c1", session: "sess-a", account: "acct-client"})
	seedCodexSamples(t, s, codexSample(0, "acct-a", "wedataopenai@example.com", "sess-a", AttributionInferred))

	n, err := s.FillCodexAccountFromQuota(ctx, time.Time{})
	if err != nil {
		t.Fatalf("FillCodexAccountFromQuota: %v", err)
	}
	if n != 0 {
		t.Fatalf("filled %d rows, want 0", n)
	}
	if acct, src := codexAccountOf(t, s, "c1"); acct != "acct-client" || src != "" {
		t.Fatalf("c1 = (%q, %q), want the client's own value with an empty source", acct, src)
	}
}

// Filling an account_id can hide a record: visible_session_records anti-joins
// excluded_billing_accounts on (billing_provider, account_id), and
// session_overview_rollups is built from that view. A pass that wrote the column
// without refreshing the derived rows would leave the session list serving a
// session whose records the view no longer returns.
//
// Mutation: drop refreshLoginEmailDerivedRows from the account pass and the
// rollup rows survive the exclusion.
func TestFillCodexAccountFromQuota_refreshesRollupsForExcludedBillingAccount(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s, codexRec{uuid: "c1", session: "sess-a"})
	seedCodexSamples(t, s, codexSample(0, "acct-a", "wedataopenai@example.com", "sess-a", AttributionObserved))
	if _, _, err := s.ExcludeBillingAccount(ctx, "openai", "acct-a", "test", "tester"); err != nil {
		t.Fatalf("ExcludeBillingAccount: %v", err)
	}
	if before := rollupRowsForSession(t, s, "sess-a"); before == 0 {
		t.Fatal("session has no rollup rows before the fill, so the test proves nothing")
	}

	if _, err := s.FillCodexAccountFromQuota(ctx, time.Time{}); err != nil {
		t.Fatalf("FillCodexAccountFromQuota: %v", err)
	}
	if after := rollupRowsForSession(t, s, "sess-a"); after != 0 {
		t.Fatalf("%d rollup rows survive the exclusion the fill just triggered, want 0", after)
	}
}

// The mapping quota_samples observed is what a Codex record's account bills. This
// is the pass that puts wedataopenai@ on screen.
//
// Mutation: key the mapping on profile_email and the same account splits into one
// row per reporting profile (store.go:813-817).
func TestAttributeCodexLoginEmail_appliesObservedMapping(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s, codexRec{uuid: "c1", session: "sess-a", account: "acct-a"})
	seedCodexSamples(t, s, codexSample(0, "acct-a", "wedataopenai@example.com", "", AttributionObserved))

	n, err := s.AttributeCodexLoginEmailFromQuota(ctx, time.Time{})
	if err != nil {
		t.Fatalf("AttributeCodexLoginEmailFromQuota: %v", err)
	}
	if n != 1 {
		t.Fatalf("attributed %d rows, want 1", n)
	}
	if email, src := loginEmailSourceOf(t, s, "c1"); email != "wedataopenai@example.com" || src != "quota" {
		t.Fatalf("c1 = (%q, %q), want (wedataopenai@example.com, quota)", email, src)
	}
}

// #524 itself: a codex row stamped with a Claude account from the user's OTEL
// timeline, an address quota_samples has never observed on any OpenAI account.
// The correction is not "leave it alone because something is already there" --
// 'inferred' is in reach precisely so a better rule can revisit a guess.
//
// Mutation: refuse non-empty login_email (as inferMatch does) and the wrong
// address stays on 344,034 production rows forever.
func TestAttributeCodexLoginEmail_correctsTheOtelGuess(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s, codexRec{
		uuid: "c1", session: "sess-a", account: "acct-a",
		email: "user-a@example.com", source: "inferred",
	})
	seedCodexSamples(t, s, codexSample(0, "acct-a", "wedataopenai@example.com", "", AttributionObserved))

	if _, err := s.AttributeCodexLoginEmailFromQuota(ctx, time.Time{}); err != nil {
		t.Fatalf("AttributeCodexLoginEmailFromQuota: %v", err)
	}
	if email, src := loginEmailSourceOf(t, s, "c1"); email != "wedataopenai@example.com" || src != "quota" {
		t.Fatalf("c1 = (%q, %q), want the quota mapping to win over the OTEL guess", email, src)
	}
}

// An observation is never replaced by a derivation, exactly as in backfillMatch.
//
// Mutation: drop the 'otel' guard and a measured account is overwritten by a
// mapping, which loses information rather than correcting anything.
func TestAttributeCodexLoginEmail_neverOverwritesObservation(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s, codexRec{
		uuid: "c1", session: "sess-a", account: "acct-a",
		email: "observed@example.test", source: "otel",
	})
	seedCodexSamples(t, s, codexSample(0, "acct-a", "wedataopenai@example.com", "", AttributionObserved))

	n, err := s.AttributeCodexLoginEmailFromQuota(ctx, time.Time{})
	if err != nil {
		t.Fatalf("AttributeCodexLoginEmailFromQuota: %v", err)
	}
	if n != 0 {
		t.Fatalf("attributed %d rows, want 0", n)
	}
	if email, src := loginEmailSourceOf(t, s, "c1"); email != "observed@example.test" || src != "otel" {
		t.Fatalf("c1 = (%q, %q), want the observation untouched", email, src)
	}
}

// The mapping being observed is not enough on its own: an address is only as good
// as the account id it was looked up by. A record whose account was itself derived
// from an inferred sample gets the weaker provenance.
//
// Mutation: write 'quota' whenever the mapping is observed and a chain with an
// inferred link reports itself as a measurement -- and loginEmailInferredExpr then
// stops marking it on screen.
func TestAttributeCodexLoginEmail_demotesInferredAccountEvidence(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s, codexRec{
		uuid: "c1", session: "sess-a", account: "acct-a", accountSrc: "quota-inferred",
	})
	seedCodexSamples(t, s, codexSample(0, "acct-a", "wedataopenai@example.com", "", AttributionObserved))

	if _, err := s.AttributeCodexLoginEmailFromQuota(ctx, time.Time{}); err != nil {
		t.Fatalf("AttributeCodexLoginEmailFromQuota: %v", err)
	}
	if email, src := loginEmailSourceOf(t, s, "c1"); email != "wedataopenai@example.com" || src != "quota-inferred" {
		t.Fatalf("c1 = (%q, %q), want the weaker provenance to survive the join", email, src)
	}
}

// One account reported under two addresses is not a mapping, and there is nothing
// to pick between them. It is refused and counted.
//
// Mutation: drop the emails = 1 filter and MIN(login_email) puts one of the two on
// every record of that account.
func TestAttributeCodexLoginEmail_refusesAccountWithTwoAddresses(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s, codexRec{uuid: "c1", session: "sess-a", account: "acct-a"})
	seedCodexSamples(t, s,
		codexSample(0, "acct-a", "one@example.test", "", AttributionObserved),
		codexSample(5, "acct-a", "two@example.test", "", AttributionObserved))

	preview, err := s.PreviewCodexQuotaAttribution(ctx, time.Time{})
	if err != nil {
		t.Fatalf("PreviewCodexQuotaAttribution: %v", err)
	}
	if preview.AmbiguousAccounts != 1 || preview.MappedAccounts != 0 || preview.RewriteRows != 0 {
		t.Fatalf("preview = %+v, want one ambiguous account and no mapping", preview)
	}
	if preview.UnmappedAccountRows != 1 {
		t.Fatalf("unmapped_account_rows = %d, want 1", preview.UnmappedAccountRows)
	}
	n, err := s.AttributeCodexLoginEmailFromQuota(ctx, time.Time{})
	if err != nil {
		t.Fatalf("AttributeCodexLoginEmailFromQuota: %v", err)
	}
	if n != 0 {
		t.Fatalf("attributed %d rows, want 0", n)
	}
}

// The periodic pass runs every tick against a hypertable. A second run over
// unchanged data must write nothing at all, or the pass churns storage forever for
// no new evidence.
//
// Mutation: drop the trailing inequality from codexQuotaMatch and every tick
// rewrites every codex row it has already attributed.
func TestAttributeCodexLoginEmail_isIdempotent(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s, codexRec{uuid: "c1", session: "sess-a", account: "acct-a"})
	seedCodexSamples(t, s, codexSample(0, "acct-a", "wedataopenai@example.com", "", AttributionObserved))

	if n, err := s.AttributeCodexLoginEmailFromQuota(ctx, time.Time{}); err != nil || n != 1 {
		t.Fatalf("first pass = (%d, %v), want (1, nil)", n, err)
	}
	n, err := s.AttributeCodexLoginEmailFromQuota(ctx, time.Time{})
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if n != 0 {
		t.Fatalf("second pass rewrote %d rows, want 0", n)
	}
}

// Reattributable has to match what the attribute phase can actually reach, which
// requires billing_provider = 'openai' and no queued deletion -- codexQuotaMatch's
// own gates. A row inside deleted_sessions would count here and then never get
// rewritten, so the preview would promise a repair the apply phase does not do.
//
// Mutation: drop either gate from the Reattributable FILTER and this row keeps
// inflating the count.
func TestPreviewCodexInferredRevert_excludesDeletedSessionFromReattributable(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s,
		codexRec{uuid: "c1", session: "sess-live", account: "acct-a",
			email: "user-a@example.com", source: "inferred"},
		codexRec{uuid: "c2", session: "sess-deleted", account: "acct-a",
			email: "user-a@example.com", source: "inferred"},
	)
	seedCodexSamples(t, s, codexSample(0, "acct-a", "wedataopenai@example.com", "", AttributionObserved))
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO deleted_sessions (session_id) VALUES ($1) ON CONFLICT DO NOTHING`, "sess-deleted"); err != nil {
		t.Fatalf("queue deletion: %v", err)
	}

	preview, err := s.PreviewCodexInferredRevert(ctx)
	if err != nil {
		t.Fatalf("PreviewCodexInferredRevert: %v", err)
	}
	if preview.RevertRows != 2 {
		t.Fatalf("revert_rows = %d, want 2 -- the revert phase itself has no deleted_sessions anti-join", preview.RevertRows)
	}
	if preview.Reattributable != 1 {
		t.Fatalf("reattributable = %d, want 1 -- the deleted session's row is not reachable by the attribute phase", preview.Reattributable)
	}
}

// The one rewrite the idempotence inequality must still allow. Evidence got
// stronger -- the account was finally observed rather than backfilled -- so the
// provenance follows it up, and loginEmailInferredExpr stops marking the row as a
// guess.
//
// Mutation: compare only login_email in that inequality and the promotion never
// happens; the row keeps claiming an inferred chain it no longer has.
func TestAttributeCodexLoginEmail_promotesOnObservedArrival(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s, codexRec{uuid: "c1", session: "sess-a", account: "acct-a"})
	seedCodexSamples(t, s, codexSample(0, "acct-a", "wedataopenai@example.com", "", AttributionInferred))
	if _, err := s.AttributeCodexLoginEmailFromQuota(ctx, time.Time{}); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if _, src := loginEmailSourceOf(t, s, "c1"); src != "quota-inferred" {
		t.Fatalf("c1 source = %q after inferred-only evidence, want quota-inferred", src)
	}

	seedCodexSamples(t, s, codexSample(10, "acct-a", "wedataopenai@example.com", "", AttributionObserved))
	n, err := s.AttributeCodexLoginEmailFromQuota(ctx, time.Time{})
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if n != 1 {
		t.Fatalf("promoted %d rows, want 1", n)
	}
	if _, src := loginEmailSourceOf(t, s, "c1"); src != "quota" {
		t.Fatalf("c1 source = %q, want quota once the account was observed", src)
	}
}

// Four profiles polling one account is the production shape, and it is the case
// store.go:813-817 forbids keying on profile_email for. The mapping has to stay
// one row per account no matter how many clients report it: a per-profile key
// would multiply every record joined to it by four.
//
// Mutation: add profile_email to the GROUP BY and this row is updated four times
// under one UPDATE, or the join fans out and the count comes back as 4.
func TestAttributeCodexLoginEmail_oneAccountReportedByFourProfiles(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s, codexRec{uuid: "c1", session: "sess-a", account: "acct-a"})
	var samples []*QuotaSample
	for i, profile := range []string{"a@example.com", "b@example.com", "c@example.com", "d@example.com"} {
		q := codexSample(i, "acct-a", "wedataopenai@example.com", "", AttributionObserved)
		q.ProfileEmail = profile
		samples = append(samples, q)
	}
	seedCodexSamples(t, s, samples...)

	preview, err := s.PreviewCodexQuotaAttribution(ctx, time.Time{})
	if err != nil {
		t.Fatalf("PreviewCodexQuotaAttribution: %v", err)
	}
	if preview.MappedAccounts != 1 || preview.RewriteRows != 1 {
		t.Fatalf("preview = %+v, want one account mapping one row", preview)
	}
	n, err := s.AttributeCodexLoginEmailFromQuota(ctx, time.Time{})
	if err != nil {
		t.Fatalf("AttributeCodexLoginEmailFromQuota: %v", err)
	}
	if n != 1 {
		t.Fatalf("attributed %d rows, want 1 -- four reporters are one account", n)
	}
	if email, _ := loginEmailSourceOf(t, s, "c1"); email != "wedataopenai@example.com" {
		t.Fatalf("c1 email = %q, want the single address all four reported", email)
	}
}

// An operator approves a preview and gets an UPDATE. If the two run different
// predicates the number they approved was never the number that ran.
//
// Mutation: change either predicate in only one of the two statements.
func TestCodexQuotaPreviewMatchesApply(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s,
		codexRec{uuid: "c1", session: "sess-a"},
		codexRec{uuid: "c2", session: "sess-a"},
		codexRec{uuid: "c3", session: "sess-b", account: "acct-b"},
		codexRec{uuid: "c4", session: "sess-c", email: "observed@example.test", source: "otel", account: "acct-a"},
		codexRec{uuid: "c5", session: "sess-d", agent: "claude", provider: "anthropic"},
	)
	seedCodexSamples(t, s,
		codexSample(0, "acct-a", "wedataopenai@example.com", "sess-a", AttributionObserved),
		codexSample(5, "acct-b", "wedata_ai@example.com", "", AttributionObserved))

	fillPreview, err := s.PreviewCodexAccountFill(ctx, time.Time{})
	if err != nil {
		t.Fatalf("PreviewCodexAccountFill: %v", err)
	}
	filled, err := s.FillCodexAccountFromQuota(ctx, time.Time{})
	if err != nil {
		t.Fatalf("FillCodexAccountFromQuota: %v", err)
	}
	if fillPreview.FillableRows != filled {
		t.Fatalf("account preview said %d rows, apply wrote %d", fillPreview.FillableRows, filled)
	}

	attrPreview, err := s.PreviewCodexQuotaAttribution(ctx, time.Time{})
	if err != nil {
		t.Fatalf("PreviewCodexQuotaAttribution: %v", err)
	}
	attributed, err := s.AttributeCodexLoginEmailFromQuota(ctx, time.Time{})
	if err != nil {
		t.Fatalf("AttributeCodexLoginEmailFromQuota: %v", err)
	}
	if attrPreview.RewriteRows != attributed {
		t.Fatalf("attribution preview said %d rows, apply wrote %d", attrPreview.RewriteRows, attributed)
	}
	if attrPreview.ObservedRows+attrPreview.InferredRows != attrPreview.RewriteRows {
		t.Fatalf("provenance split %+v does not add up to the rewrite total", attrPreview)
	}
}

// The bounded shape exists so the periodic pass does not rescan all of history
// every tick. It has to actually bound something.
//
// Mutation: drop codexQuotaSinceBound from either builder and the bounded pass
// writes the row it was told to skip.
func TestCodexQuotaPassesRespectSince(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s,
		codexRec{uuid: "old-fill", session: "sess-a", at: codexQuotaBase},
		codexRec{uuid: "old-attr", session: "sess-b", account: "acct-a", at: codexQuotaBase})
	seedCodexSamples(t, s,
		codexSample(0, "acct-a", "wedataopenai@example.com", "sess-a", AttributionObserved))

	since := codexQuotaBase.Add(time.Hour)
	if n, err := s.FillCodexAccountFromQuota(ctx, since); err != nil || n != 0 {
		t.Fatalf("bounded fill = (%d, %v), want (0, nil)", n, err)
	}
	if n, err := s.AttributeCodexLoginEmailFromQuota(ctx, since); err != nil || n != 0 {
		t.Fatalf("bounded attribution = (%d, %v), want (0, nil)", n, err)
	}
	if n, err := s.FillCodexAccountFromQuota(ctx, time.Time{}); err != nil || n != 1 {
		t.Fatalf("unbounded fill = (%d, %v), want (1, nil)", n, err)
	}
	if n, err := s.AttributeCodexLoginEmailFromQuota(ctx, time.Time{}); err != nil || n != 2 {
		t.Fatalf("unbounded attribution = (%d, %v), want (2, nil)", n, err)
	}
}

// backfillMatch admits a blank provenance and 'inferred' and nothing else, so what
// the quota passes write is out of the OTEL backfill's reach without any change to
// it. This is the assertion that keeps it that way.
//
// Mutation: add 'quota' to backfillMatch's admitted set and a Claude OTEL
// observation on this session overwrites an OpenAI account attribution.
func TestBackfillLeavesQuotaAttributionAlone(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s, codexRec{
		uuid: "c1", session: "sess-a", account: "acct-a",
		email: "wedataopenai@example.com", source: "quota",
	})
	if err := s.InsertEvents(ctx, []*OtelEvent{{
		Ts: codexQuotaBase, EventName: "api_request", SessionID: "sess-a",
		UserID: "u-codex", LoginEmail: "user-a@example.com",
	}}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	if _, err := s.BackfillSessionRecordLoginEmail(ctx, time.Time{}); err != nil {
		t.Fatalf("BackfillSessionRecordLoginEmail: %v", err)
	}
	if email, src := loginEmailSourceOf(t, s, "c1"); email != "wedataopenai@example.com" || src != "quota" {
		t.Fatalf("c1 = (%q, %q), want the quota attribution untouched", email, src)
	}
}

// The other half of the same guarantee, on the pass that caused #524. Even with a
// dense Claude timeline for this user, a codex row is not its business -- and a
// row already attributed from quota is doubly out of reach.
//
// Mutation: remove the codex guard from inferMatch and the OTEL timeline restates
// its answer over the quota mapping the moment the row goes blank.
func TestInferLeavesQuotaAttributedCodexRowsAlone(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	seedCodexRecords(t, s,
		codexRec{uuid: "c1", session: "sess-a", account: "acct-a",
			email: "wedataopenai@example.com", source: "quota"},
		codexRec{uuid: "c2", session: "sess-b"},
	)
	if err := s.InsertEvents(ctx, []*OtelEvent{{
		Ts: codexQuotaBase.Add(-time.Hour), EventName: "api_request", SessionID: "sess-otel",
		UserID: "u-codex", LoginEmail: "user-a@example.com",
	}}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	n, err := s.InferSessionRecordLoginEmail(ctx, time.Time{})
	if err != nil {
		t.Fatalf("InferSessionRecordLoginEmail: %v", err)
	}
	if n != 0 {
		t.Fatalf("inference wrote %d codex rows, want 0", n)
	}
	if email, src := loginEmailSourceOf(t, s, "c1"); email != "wedataopenai@example.com" || src != "quota" {
		t.Fatalf("c1 = (%q, %q), want the quota attribution untouched", email, src)
	}
	if email, src := loginEmailSourceOf(t, s, "c2"); email != "" || src != "" {
		t.Fatalf("c2 = (%q, %q), want the blank codex row left blank", email, src)
	}
}
