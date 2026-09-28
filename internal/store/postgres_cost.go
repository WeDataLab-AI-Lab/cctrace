package store

import (
	"context"
	"strings"
	"time"
)

func (s *PgStore) ListLoginAccounts(ctx context.Context, userID string) ([]string, error) {
	userPredicate := ""
	var args []interface{}
	if userID != "" {
		userPredicate = " AND user_id = $1"
		args = append(args, userID)
	}

	// DISTINCT belongs inside every physical arm. Reading visible_events forces
	// PostgreSQL to expand every full-width unified_events row before discarding all
	// but login_email; reducing each source first keeps the union narrow and bounded
	// by the number of accounts in that source.
	//
	// The two session_records arms are the only place an inferred login_email
	// (login_email_source = 'inferred', #346) reaches cost and stats: everything
	// else here reads otel_events or an imputed-cost table, and none of those has
	// or needs provenance -- a row there IS the observation. Neither arm consults
	// login_email_source, so a gjc/omo account that only ever appeared through
	// inference can list here and be grouped on downstream. That is intended --
	// hiding inferred usage from cost would understate real spend, and the value is
	// the system's best answer -- but it means these totals are not purely observed.
	// SessionOverview.LoginEmailInferred is where that distinction is surfaced;
	// widening it to the cost and stats screens is a separate change.
	arms := []string{
		"SELECT DISTINCT login_email FROM otel_events WHERE true" + userPredicate,
		"SELECT DISTINCT login_email FROM codex_imputed_cost WHERE true" + userPredicate,
		"SELECT DISTINCT login_email FROM claude_imputed_cost WHERE true" + userPredicate,
		"SELECT DISTINCT login_email FROM session_records WHERE agent = 'gjc' AND record_type = 'assistant'" + userPredicate,
		"SELECT DISTINCT login_email FROM session_records WHERE agent = 'omo' AND record_type = 'assistant'" + userPredicate,
	}
	q := `SELECT login_email FROM (` + strings.Join(arms, " UNION ") + `) accounts
		WHERE login_email != ''
		  AND NOT EXISTS (
			SELECT 1 FROM excluded_accounts x
			WHERE lower(x.login_email) = lower(accounts.login_email)
		  )
		ORDER BY login_email`

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, err
		}
		result = append(result, email)
	}
	return result, rows.Err()
}

func (s *PgStore) CostByUser(ctx context.Context, since, until time.Time, profileEmail string, loginEmail string, userID string) ([]*CostSummary, error) {
	q := `SELECT profile_email, ` + weeklyUserKeyExpr + ` as user_key, COALESCE(user_team,'') as user_team, model,
		COALESCE(agent, 'claude') as agent,
		COALESCE(billing_provider, 'anthropic') as billing_provider,
		COALESCE(SUM(cost_usd),0),
		COALESCE(SUM(input_tokens),0),
		COALESCE(SUM(output_tokens),0),
		COUNT(*),
		array_remove(array_agg(DISTINCT login_email), '') as login_emails
	FROM visible_events
	WHERE ts >= $1 AND ts < $2 AND model != ''
		AND ($3 = '' OR profile_email = $3) AND ($4 = '' OR login_email = $4) AND ($5 = '' OR user_id = $5)
	GROUP BY profile_email, user_key, user_team, model, agent, billing_provider
	ORDER BY SUM(cost_usd) DESC`
	return s.scanCostSummariesWithUserID(ctx, q, since, until, profileEmail, loginEmail, userID)
}

func (s *PgStore) CostByModel(ctx context.Context, since, until time.Time, profileEmail string, loginEmail string, userID string) ([]*ModelStat, error) {
	q := `SELECT model, COALESCE(profile_email,'') as profile_email, COALESCE(user_team,'') as user_team,
		COALESCE(agent,'claude') as agent,
		COALESCE(billing_provider,'anthropic') as billing_provider,
		COALESCE(SUM(cost_usd),0), COALESCE(SUM(input_tokens),0),
		COALESCE(SUM(output_tokens),0), COUNT(*)
	FROM visible_events
	WHERE ts >= $1 AND ts < $2 AND model != '' AND ($3 = '' OR profile_email = $3) AND ($4 = '' OR login_email = $4) AND ($5 = '' OR user_id = $5)
	GROUP BY model, profile_email, user_team, agent, billing_provider
	ORDER BY SUM(cost_usd) DESC`

	rows, err := s.pool.Query(ctx, q, since, until, profileEmail, loginEmail, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*ModelStat, 0)
	for rows.Next() {
		m := &ModelStat{}
		if err := rows.Scan(&m.Model, &m.ProfileEmail, &m.UserTeam, &m.Agent, &m.BillingProvider,
			&m.TotalCost, &m.InputTokens, &m.OutputTokens, &m.RequestCount); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

func (s *PgStore) CostByTeam(ctx context.Context, since, until time.Time, profileEmail string, userID string) ([]*CostSummary, error) {
	q := `SELECT '' AS profile_email, user_team, model,
		COALESCE(SUM(cost_usd), 0), COALESCE(SUM(input_tokens), 0),
		COALESCE(SUM(output_tokens), 0), COUNT(*)
		FROM visible_events
		WHERE ts >= $1 AND ts < $2 AND model != ''
		AND ($3 = '' OR user_team IN (SELECT DISTINCT user_team FROM visible_events WHERE profile_email = $3 AND user_team != ''))
		AND ($4 = '' OR user_team IN (SELECT DISTINCT user_team FROM visible_events WHERE user_id = $4 AND user_team != ''))
		GROUP BY user_team, model
		ORDER BY SUM(cost_usd) DESC`
	rows, err := s.pool.Query(ctx, q, since, until, profileEmail, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]*CostSummary, 0)
	for rows.Next() {
		c := &CostSummary{}
		if err := rows.Scan(&c.ProfileEmail, &c.UserTeam, &c.Model,
			&c.TotalCost, &c.TotalInput, &c.TotalOutput, &c.RequestCount); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

func (s *PgStore) scanCostSummariesWithUserID(ctx context.Context, q string, since, until time.Time, profileEmail string, loginEmail string, userID string) ([]*CostSummary, error) {
	rows, err := s.pool.Query(ctx, q, since, until, profileEmail, loginEmail, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*CostSummary, 0)
	for rows.Next() {
		c := &CostSummary{}
		if err := rows.Scan(&c.ProfileEmail, &c.UserID, &c.UserTeam, &c.Model, &c.Agent, &c.BillingProvider,
			&c.TotalCost, &c.TotalInput, &c.TotalOutput, &c.RequestCount, &c.LoginEmails); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}
