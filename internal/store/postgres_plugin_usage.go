package store

import (
	"context"
	"time"

	"cctrace/internal/commandclass"
)

// pluginUsageRawQuery is the exact pre-fact response-window query. PluginUsage keeps
// serving it until the fact backfill and its completion marker commit atomically.
const pluginUsageRawQuery = `
WITH login_user_ids AS (
  -- Map login_email → user_ids via otel_events. session_records.login_email is
  -- backfilled from otel and only for single-login sessions, so the event side
  -- stays the authoritative mapping.
  SELECT DISTINCT user_id
  FROM visible_events
  WHERE $4 != '' AND login_email = $4 AND user_id != ''
),
-- MATERIALIZED is required: it forces next_user_ts to be computed once per
-- command row here (hundreds of rows) instead of letting the planner inline
-- this CTE and re-evaluate next_user_ts as a correlated subplan inside the
-- token-attribution join filter below (440k+ evaluations → ~34s). Keep it.
cmd_records AS MATERIALIZED (
  SELECT
    sr.session_id,
    sr.ts,
    sr.record_type AS command_record_type,
    sr.command_name,
    sr.profile_email,
    sr.user_id,
    COALESCE(NULLIF(sr.agent, ''), 'claude') AS agent,
    COALESCE(sr.project_hash, '') AS project_hash,
    COALESCE(p.project_name, '') AS project_name,
    COALESCE(NULLIF(sr.repository_id, ''), NULLIF(p.repository_id, ''), '') AS repository_id,
    COALESCE(NULLIF(sr.repository_name, ''), NULLIF(p.repository_name, ''), '') AS repository_name,
    COALESCE(NULLIF(sr.repo_subpath, ''), NULLIF(p.repo_subpath, ''), '') AS repo_subpath,
    (
      NULLIF(sr.commit_sha, '') IS NOT NULL
      OR NULLIF(sr.branch, '') IS NOT NULL
      OR NULLIF(sr.repo_subpath, '') IS NOT NULL
      OR (
        COALESCE(NULLIF(sr.repository_id, ''), NULLIF(p.repository_id, ''), '') != ''
        AND COALESCE(NULLIF(sr.repository_id, ''), NULLIF(p.repository_id, ''), '') NOT LIKE 'local:%'
      )
    ) AS has_git,
    (SELECT MIN(u.ts) FROM session_records u
     WHERE u.session_id = sr.session_id
       AND u.record_type = 'user'
       AND (
         COALESCE(NULLIF(sr.agent, ''), 'claude') = 'codex'
         OR jsonb_typeof(u.raw->'message'->'content') = 'string'
         OR (jsonb_typeof(u.raw->'message'->'content') = 'array' AND EXISTS (
               SELECT 1 FROM jsonb_array_elements(u.raw->'message'->'content') e
               WHERE e->>'type' IN ('text', 'input_text')
             ))
       )
       AND u.ts > sr.ts) AS next_user_ts
  FROM visible_session_records sr
  LEFT JOIN projects p ON p.agent = COALESCE(NULLIF(sr.agent, ''), 'claude') AND p.project_hash = sr.project_hash
  WHERE sr.command_name != ''
    -- Count only what a plugin actually provided. The client decides this while it
    -- still has the directories that answer (#57); '' means an older client that
    -- never looked, and those rows keep their previous behaviour rather than
    -- vanishing from history the day this ships.
    AND (
      sr.command_source = 'plugin'
      OR (
        -- '' means a client that predates the classification. Every row in the
        -- field is still that shape, so this arm is not a legacy tail -- it is all
        -- of the data, and letting it through unfiltered counted /clear and /model
        -- as plugin usage. That is the 5.5x over-count #57 set out to remove.
        --
        -- Name alone cannot separate a project command from a plugin one, which is
        -- why the client decides. It CAN separate the names the binary ships, so
        -- the server subtracts exactly those and leaves the rest alone. A project
        -- command deliberately named 'clear' is lost here; that is the price of
        -- having no classification, and it stops the day a v0.7.22+ client syncs.
        --
        -- Keyed by agent, not name: Codex ships /rename and Claude does not, so a
        -- flat list would let one agent's builtin excuse the other's plugin.
        sr.command_source = ''
        AND NOT (COALESCE(NULLIF(sr.agent, ''), 'claude') || '|' || sr.command_name = ANY($7))
      )
    )
    AND sr.ts >= $1 AND sr.ts < $2
    AND ($3 = '' OR sr.profile_email = $3)
    AND ($4 = '' OR sr.user_id IN (SELECT user_id FROM login_user_ids))
    AND ($5 = '' OR sr.user_id = $5)
    AND ($6 = '' OR COALESCE(NULLIF(sr.agent, ''), 'claude') = $6)
),
attributed AS (
  SELECT
    c.command_name,
    c.agent,
    c.profile_email,
    c.user_id,
    c.project_hash,
    c.project_name,
    c.repository_id,
    c.repository_name,
    c.repo_subpath,
    c.has_git,
    COUNT(DISTINCT c.ts) AS invocation_count,
    COALESCE(SUM(
      CASE
        WHEN c.agent = 'codex' THEN GREATEST(COALESCE(r.input_tokens,0) - COALESCE(r.cache_read_tokens,0), 0)
        ELSE COALESCE(r.input_tokens,0)
      END + COALESCE(r.output_tokens,0)
    ), 0) AS total_tokens,
    COALESCE(SUM(CASE
      WHEN c.agent = 'codex' THEN GREATEST(COALESCE(r.input_tokens,0) - COALESCE(r.cache_read_tokens,0), 0)
      ELSE COALESCE(r.input_tokens,0)
    END), 0) AS input_tokens,
    COALESCE(SUM(COALESCE(r.output_tokens,0)), 0) AS output_tokens
  FROM cmd_records c
  LEFT JOIN session_records r ON
    r.session_id = c.session_id AND
    r.ts >= $1 AND
    COALESCE(NULLIF(r.agent, ''), 'claude') = c.agent AND
    ((c.agent = 'codex' AND r.record_type = 'usage') OR (c.agent != 'codex' AND r.record_type = 'assistant')) AND
    ((c.command_record_type = 'assistant' AND r.ts >= c.ts) OR (c.command_record_type != 'assistant' AND r.ts > c.ts)) AND
    (c.next_user_ts IS NULL OR r.ts < c.next_user_ts)
  GROUP BY c.command_name, c.agent, c.profile_email, c.user_id,
    c.project_hash, c.project_name, c.repository_id, c.repository_name, c.repo_subpath, c.has_git
)
SELECT command_name, agent, profile_email, user_id,
  project_hash, project_name, repository_id, repository_name, repo_subpath, has_git,
  invocation_count, total_tokens, input_tokens, output_tokens
FROM attributed
ORDER BY invocation_count DESC
`

