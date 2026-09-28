package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ExcludedAccount is a login_email hidden from the dashboard, reported together
// with what it hides. The totals come from the unfiltered sources on purpose:
// without them an exclusion silently shrinks company-wide spend and nothing can
// explain the gap afterwards.
type ExcludedAccount struct {
	LoginEmail string     `json:"login_email"`
	Reason     string     `json:"reason"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	EventCount int64      `json:"event_count"`
	CostUSD    float64    `json:"cost_usd"`
	FirstTs    *time.Time `json:"first_ts,omitempty"`
	LastTs     *time.Time `json:"last_ts,omitempty"`
	// LinkedBillingAccounts are the billing accounts this address was seen with,
	// which the exclusion reaches as well (#715). Shown so whoever excluded the
	// address can see what is actually hidden.
	LinkedBillingAccounts []BillingAccountRef `json:"linked_billing_accounts"`
}

// normalizeLoginEmail stores one canonical form of the key. Ingest does not
// lowercase login_email, so the views compare case-insensitively; normalizing here
// keeps the table from holding the same account twice under different casing.
func normalizeLoginEmail(loginEmail string) string {
	return strings.ToLower(strings.TrimSpace(loginEmail))
}

// ListExcludedAccounts returns every excluded account with the volume it hides.
// The totals are aggregated in one pass — a per-account lateral would re-evaluate
// unified_events, whose codex arm carries a window function that nothing pushes
// predicates through, once per excluded account.
func (s *PgStore) ListExcludedAccounts(ctx context.Context) ([]ExcludedAccount, error) {
	rows, err := s.pool.Query(ctx, `
		WITH hidden AS (
			SELECT lower(login_email) AS login_email, count(*) AS event_count,
				SUM(cost_usd) AS cost_usd, min(ts) AS first_ts, max(ts) AS last_ts
			FROM unified_events
			WHERE lower(login_email) IN (SELECT lower(login_email) FROM excluded_accounts)
			GROUP BY lower(login_email)
		)
		SELECT x.login_email, x.reason, x.created_by, x.created_at,
			COALESCE(h.event_count, 0), COALESCE(h.cost_usd, 0), h.first_ts, h.last_ts
		FROM excluded_accounts x
		LEFT JOIN hidden h ON h.login_email = lower(x.login_email)
		ORDER BY x.created_at, x.login_email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ExcludedAccount, 0)
	for rows.Next() {
		var a ExcludedAccount
		if err := rows.Scan(&a.LoginEmail, &a.Reason, &a.CreatedBy, &a.CreatedAt,
			&a.EventCount, &a.CostUSD, &a.FirstTs, &a.LastTs); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	links, err := s.excludedBillingLinksByEmail(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].LinkedBillingAccounts = links[normalizeLoginEmail(out[i].LoginEmail)]
		if out[i].LinkedBillingAccounts == nil {
			out[i].LinkedBillingAccounts = []BillingAccountRef{}
		}
	}
	return out, nil
}

// ExcludeAccount hides a login_email from the dashboard and reports how many
// events that hides, so the caller can record the blast radius. The rows
// themselves are untouched: RemoveExcludedAccount brings them straight back.
func (s *PgStore) ExcludeAccount(ctx context.Context, loginEmail, reason, actor string) (int64, error) {
	loginEmail = normalizeLoginEmail(loginEmail)
	if loginEmail == "" || !strings.Contains(loginEmail, "@") {
		return 0, fmt.Errorf("a valid loginEmail is required")
	}

	// Blast radius for the audit trail, counted on otel_events: it is the axis the
	// exclusion actually keys on, and counting unified_events here would evaluate the
	// codex arm's window function over all history on every call.
	var affected int64
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM otel_events WHERE lower(login_email) = $1`, loginEmail).Scan(&affected); err != nil {
		return 0, err
	}
	// The account mutation, excluded session set, links and overview rebuild commit
	// as one visibility change. A rebuild failure must not leave the policy
	// half-applied.
	err := s.applyExclusionChange(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO excluded_accounts (login_email, reason, created_by) VALUES ($1, $2, $3)
			ON CONFLICT (login_email) DO UPDATE SET reason = EXCLUDED.reason, created_by = EXCLUDED.created_by`,
			loginEmail, reason, actor)
		return err
	}, []string{loginEmail}, nil, false)
	if err != nil {
		return 0, err
	}
	return affected, nil
}

// RemoveExcludedAccount makes an excluded account visible again.
func (s *PgStore) RemoveExcludedAccount(ctx context.Context, loginEmail string) error {
	loginEmail = normalizeLoginEmail(loginEmail)
	if loginEmail == "" {
		return fmt.Errorf("loginEmail required")
	}
	// Full recompute, not a delete keyed on this account's own sessions: a session
	// another excluded account also touched must stay hidden.
	return s.applyExclusionChange(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM excluded_accounts WHERE login_email = $1`, loginEmail)
		return err
	}, []string{loginEmail}, nil, false)
}
