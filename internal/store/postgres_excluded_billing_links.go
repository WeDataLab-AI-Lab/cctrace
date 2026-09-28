package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// BillingAccountRef names one billing account.
type BillingAccountRef struct {
	BillingProvider string `json:"billing_provider"`
	AccountID       string `json:"account_id"`
}

// excludedBillingLinksDesiredSQL is every billing account an excluded address
// reaches. A quota reading is the only row that ever carries both, so it is the
// only evidence that the two are one person -- and only an observed reading
// counts. An inferred one was attributed from which account was logged in around
// it, which is a guess about the account, not a sighting of the address on it.
//
// Only an account whose every observed address is excluded is linked. A team or
// business plan bills several people under one account, and excluding one
// person's address must not hide everyone else on it; that account stays visible
// until the last address on it is excluded, or an admin excludes the account
// itself by its billing id.
//
// The address is compared case-folded, as everywhere else login_email is: ingest
// stores it exactly as the client sent it.
const excludedBillingLinksDesiredSQL = `
	SELECT a.billing_provider, a.account_id, a.login_email
	FROM (
		SELECT DISTINCT billing_provider, account_id, lower(login_email) AS login_email
		FROM quota_samples
		WHERE account_id <> '' AND login_email <> '' AND attribution = 'observed'
	) a
	WHERE a.login_email IN (SELECT lower(login_email) FROM excluded_accounts)
	  AND NOT EXISTS (
		SELECT 1 FROM quota_samples o
		WHERE o.billing_provider = a.billing_provider AND o.account_id = a.account_id
		  AND o.attribution = 'observed' AND o.login_email <> ''
		  AND lower(o.login_email) NOT IN (SELECT lower(login_email) FROM excluded_accounts)
	  )`

// syncExcludedBillingLinksTx brings excluded_billing_links up to date and returns
// the accounts whose visibility that changed.
//
// Links are added when the rule above first holds, and removed only when their
// address stops being excluded. They are deliberately not removed when a later
// reading shows another address on the account: that would bring back every row
// the link had hidden -- a personal account's whole history -- on the next tick,
// with nothing but a log line to say so. Taking a link away is an admin decision,
// made by un-excluding the address.
func syncExcludedBillingLinksTx(ctx context.Context, tx pgx.Tx) ([]BillingAccountRef, error) {
	var changed []BillingAccountRef
	collect := func(rows pgx.Rows, err error) error {
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ref BillingAccountRef
			if err := rows.Scan(&ref.BillingProvider, &ref.AccountID); err != nil {
				return err
			}
			changed = append(changed, ref)
		}
		return rows.Err()
	}
	if err := collect(tx.Query(ctx, `
		DELETE FROM excluded_billing_links
		WHERE login_email NOT IN (SELECT lower(login_email) FROM excluded_accounts)
		RETURNING billing_provider, account_id`)); err != nil {
		return nil, fmt.Errorf("drop links of un-excluded addresses: %w", err)
	}
	if err := collect(tx.Query(ctx, `
		INSERT INTO excluded_billing_links (billing_provider, account_id, login_email)`+
		excludedBillingLinksDesiredSQL+`
		ON CONFLICT DO NOTHING
		RETURNING billing_provider, account_id`)); err != nil {
		return nil, fmt.Errorf("add links: %w", err)
	}
	return changed, nil
}

// syncExcludedCodexMetricProfilesTx rewrites excluded_codex_metric_profiles:
// the profiles whose every Codex account in quota_samples is excluded, by billing
// id or through a link. quota_samples is the source because it is the one table
// that ties a profile to its accounts; measured on prod it names every account
// behind 16 of the 17 (profile, account) pairs in Codex session records, and the
// one it misses has no reading at all -- which leaves it visible, never hidden.
// Inferred readings count: they were credited from that same machine's own
// observation log, so the account is still one this profile used.
func syncExcludedCodexMetricProfilesTx(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `DELETE FROM excluded_codex_metric_profiles`); err != nil {
		return fmt.Errorf("clear excluded codex metric profiles: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO excluded_codex_metric_profiles (profile_email)`+
		excludedCodexMetricProfilesDesiredSQL); err != nil {
		return fmt.Errorf("add excluded codex metric profiles: %w", err)
	}
	return nil
}