// pluginUsageQuery deliberately contains no session_records reference. Once the
// completed marker is visible, every range including All and 30d aggregates facts.
var pluginUsageQuery = `
WITH login_user_ids AS (
  -- Keep the existing authoritative login_email -> user_id mapping. A session record's
  -- login_email is asynchronously backfilled and is not reliable for this filter.
  SELECT DISTINCT user_id
  FROM visible_events
  WHERE $4 != '' AND login_email = $4 AND user_id != ''
),
attributed AS (
  SELECT
    f.command_name,
    f.agent,
    f.profile_email,
    f.user_id,
    f.project_hash,
    COALESCE(p.project_name, '') AS project_name,
    COALESCE(NULLIF(f.repository_id, ''), NULLIF(p.repository_id, ''), '') AS repository_id,
    COALESCE(NULLIF(f.repository_name, ''), NULLIF(p.repository_name, ''), '') AS repository_name,
    COALESCE(NULLIF(f.repo_subpath, ''), NULLIF(p.repo_subpath, ''), '') AS repo_subpath,
    (
      f.commit_sha <> '' OR f.branch <> '' OR f.repo_subpath <> '' OR (
        COALESCE(NULLIF(f.repository_id, ''), NULLIF(p.repository_id, ''), '') <> ''
        AND COALESCE(NULLIF(f.repository_id, ''), NULLIF(p.repository_id, ''), '') NOT LIKE 'local:%'
      )
    ) AS has_git,
    COUNT(DISTINCT f.command_ts) AS invocation_count,
    COALESCE(SUM(f.input_tokens + f.output_tokens), 0) AS total_tokens,
    COALESCE(SUM(f.input_tokens), 0) AS input_tokens,
    COALESCE(SUM(f.output_tokens), 0) AS output_tokens
  FROM plugin_invocation_facts f
  LEFT JOIN projects p ON p.agent = f.agent AND p.project_hash = f.project_hash
  WHERE f.command_ts >= $1 AND f.command_ts < $2
    AND ($3 = '' OR f.profile_email = $3)
    AND ($4 = '' OR f.user_id IN (SELECT user_id FROM login_user_ids))
    AND ($5 = '' OR f.user_id = $5)
    AND ($6 = '' OR f.agent = $6)
    -- Match visible_session_records at read time so account exclusions remain
    -- reversible policy, not destructive fact maintenance. The billing keys
    -- matter here (#715): Codex rows never carry an address, and excluded_sessions
    -- is built from otel_events, where Codex has no rows (#717).
    AND ` + excludedAccountPredicateSQL("f") + `
    -- The same rule the raw query applies, against the column the fact row carries.
    -- Kept at read time, next to the account and session exclusions, because the
    -- builtin list moves with each release and stored facts must stay re-judgeable.
    AND (
      f.command_source = 'plugin'
      OR (
        f.command_source = ''
        AND NOT (COALESCE(NULLIF(f.agent, ''), 'claude') || '|' || f.command_name = ANY($7))
      )
    )
    AND NOT EXISTS (
      SELECT 1 FROM excluded_sessions es
      WHERE es.session_id = f.session_id AND f.session_id <> ''
    )
  GROUP BY f.command_name, f.agent, f.profile_email, f.user_id,
    f.project_hash, p.project_name,
    COALESCE(NULLIF(f.repository_id, ''), NULLIF(p.repository_id, ''), ''),
    COALESCE(NULLIF(f.repository_name, ''), NULLIF(p.repository_name, ''), ''),
    COALESCE(NULLIF(f.repo_subpath, ''), NULLIF(p.repo_subpath, ''), ''),
    (
      f.commit_sha <> '' OR f.branch <> '' OR f.repo_subpath <> '' OR (
        COALESCE(NULLIF(f.repository_id, ''), NULLIF(p.repository_id, ''), '') <> ''
        AND COALESCE(NULLIF(f.repository_id, ''), NULLIF(p.repository_id, ''), '') NOT LIKE 'local:%'
      )
    )
)
SELECT command_name, agent, profile_email, user_id,
  project_hash, project_name, repository_id, repository_name, repo_subpath, has_git,
  invocation_count, total_tokens, input_tokens, output_tokens
FROM attributed
ORDER BY invocation_count DESC
`

