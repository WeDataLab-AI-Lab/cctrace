package store

import (
	"context"
	"time"
)

// The three passes in this file repair and then maintain Codex account
// attribution from quota_samples, which is the only source on the server that
// knows anything about OpenAI billing.
//
// They exist because #357 shipped an OTEL-timeline inference with no agent guard
// while #372 had just built excluded_billing_accounts on the premise that codex
// rows carry no login_email at all. The result was 1,236,569 codex rows stamped
// login_email_source = 'inferred' from a Claude-only table, 344,034 of them with
// an address quota_samples has never once observed on an OpenAI account (#524).
//
// profile_email is deliberately not a key anywhere below. store.go:813-817 says
// why: several profiles report one billing account, so keying on the reporting
// profile splits one account into N and counts it N times. account_id is the
// account; profile_email is only who happened to be looking at it.
//
// Each pass keeps its predicate in one constant that both the preview and the
// UPDATE concatenate, so the row count an operator approves is the row count the
// statement produces. Full-history and bounded shapes are separate literals for
// the reason postgres_backfill.go gives: a nullable optional predicate forces one
// generic plan to serve both an unbounded scan and a selective one.

// codexQuotaSinceBound is the only place `since` enters these statements. Unlike
// the OTEL passes there is no timeline to truncate here -- the evidence is a
// per-account constant, not a series -- so bounding the records is safe on its
// own and needs no active-key discovery CTE.
const codexQuotaSinceBound = `
	  AND sr.ts >= $1::timestamptz`

func codexQuotaScope(match string, since time.Time) (string, []any) {
	if since.IsZero() {
		return match, nil
	}
	return match + codexQuotaSinceBound, []any{since}
}

// codexRevertMatch selects the rows #524 attributed and nothing else.
//
// 'inferred' on a codex row cannot be anything but that defect: otel_events is
// written by the Anthropic exporter alone, so the timeline the inference pass
// reads holds no evidence about OpenAI billing, and after the guard in inferMatch
// no new row can reach this state. Clearing both the value and its provenance rather
// than overwriting is the point -- a row this pass cannot attribute from
// quota_samples has no known account, and blank is a state the product already
// handles (OnlyUnattributed, the non-empty filter in ListLoginAccounts) whereas a
// plausible wrong address is not.
//
// No deleted_sessions anti-join. Removing a fiction is correct on a session queued
// for deletion too, and skipping those rows would leave the defect behind in
// exactly the rows nobody is going to look at again.
const codexRevertMatch = `
	  AND ` + sqlAgentKind + ` = 'codex'
	  AND sr.login_email_source = 'inferred'`

// codexQuotaSessionAccountCTE maps a Codex session to the billing account its
// quota readings were credited to.
//
// This is not a new inference rule. The client already decided which account was
// logged in when it read the session log (internal/syncer/quota_account.go's
// resolveQuotaAccount) and recorded the decision on the sample together with
// source_session_id, the UUID of the file it came from. Joining that back to
// session_records.session_id carries the client's own judgement onto the records
// of the same session; on production the two agreed on all 400,349 rows where
// both were present, with no counterexample.
//
// A session whose samples disagree about the account is dropped, not resolved:
// `accounts = 1` is the whole admission rule. MIN(account_id) is then just the
// idiom for reading the single surviving value out of a group -- the filter, not
// MIN, is what makes it the right one.
//
// any_inferred carries the weaker half of the evidence forward. A sample whose own
// attribution was inferred cannot make an account_id derived from it observed.
const codexQuotaSessionAccountCTE = `
	WITH session_account_all AS (
		SELECT source_session_id, COUNT(DISTINCT account_id) AS accounts,
		       MIN(account_id) AS account_id,
		       bool_or(attribution = 'inferred') AS any_inferred
		FROM quota_samples
		WHERE billing_provider = 'openai' AND source_session_id <> '' AND account_id <> ''
		GROUP BY source_session_id
	), session_account AS (
		SELECT source_session_id AS session_id, account_id, any_inferred
		FROM session_account_all WHERE accounts = 1
	)`