// syncExcludedCodexMetricMinutesTx rewrites excluded_codex_metric_minutes.
//
// Codex metrics are 60-second deltas, so a row stamped ts covers roughly the
// minute before it; its window is [ts-2m, ts+1m], and the union of those windows
// over one minute M is [M-2m, M+2m) -- record minutes M-2 through M+1. The
// profile's Codex session records in that window decide: hidden when there is at
// least one and every one is on an excluded account (billing id or link). With
// none, the latest record before the window within 12 hours decides. Minutes
// with neither get no row and fall back to excluded_codex_metric_profiles.
//
// Records are read from session_records, not the visible view: the question is
// which account was in use, and hiding is what this answers.
//
// profile_email is stored lower-cased, as in excluded_codex_metric_profiles:
// quota_samples, session_records and otel_metrics each carry the address as its
// client sent it, and a case mismatch would silently drop the minute decision.
//
// Affected profiles are those with an excluded OpenAI account, the same
// provider rule excluded_codex_metric_profiles uses: an excluded Anthropic
// account says nothing about which Codex account a minute was on.
//
// Driven from the records of affected profiles rather than from the metrics:
// scanning the metric side for distinct minutes took 265s on prod (8.5M Codex
// rows). This took 3.0s there and wrote 41,581 minutes, 4,636 of them hidden.
func syncExcludedCodexMetricMinutesTx(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `DELETE FROM excluded_codex_metric_minutes`); err != nil {
		return fmt.Errorf("clear excluded codex metric minutes: %w", err)
	}
	if _, err := tx.Exec(ctx, `
WITH ex AS (
	SELECT billing_provider, account_id FROM excluded_billing_accounts
	UNION SELECT billing_provider, account_id FROM excluded_billing_links
), affected AS (
	SELECT DISTINCT lower(q.profile_email) AS profile_email FROM quota_samples q
	JOIN ex ON ex.billing_provider = q.billing_provider AND ex.account_id = q.account_id
	WHERE q.billing_provider = 'openai' AND q.profile_email <> ''
), rec AS (
	SELECT lower(sr.profile_email) AS profile_email, date_trunc('minute', sr.ts) AS minute,
		bool_and(EXISTS (SELECT 1 FROM ex
			WHERE ex.billing_provider = sr.billing_provider AND ex.account_id = sr.account_id)) AS all_excluded
	FROM session_records sr JOIN affected a ON a.profile_email = lower(sr.profile_email)
	WHERE sr.agent = 'codex' AND sr.account_id <> ''
	GROUP BY 1, 2
), span AS (
	SELECT profile_email, min(minute) - interval '1 minute' AS lo,
		max(minute) + interval '12 hours 2 minutes' AS hi
	FROM rec GROUP BY 1
), grid AS (
	SELECT s.profile_email, g AS minute, r.all_excluded
	FROM span s CROSS JOIN LATERAL generate_series(s.lo, s.hi, interval '1 minute') g
	LEFT JOIN rec r ON r.profile_email = s.profile_email AND r.minute = g
), framed AS (
	SELECT profile_email, minute,
		count(all_excluded) OVER w_win AS win_n,
		bool_and(all_excluded) OVER w_win AS win_all,
		max(CASE WHEN all_excluded IS NOT NULL THEN minute END) OVER w_back AS last_minute
	FROM grid
	WINDOW w_win AS (PARTITION BY profile_email ORDER BY minute ROWS BETWEEN 2 PRECEDING AND 1 FOLLOWING),
	       w_back AS (PARTITION BY profile_email ORDER BY minute ROWS BETWEEN UNBOUNDED PRECEDING AND 3 PRECEDING)
), decided AS (
	SELECT f.profile_email, f.minute,
		CASE WHEN f.win_n > 0 THEN f.win_all
		     WHEN f.last_minute >= f.minute - interval '12 hours' THEN r.all_excluded
		END AS hidden
	FROM framed f LEFT JOIN rec r ON r.profile_email = f.profile_email AND r.minute = f.last_minute
)
INSERT INTO excluded_codex_metric_minutes (profile_email, minute, hidden)
SELECT profile_email, minute, hidden FROM decided WHERE hidden IS NOT NULL`); err != nil {
		return fmt.Errorf("add excluded codex metric minutes: %w", err)
	}
	return nil
}

