package store

import (
	"context"
)

// claudeRateAt resolves one row's $/weighted-token, in preference order, as a
// correlated subquery per candidate so COALESCE stops at the first hit.
//
//  1. the week the row falls in
//  2. the nearest earlier week, then the nearest later week -- a model billed
//     in June prices a March row with June's scale, which is the closest thing
//     to an observation that exists for a month with no OTEL at all
//  3. Anthropic's published price, for a model never billed here
//
// A model that matches none of these gets no cost rather than a guessed one:
// local and third-party models run through Claude Code (ollama, kimi, qwen) are
// not Anthropic-priced, and inventing a number for them is worse than the gap
// (#441).
const claudeRateAt = `
(SELECT b.scale FROM model_rate_buckets b
  WHERE b.model = sr.model AND b.bucket_start <= sr.ts::date
  ORDER BY b.bucket_start DESC LIMIT 1)`

const claudeRateBack = `
(SELECT b.scale FROM model_rate_buckets b
  WHERE b.model = sr.model AND b.bucket_start > sr.ts::date
  ORDER BY b.bucket_start ASC LIMIT 1)`

const claudeRateOfficial = `
(SELECT r.input_rate / 1e6 FROM claude_model_rates r
  WHERE sr.model LIKE r.model_prefix || '%' AND r.effective_from <= sr.ts::date
  ORDER BY length(r.model_prefix) DESC, r.effective_from DESC LIMIT 1)`

// claudeRateSource names which of the three answered, so the dashboard can tell
// spend measured against real billing from spend carried in from another week
// or read off a price list.
const claudeRateSource = `
CASE
  WHEN (SELECT b.bucket_start FROM model_rate_buckets b
         WHERE b.model = sr.model AND b.bucket_start <= sr.ts::date
         ORDER BY b.bucket_start DESC LIMIT 1) = date_trunc('week', sr.ts)::date THEN 'bucket'
  WHEN ` + claudeRateAt + ` IS NOT NULL THEN 'carry_forward'
  WHEN ` + claudeRateBack + ` IS NOT NULL THEN 'carry_back'
  ELSE 'official'
END`

// RefreshClaudeImputedCost recomputes imputed cost for offline Claude sessions
// (JSONL tokens x the OTEL-derived weekly scale, sessions with no OTEL) into
// claude_imputed_cost. Runs periodically off the hot path; see cmd/cctraced.
func (s *PgStore) RefreshClaudeImputedCost(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return err
	}
	// No rate test here. The rows it would exclude are the handful of non-Anthropic
	// models, so filtering for them costs more than carrying them: over-including a
	// session only means its rollup is refreshed with the same numbers it had.
	affected, err := querySessionIDs(ctx, tx, `
		SELECT session_id FROM claude_imputed_cost WHERE session_id <> ''
		UNION
		SELECT sr.session_id FROM session_records sr
		WHERE sr.agent = 'claude' AND sr.record_type = 'assistant' AND sr.session_id <> ''
		  AND NOT EXISTS (SELECT 1 FROM otel_events o WHERE o.session_id = sr.session_id)`)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM claude_imputed_cost`); err != nil {
		return err
	}
	const q = `
INSERT INTO claude_imputed_cost
	(srec_id, session_id, ts, user_id, profile_email, login_email, model, cost_usd,
	 input_tokens, output_tokens, cache_read_tokens, cache_create_tokens, agent, billing_provider,
	 rate_source)
SELECT sr.id, sr.session_id, sr.ts, COALESCE(sr.user_id,''), sr.profile_email, COALESCE(sr.login_email,''),
	COALESCE(sr.model,''),
	claude_weighted_tokens(sr.model, sr.input_tokens, sr.output_tokens,
	                       sr.cache_read_tokens, sr.cache_create_tokens) * rate.scale,
	COALESCE(sr.input_tokens,0), COALESCE(sr.output_tokens,0),
	COALESCE(sr.cache_read_tokens,0), COALESCE(sr.cache_create_tokens,0),
	sr.agent, sr.billing_provider,
	rate.source
FROM session_records sr
CROSS JOIN LATERAL (
	SELECT COALESCE(` + claudeRateAt + `, ` + claudeRateBack + `, ` + claudeRateOfficial + `) AS scale,
	       ` + claudeRateSource + ` AS source
) rate
WHERE sr.agent = 'claude' AND sr.record_type = 'assistant'
  AND rate.scale IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM deleted_sessions d WHERE d.session_id = sr.session_id)
  AND NOT EXISTS (SELECT 1 FROM otel_events o WHERE o.session_id = sr.session_id)`
	if _, err := tx.Exec(ctx, q); err != nil {
		return err
	}
	if err := refreshSessionOverviewRollups(ctx, tx, affected); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