// codexAccountFillMatch admits only records with no account_id at all.
//
// The empty-account_id clause is the load-bearing one. A non-empty account_id was read
// out of auth.json by the client at the moment the session ran, which is a direct
// observation of the account in use; the join above is a derivation from readings
// taken around it. Letting the derivation write over the observation would let one
// inferred sample of a session demote a value that was measured, which is the
// mistake account_id_source exists to keep visible in the first place.
const codexAccountFillMatch = `
	  AND ` + sqlAgentKind + ` = 'codex'
	  AND sr.billing_provider = 'openai'
	  AND sr.account_id = ''
	  AND sr.session_id <> ''
	  AND NOT EXISTS (SELECT 1 FROM deleted_sessions d WHERE d.session_id = sr.session_id)`

// codexAccountSourceExpr stamps where the filled account_id came from, taking the
// weaker of the two provenances behind it.
const codexAccountSourceExpr = `CASE WHEN a.any_inferred THEN 'quota-inferred' ELSE 'quota' END`

// codexQuotaAccountEmailCTE maps a billing account to the login email it bills.
//
// Keyed on account_id, never on profile_email: the same account is reported by
// four to seven profiles on production, and every one of them reports the same
// login_email for it, so grouping by the reporter would produce N identical rows
// and multiply whatever is joined to them (store.go:813-817).
//
// An account observed under two different addresses is dropped rather than
// guessed at -- `emails = 1` is the admission rule, and MIN(login_email) reads the
// single surviving value out of the group. lower() only in the comparison: the
// value is stored exactly as the client sent it and is written back verbatim, the
// way BackfillSessionRecordLoginEmail does, because nothing lowercases on ingest.
//
// observed says at least one sample behind the mapping was measured rather than
// backfilled across a gap.
const codexQuotaAccountEmailCTE = `
	WITH account_email_all AS (
		SELECT account_id, COUNT(DISTINCT lower(login_email)) AS emails,
		       MIN(login_email) AS login_email,
		       bool_or(attribution = 'observed') AS observed
		FROM quota_samples
		WHERE billing_provider = 'openai' AND account_id <> '' AND login_email <> ''
		GROUP BY account_id
	), account_email AS (
		SELECT account_id, login_email, observed
		FROM account_email_all WHERE emails = 1
	)`

// codexQuotaSourceExpr is the provenance written with the attributed address, and
// it is the weaker of the two links in the chain. The mapping being observed is
// not enough on its own: if this record's account_id was itself derived from an
// inferred sample, then the address is only as good as that derivation.
const codexQuotaSourceExpr = `CASE WHEN m.observed AND sr.account_id_source <> 'quota-inferred'
		           THEN 'quota' ELSE 'quota-inferred' END`

// codexQuotaMatch pairs a Codex record with the address its account bills.
//
// 'otel' is out of reach, exactly as in backfillMatch: an observation is never
// replaced by a derivation. Everything weaker is in reach, including this pass's
// own output, which is what the trailing inequality is for. It makes the pass
// idempotent -- a row already carrying this address under this provenance does not
// match, so a second run updates nothing and does not churn the hypertable -- while
// still admitting the one change worth making: a 'quota-inferred' row whose account
// has since been observed is promoted to 'quota'.
//
// backfillMatch needs no change to protect what is written here. It admits only a
// blank provenance and 'inferred', so 'quota' and 'quota-inferred' are out of its reach already. A
// codex row this pass leaves blank is still reachable by it, which is correct: if
// an OTEL observation for that very session ever appears, an observation beats no
// attribution at all.
const codexQuotaMatch = `
	  AND ` + sqlAgentKind + ` = 'codex'
	  AND sr.billing_provider = 'openai'
	  AND sr.account_id <> ''
	  AND sr.login_email_source <> 'otel'
	  AND NOT EXISTS (SELECT 1 FROM deleted_sessions d WHERE d.session_id = sr.session_id)
	  AND (sr.login_email <> m.login_email OR sr.login_email_source <> ` + codexQuotaSourceExpr + `)`