func (s *PgStore) refreshExcludedCodexMetricMinutes(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := syncExcludedCodexMetricMinutesTx(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const excludedCodexMetricProfilesDesiredSQL = `
	SELECT a.profile_email
	FROM (
		SELECT DISTINCT lower(profile_email) AS profile_email, billing_provider, account_id
		FROM quota_samples
		WHERE billing_provider = 'openai' AND account_id <> '' AND profile_email <> ''
	) a
	GROUP BY a.profile_email
	HAVING bool_and(
		EXISTS (SELECT 1 FROM excluded_billing_accounts b
			WHERE b.billing_provider = a.billing_provider AND b.account_id = a.account_id)
		OR EXISTS (SELECT 1 FROM excluded_billing_links l
			WHERE l.billing_provider = a.billing_provider AND l.account_id = a.account_id))`

// RefreshExcludedBillingLinks picks up links learned after an address was
// excluded, and reports whether anything changed.
//
// The common order is the wrong one: an address is excluded first, and the quota
// reading that names its billing account arrives later. Without this, the account
// would stay visible until someone next touched the exclusion list.
//
// A change rebuilds what is derived from visibility, so it runs only when there is
// one to make. Meant for the periodic loop.
func (s *PgStore) RefreshExcludedBillingLinks(ctx context.Context) (bool, error) {
	var pending int64
	if err := s.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM (`+excludedBillingLinksDesiredSQL+`
		          EXCEPT SELECT billing_provider, account_id, login_email FROM excluded_billing_links) added)
		     + (SELECT count(*) FROM excluded_billing_links
		          WHERE login_email NOT IN (SELECT lower(login_email) FROM excluded_accounts))
		     -- A new reading can also move a profile into or out of the fully
		     -- excluded set that account-less Codex metrics are judged by.
		     + (SELECT count(*) FROM ((`+excludedCodexMetricProfilesDesiredSQL+`
		          EXCEPT SELECT profile_email FROM excluded_codex_metric_profiles)
		        UNION ALL (SELECT profile_email FROM excluded_codex_metric_profiles
		          EXCEPT `+excludedCodexMetricProfilesDesiredSQL+`)) drift)`).Scan(&pending); err != nil {
		return false, err
	}
	if pending == 0 {
		// Session records keep arriving, and with them the accounts that decide
		// each minute of account-less Codex metrics. Nothing else is derived from
		// that table, so it is rewritten on its own rather than through a full
		// exclusion change.
		return false, s.refreshExcludedCodexMetricMinutes(ctx)
	}
	if err := s.applyExclusionChange(ctx, nil, nil, nil, false); err != nil {
		return false, err
	}
	return true, nil
}

// applyExclusionChange is how every change to what is excluded takes effect.
//
// mutate, when given, is the change itself (insert or delete an exclusion); it
// runs in the same transaction as the recompute, so readers see either the old
// visibility or the new one.
//
// This runs inside the request and has to answer well within the server's 30s
// WriteTimeout. Past it the response is never written and the connection is
// closed; the browser resends the POST, the resend finds the change already made,
// and the screen shows "already excluded" (409) or a refusal (403) for a change
// that succeeded -- observed at 73s and 93s on a production copy. So nothing here
// scans usage history. The usage aggregate is not rebuilt here, and not even
// where its rebuild would start is looked up: the change queues who it affects in
// the same transaction, so the request is never lost, and the server's worker
// finds the start and rebuilds (RunPendingUsageRollupRebuild).
//
// emails and accounts name what the caller changed directly; the links the
// recompute added or removed are added to them. Only the overview rows of
// sessions they reach are rebuilt -- unless full is set, for a caller that
// cannot say what changed.
func (s *PgStore) applyExclusionChange(ctx context.Context, mutate func(pgx.Tx) error, emails []string, accounts []BillingAccountRef, full bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if mutate != nil {
		if err := mutate(tx); err != nil {
			return err
		}
	}
	linked, err := s.recomputeExcludedSessionsTx(ctx, tx, emails, accounts, full)
	if err != nil {
		return err
	}
	if err := requestUsageRollupRebuild(ctx, tx, emails, append(accounts, linked...)); err != nil {
		return fmt.Errorf("queue usage rebuild: %w", err)
	}
	return tx.Commit(ctx)
}

// excludedBillingLinksByEmail returns the linked billing accounts keyed by the
// lowercased address, for the admin list.
func (s *PgStore) excludedBillingLinksByEmail(ctx context.Context) (map[string][]BillingAccountRef, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT login_email, billing_provider, account_id FROM excluded_billing_links
		ORDER BY login_email, billing_provider, account_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]BillingAccountRef{}
	for rows.Next() {
		var email string
		var ref BillingAccountRef
		if err := rows.Scan(&email, &ref.BillingProvider, &ref.AccountID); err != nil {
			return nil, err
		}
		out[email] = append(out[email], ref)
	}
	return out, rows.Err()
}
