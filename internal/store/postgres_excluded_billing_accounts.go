package store

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ExcludedBillingAccount is an account hidden by the id it is billed under,
// reported together with what it hides.
//
// It exists because excluded_accounts cannot reach every account. That table is
// keyed on login_email and CHECKs the value to look like an address, while Codex
// rows carry no login email at all -- 0 of 1,002,433 in session_records, and
// otel_events holds no Codex rows to fall back on. quota_samples does carry
// account_id on every row, because it is part of the primary key.
//
// The hidden totals come from the unfiltered table on purpose: an exclusion that
// shrinks a reported figure with nothing able to explain the gap afterwards is
// worse than one that shows its own blast radius.
type ExcludedBillingAccount struct {
	BillingProvider string     `json:"billing_provider"`
	AccountID       string     `json:"account_id"`
	Reason          string     `json:"reason"`
	CreatedBy       string     `json:"created_by"`
	CreatedAt       time.Time  `json:"created_at"`
	SampleCount     int64      `json:"sample_count"`
	EventCount      int64      `json:"event_count"`
	FirstTs         *time.Time `json:"first_ts,omitempty"`
	LastTs          *time.Time `json:"last_ts,omitempty"`

	// SelfRegistered is an entry the account's owner made from Settings (#716).
	SelfRegistered bool `json:"self_registered"`
}

// normalizeBillingKey trims the pair but does NOT lowercase the account id.
//
// login_email is lowercased because addresses are case-insensitive and ingest
// does not normalize them. An account id is an opaque token from the provider:
// two ids differing only in case are two ids, and folding them would exclude an
// account nobody asked to exclude.
func normalizeBillingKey(provider, accountID string) (string, string) {
	return strings.ToLower(strings.TrimSpace(provider)), strings.TrimSpace(accountID)
}

// ListExcludedBillingAccounts returns every excluded billing account with the
// number of quota readings and usage events it hides.
func (s *PgStore) ListExcludedBillingAccounts(ctx context.Context) ([]ExcludedBillingAccount, error) {
	rows, err := s.pool.Query(ctx, `
		WITH hidden AS (
			SELECT billing_provider, account_id, count(*) AS sample_count,
				min(sampled_at) AS first_ts, max(sampled_at) AS last_ts
			FROM quota_samples
			GROUP BY billing_provider, account_id
		),
		hidden_events AS (
			-- The same two sources ExcludeBillingAccount counts, so the number shown
			-- at exclusion time and the number listed afterwards agree by construction.
			-- unified_events' otel arm projects '' as account_id and cannot match, so
			-- reading the view would only add a scan of it.
			SELECT billing_provider, account_id, count(*) AS event_count FROM (
				SELECT billing_provider, account_id FROM codex_imputed_cost
				UNION ALL
				SELECT billing_provider, account_id FROM session_records
				WHERE agent IN ('gjc','omo') AND record_type = 'assistant'
			) src
			WHERE EXISTS (
				SELECT 1 FROM excluded_billing_accounts b
				WHERE b.billing_provider = src.billing_provider AND b.account_id = src.account_id
			)
			GROUP BY billing_provider, account_id
		)
		SELECT b.billing_provider, b.account_id, b.reason, b.created_by, b.created_at, b.registered_by_self,
			COALESCE(h.sample_count, 0), COALESCE(he.event_count, 0), h.first_ts, h.last_ts
		FROM excluded_billing_accounts b
		LEFT JOIN hidden h
			ON h.billing_provider = b.billing_provider AND h.account_id = b.account_id
		LEFT JOIN hidden_events he
			ON he.billing_provider = b.billing_provider AND he.account_id = b.account_id
		ORDER BY b.created_at, b.billing_provider, b.account_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ExcludedBillingAccount, 0)
	for rows.Next() {
		var a ExcludedBillingAccount
		if err := rows.Scan(&a.BillingProvider, &a.AccountID, &a.Reason, &a.CreatedBy,
			&a.CreatedAt, &a.SelfRegistered, &a.SampleCount, &a.EventCount, &a.FirstTs, &a.LastTs); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ExcludeBillingAccount hides an account by its billing id and reports how many
// quota readings and usage events that hides, so the caller can record the
// blast radius. The rows themselves are untouched: RemoveExcludedBillingAccount
// brings them straight back.
func (s *PgStore) ExcludeBillingAccount(ctx context.Context, provider, accountID, reason, actor string) (samples, events int64, err error) {
	provider, accountID = normalizeBillingKey(provider, accountID)

	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM quota_samples WHERE billing_provider = $1 AND account_id = $2`,
		provider, accountID).Scan(&samples); err != nil {
		return 0, 0, err
	}
	// Counted on the sources that carry a billing id rather than on unified_events:
	// the otel arm projects '' and cannot match, so scanning it would only add cost.
	if err := s.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM codex_imputed_cost WHERE billing_provider = $1 AND account_id = $2)
		     + (SELECT count(*) FROM session_records WHERE agent IN ('gjc','omo') AND record_type = 'assistant'
		          AND billing_provider = $1 AND account_id = $2)`,
		provider, accountID).Scan(&events); err != nil {
		return 0, 0, err
	}
	// The same recompute an address exclusion runs. Without it only the views
	// changed, and the session list and usage charts -- which read their own
	// rollups -- kept showing the account (#715).
	ref := BillingAccountRef{BillingProvider: provider, AccountID: accountID}
	if err := s.applyExclusionChange(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO excluded_billing_accounts (billing_provider, account_id, reason, created_by)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (billing_provider, account_id) DO UPDATE
				SET reason = EXCLUDED.reason, created_by = EXCLUDED.created_by,
					-- An admin re-registering an owner's entry makes it the admin's,
					-- so the owner can no longer take it back.
					registered_by_self = false`,
			provider, accountID, reason, actor)
		return err
	}, nil, []BillingAccountRef{ref}, false); err != nil {
		return 0, 0, err
	}
	return samples, events, nil
}

// RemoveExcludedBillingAccount makes the account visible again.
func (s *PgStore) RemoveExcludedBillingAccount(ctx context.Context, provider, accountID string) error {
	provider, accountID = normalizeBillingKey(provider, accountID)
	ref := BillingAccountRef{BillingProvider: provider, AccountID: accountID}
	return s.applyExclusionChange(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`DELETE FROM excluded_billing_accounts WHERE billing_provider = $1 AND account_id = $2`,
			provider, accountID)
		return err
	}, nil, []BillingAccountRef{ref}, false)
}