// CodexInferredRevertPreview reports what reverting #524's attributions removes.
type CodexInferredRevertPreview struct {
	// RevertRows is every codex row carrying an OTEL-derived guess: the size of
	// the fiction.
	RevertRows int64 `json:"revert_rows"`
	// RevertSessions is how many distinct sessions those rows span.
	RevertSessions int64 `json:"revert_sessions"`
	// Reattributable is how many of RevertRows the attribute pass can immediately
	// replace with a mapped address, because they already carry an account_id the
	// quota mapping knows. Counted apart because "we are blanking 1.2M rows" and
	// "and re-filling 350k of them correctly in the same run" are different facts,
	// and an operator approving the first is entitled to the second. Carries the
	// same billing_provider and deleted_sessions gates codexQuotaMatch requires, so
	// the count matches the rows the attribute phase actually reaches.
	Reattributable int64 `json:"reattributable"`
}

// PreviewCodexInferredRevert measures what the revert phase would clear, reading
// only. It is unscoped by design: the defect is not confined to a recent window,
// and a partial revert would leave the rest of the fiction on screen.
func (s *PgStore) PreviewCodexInferredRevert(ctx context.Context) (*CodexInferredRevertPreview, error) {
	p := &CodexInferredRevertPreview{}
	if err := s.pool.QueryRow(ctx, codexInferredRevertPreviewSQL()).
		Scan(&p.RevertRows, &p.RevertSessions, &p.Reattributable); err != nil {
		return nil, err
	}
	return p, nil
}

func codexInferredRevertPreviewSQL() string {
	// WHERE true so the shared predicate can keep its leading AND and be
	// concatenated by the batch statements, which do carry a cursor predicate.
	return codexQuotaAccountEmailCTE + `
	SELECT COUNT(*), COUNT(DISTINCT sr.session_id),
	       COUNT(*) FILTER (WHERE m.account_id IS NOT NULL
	                         AND sr.billing_provider = 'openai'
	                         AND NOT EXISTS (SELECT 1 FROM deleted_sessions d WHERE d.session_id = sr.session_id))
	FROM session_records sr
	LEFT JOIN account_email m ON m.account_id = sr.account_id AND sr.account_id <> ''
	WHERE true` + codexRevertMatch
}

// CodexAccountFillPreview reports what the account-fill pass would write.
type CodexAccountFillPreview struct {
	// MissingAccountRows is every Codex row with no account at all, regardless of
	// scope: the size of the gap this pass exists to close.
	MissingAccountRows int64 `json:"missing_account_rows"`
	// FillableRows is how many of those this pass would fill; the number the
	// UPDATE returns.
	FillableRows int64 `json:"fillable_rows"`
	// InferredEvidenceRows is how many of FillableRows rest on at least one
	// inferred sample and are therefore stamped 'quota-inferred'. They are not
	// worth less than the rest, but an operator should see the split before
	// approving rather than discover it in the provenance column afterwards.
	InferredEvidenceRows int64 `json:"inferred_evidence_rows"`
	// FillableSessions is how many distinct sessions the pass would touch.
	FillableSessions int64 `json:"fillable_sessions"`
	// AmbiguousSessions is how many sessions were refused because their samples
	// name more than one account, regardless of scope: session_account_all is built
	// over all of quota_samples with no `since` bound. On production this is 0; a
	// number here says the client's own account resolution disagreed with itself,
	// which is a defect to investigate rather than an amount of work left to do.
	AmbiguousSessions int64 `json:"ambiguous_sessions"`
}

