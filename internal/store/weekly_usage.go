package store

// Weekly AI report runs spend tokens too -- the server's runtime account or API
// key runs them, not any person's agent. They reach the dashboard as their own
// agent, 'weekly', so a total includes them while nobody's personal usage does.

const agentWeekly = "weekly"

// weeklyUsageArm is the unified_events arm for ai_report_runs. Only runs whose
// tokens were reported count; that covers failed and canceled runs, which spent
// tokens just the same.
//
// Every runtime reports tokens the codex way -- cached input is part of input --
// so input is stored net of it and cache reads apart, whatever the runtime.
// Cost follows the provider the runtime bills: claude-api at the Claude price
// table through claude_rate_cost_usd, the Codex and OpenAI API runtimes at
// codex_model_rates through codex_rate_cost_usd. Computed at read time rather
// than materialised: a few runs a week do not need a table.
//
// ts is started_at, never finished_at. A run reports tokens while still running
// and boot recovery stamps finished_at on it later; a ts that moved with it would
// leave the rollup copy made at the old ts behind once it is outside the refresh
// window, and count the run again at the new one.
//
// No session, prompt or person. An empty session_id keeps the runs out of
// session counts and lists, which already skip it; user_name says what the row is.
const weeklyUsageArm = `
	SELECT
		r.started_at AS ts, 'weekly_usage' AS event_name,
		'' AS session_id, '' AS prompt_id,
		'' AS user_id, '' AS profile_email, '' AS login_email,
		'weekly' AS user_name, '' AS user_team, '' AS org_id,
		r.model,
		CASE r.runtime
			WHEN 'claude-api' THEN claude_rate_cost_usd(r.model, r.started_at::date,
				r.input_tokens, r.cached_input_tokens, r.output_tokens)
			WHEN 'codex-app-server' THEN codex_rate_cost_usd(r.model, r.started_at::date,
				r.input_tokens, r.cached_input_tokens, r.output_tokens)
			WHEN 'openai-api' THEN codex_rate_cost_usd(r.model, r.started_at::date,
				r.input_tokens, r.cached_input_tokens, r.output_tokens)
			-- No rate table prices the models these runtimes reach. NULL says
			-- the cost is unknown; zero would claim the run was free, and the
			-- rollups COALESCE it to zero anyway when they sum.
			ELSE NULL
		END AS cost_usd,
		-- INTEGER like every other arm; see codex_imputed_cost for why the type matters.
		GREATEST(COALESCE(r.input_tokens,0) - COALESCE(r.cached_input_tokens,0), 0)::int AS input_tokens,
		COALESCE(r.output_tokens,0)::int AS output_tokens,
		COALESCE(r.cached_input_tokens,0)::int AS cache_read_tokens,
		0 AS cache_create_tokens,
		NULL::int AS duration_ms,
		'' AS tool_name, '' AS tool_decision, NULL::boolean AS tool_success,
		'' AS speed, '' AS service_version, '{}'::jsonb AS attrs,
		'weekly' AS agent,
		CASE r.runtime
			WHEN 'claude-api' THEN 'anthropic'
			WHEN 'nvidia-api' THEN 'nvidia'
			WHEN 'litellm-api' THEN 'litellm'
			ELSE 'openai'
		END AS billing_provider,
		'' AS account_id,
		'w:' || r.id AS tiebreak
	FROM ai_report_runs r
	WHERE r.tokens_reported`

// codexRateCostUSDFunction prices codex-semantics tokens (cached input inside input)
// at the rate in force on a day: the arithmetic and the longest-prefix, date-bounded
// match of codexImputedSelect, which TestPgStore_WeeklyUsage_UnifiedEventsArm pins by
// comparing the two on the same tokens.
//
// A function rather than a LATERAL join in the view for two reasons. A string-bodied
// SQL function records no dependency on the tables it reads, so the view does not pin
// codex_model_rates' columns -- a join would make every future change to that table
// fail with 2BP01 outside the migration pass that drops the views. And the latest
// activity probe reuses weeklyUsageArm as a subquery, where a lateral has no business.
const codexRateCostUSDFunction = `CREATE OR REPLACE FUNCTION codex_rate_cost_usd(
	model TEXT, day DATE, input BIGINT, cached BIGINT, output BIGINT
) RETURNS DOUBLE PRECISION
LANGUAGE SQL STABLE PARALLEL SAFE AS $$
	SELECT ((GREATEST(COALESCE(input,0) - COALESCE(cached,0), 0) * COALESCE(rate.input_rate, 0)
		+ COALESCE(output,0) * COALESCE(rate.output_rate, 0)
		+ COALESCE(cached,0) * COALESCE(rate.cache_read_rate, 0)) / 1000000.0)::double precision
	FROM (SELECT 1) one
	LEFT JOIN LATERAL (
		SELECT cmr.input_rate, cmr.output_rate, cmr.cache_read_rate
		FROM codex_model_rates cmr
		WHERE LOWER(COALESCE(model,'')) LIKE cmr.model_prefix || '%'
			AND cmr.effective_from <= day
		ORDER BY LENGTH(cmr.model_prefix) DESC, cmr.effective_from DESC
		LIMIT 1
	) rate ON TRUE
$$`

// claudeRateCostUSDFunction prices a Claude API run: the tokens weighted by
// claude_weighted_tokens, times the dated, longest-prefix base input price in
// claude_model_rates -- the published table the offline Claude imputation falls
// back to. The measured model_rate_buckets scale is not used: it absorbs Claude
// Code's cache mix, and a report run is billed at list price. A run reports cache
// reads but not cache writes apart, so writes are priced as plain input, a small
// under-count. A model the table does not know gets no cost rather than a guess.
//
// plpgsql, not SQL: claude_model_rates and claude_weighted_tokens are created in
// dependentViews, after unified_events, and a SQL function body is checked
// against its tables when created, which fails on a fresh database. plpgsql
// resolves them when called.
const claudeRateCostUSDFunction = `CREATE OR REPLACE FUNCTION claude_rate_cost_usd(
	model TEXT, day DATE, input BIGINT, cached BIGINT, output BIGINT
) RETURNS DOUBLE PRECISION
LANGUAGE plpgsql STABLE PARALLEL SAFE AS $$
BEGIN
	RETURN (
		SELECT claude_weighted_tokens(model, GREATEST(COALESCE(input,0) - COALESCE(cached,0), 0),
			COALESCE(output,0), COALESCE(cached,0), 0) * r.input_rate / 1000000.0
		FROM claude_model_rates r
		WHERE COALESCE(model,'') LIKE r.model_prefix || '%' AND r.effective_from <= day
		ORDER BY length(r.model_prefix) DESC, r.effective_from DESC
		LIMIT 1);
END
$$`

// weeklyUserKeyExpr is the user key for per-user groupings. Weekly rows carry no
// user_id, and grouping them on it would fold them into the blank-user bucket
// alongside unattributed personal usage; this gives them one line of their own.
// Applies to both visible_events and usage_hourly_rollups, which share the columns.
const weeklyUserKeyExpr = `CASE WHEN agent = 'weekly' THEN 'weekly' ELSE COALESCE(user_id, '') END`