// PluginUsage returns aggregated plugin (slash command) usage statistics from the
// write-maintained per-invocation facts.
func (s *PgStore) PluginUsage(ctx context.Context, since, until time.Time, profileEmail string, loginEmail string, userID string, agent string) ([]*PluginUsageSummary, error) {
	ready, err := s.pluginInvocationFactsReady(ctx)
	if err != nil {
		return nil, err
	}
	// $7 goes to BOTH queries. The builtin list is what separates a plugin command
	// from one the binary ships, and the answer must not depend on which path served
	// it -- the fact path used to skip this and counted /clear, /model and /rename as
	// plugin usage the moment the backfill marker landed.
	query := pluginUsageQuery
	if !ready {
		query = pluginUsageRawQuery
	}
	args := []any{since, until, profileEmail, loginEmail, userID, agent, commandclass.AgentBuiltinKeys()}
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*PluginUsageSummary
	for rows.Next() {
		p := &PluginUsageSummary{}
		if err := rows.Scan(
			&p.CommandName,
			&p.Agent,
			&p.ProfileEmail,
			&p.UserID,
			&p.ProjectHash,
			&p.ProjectName,
			&p.RepositoryID,
			&p.RepositoryName,
			&p.RepoSubpath,
			&p.HasGit,
			&p.InvocationCount,
			&p.TotalTokens,
			&p.InputTokens,
			&p.OutputTokens,
		); err != nil {
			return nil, err
		}
		results = append(results, p)
	}
	return results, rows.Err()
}