// PreviewCodexAccountFill measures the blast radius of FillCodexAccountFromQuota
// for the same `since`, reading only, through the identical join and predicate.
func (s *PgStore) PreviewCodexAccountFill(ctx context.Context, since time.Time) (*CodexAccountFillPreview, error) {
	q, args := codexAccountFillPreviewSQL(since)
	p := &CodexAccountFillPreview{}
	if err := s.pool.QueryRow(ctx, q, args...).
		Scan(&p.MissingAccountRows, &p.AmbiguousSessions, &p.FillableRows,
			&p.InferredEvidenceRows, &p.FillableSessions); err != nil {
		return nil, err
	}
	return p, nil
}

func codexAccountFillPreviewSQL(since time.Time) (string, []any) {
	match, args := codexQuotaScope(codexAccountFillMatch, since)
	return codexQuotaSessionAccountCTE + `
	SELECT
		(SELECT COUNT(*) FROM session_records sr
		 WHERE ` + sqlAgentKind + ` = 'codex' AND sr.billing_provider = 'openai'
		   AND sr.account_id = ''),
		(SELECT COUNT(*) FROM session_account_all WHERE accounts > 1),
		COUNT(*),
		COUNT(*) FILTER (WHERE a.any_inferred),
		COUNT(DISTINCT sr.session_id)
	FROM session_records sr
	JOIN session_account a ON a.session_id = sr.session_id` + match, args
}

// FillCodexAccountFromQuota carries the client's own account resolution from the
// quota readings of a session onto the records of that same session.
//
// It runs before the attribution pass on purpose: an account_id filled here is
// what makes the same record attributable in the same run, and on production this
// is the difference between 352,631 and 458,762 repaired rows.
//
// The derived tables are refreshed even though this pass writes no login_email.
// visible_session_records anti-joins excluded_billing_accounts on
// (billing_provider, account_id), so filling an account can hide a record that was
// visible a moment ago, and session_overview_rollups is built from that view. A
// pass that skipped the refresh here would leave the rollup asserting a session is
// visible whose records the view no longer returns.
func (s *PgStore) FillCodexAccountFromQuota(ctx context.Context, since time.Time) (int64, error) {
	q, args := codexAccountFillApplySQL(since)
	return s.applyCodexQuotaUpdate(ctx, q, args)
}

func codexAccountFillApplySQL(since time.Time) (string, []any) {
	match, args := codexQuotaScope(codexAccountFillMatch, since)
	return codexQuotaSessionAccountCTE + `
	UPDATE session_records sr
	SET account_id = a.account_id, account_id_source = ` + codexAccountSourceExpr + `
	FROM session_account a
	WHERE sr.session_id = a.session_id` + match + `
	RETURNING sr.session_id`, args
}

// CodexQuotaAttributionPreview reports what the attribution pass would write.
type CodexQuotaAttributionPreview struct {
	// AccountRows is every Codex row that carries an account id, regardless of
	// scope: the population an account-to-address mapping can reach at all.
	AccountRows int64 `json:"account_rows"`
	// RewriteRows is how many rows this pass would write; the number the UPDATE
	// returns. On a second run of an unchanged database it is 0.
	RewriteRows int64 `json:"rewrite_rows"`
	// ObservedRows and InferredRows split RewriteRows by the provenance each row
	// would be stamped with.
	ObservedRows int64 `json:"observed_rows"`
	InferredRows int64 `json:"inferred_rows"`
	// UnmappedAccountRows is the remainder: rows whose account quota_samples has
	// never reported an address for. They stay blank, which is the honest answer
	// and not a shortfall to fix by guessing.
	UnmappedAccountRows int64 `json:"unmapped_account_rows"`
	// MappedAccounts is how many accounts the mapping covers, AmbiguousAccounts how
	// many it refused for naming two addresses. On production the first is 2 and
	// the second 0; a non-zero second number means the mapping's premise is wrong
	// and this pass should not be approved on it.
	MappedAccounts    int64 `json:"mapped_accounts"`
	AmbiguousAccounts int64 `json:"ambiguous_accounts"`
}

// PreviewCodexQuotaAttribution measures the blast radius of
// AttributeCodexLoginEmailFromQuota for the same `since`, reading only, through
// the identical mapping and predicate.
func (s *PgStore) PreviewCodexQuotaAttribution(ctx context.Context, since time.Time) (*CodexQuotaAttributionPreview, error) {
	q, args := codexQuotaAttributionPreviewSQL(since)
	p := &CodexQuotaAttributionPreview{}
	if err := s.pool.QueryRow(ctx, q, args...).
		Scan(&p.AccountRows, &p.UnmappedAccountRows, &p.MappedAccounts, &p.AmbiguousAccounts,
			&p.RewriteRows, &p.ObservedRows, &p.InferredRows); err != nil {
		return nil, err
	}
	return p, nil
}

func codexQuotaAttributionPreviewSQL(since time.Time) (string, []any) {
	match, args := codexQuotaScope(codexQuotaMatch, since)
	return codexQuotaAccountEmailCTE + `
	SELECT
		(SELECT COUNT(*) FROM session_records sr
		 WHERE ` + sqlAgentKind + ` = 'codex' AND sr.billing_provider = 'openai'
		   AND sr.account_id <> ''),
		(SELECT COUNT(*) FROM session_records sr
		 WHERE ` + sqlAgentKind + ` = 'codex' AND sr.billing_provider = 'openai'
		   AND sr.account_id <> ''
		   AND NOT EXISTS (SELECT 1 FROM account_email e WHERE e.account_id = sr.account_id)),
		(SELECT COUNT(*) FROM account_email),
		(SELECT COUNT(*) FROM account_email_all WHERE emails > 1),
		COUNT(*),
		COUNT(*) FILTER (WHERE ` + codexQuotaSourceExpr + ` = 'quota'),
		COUNT(*) FILTER (WHERE ` + codexQuotaSourceExpr + ` = 'quota-inferred')
	FROM session_records sr
	JOIN account_email m ON m.account_id = sr.account_id` + match, args
}

// AttributeCodexLoginEmailFromQuota writes the subscription account a Codex
// record billed, taken from the account-to-address mapping quota_samples
// observed.
//
// This is the axis decision #524 turned on. Branching in the display layer would
// have left the wrong address in the column and every row without an account_id
// still unattributed; correcting the column itself reaches both the dashboard and
// codex_imputed_cost, which copies login_email and account_id straight out of
// these rows.
func (s *PgStore) AttributeCodexLoginEmailFromQuota(ctx context.Context, since time.Time) (int64, error) {
	q, args := codexQuotaAttributionApplySQL(since)
	return s.applyCodexQuotaUpdate(ctx, q, args)
}

func codexQuotaAttributionApplySQL(since time.Time) (string, []any) {
	match, args := codexQuotaScope(codexQuotaMatch, since)
	return codexQuotaAccountEmailCTE + `
	UPDATE session_records sr
	SET login_email = m.login_email, login_email_source = ` + codexQuotaSourceExpr + `
	FROM account_email m
	WHERE sr.account_id = m.account_id` + match + `
	RETURNING sr.session_id`, args
}

// applyCodexQuotaUpdate is the shape both periodic passes share: the shared
// mutation lock, one UPDATE returning its touched sessions, and the derived-row
// refresh for exactly those sessions, all in one transaction. Committing the
// UPDATE without the refresh would leave the roll-ups and fact tables asserting
// the pre-repair attribution.
func (s *PgStore) applyCodexQuotaUpdate(ctx context.Context, query string, args []any) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return 0, err
	}
	updated, ids, err := updateLoginEmailRows(ctx, tx, query, args...)
	if err != nil {
		return 0, err
	}
	if err := refreshLoginEmailDerivedRows(ctx, tx, ids); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return updated, nil
}
