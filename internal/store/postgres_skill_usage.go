package store

import (
	"context"
	"time"
)

// SkillUsage returns aggregated skill invocation metrics.
//
// Codex reports skills as a metric at injection time. Claude reports only a command
// name, so its rows join here only once the client has classified one as a skill.
func (s *PgStore) SkillUsage(ctx context.Context, since, until time.Time, profileEmail string, loginEmail string, userID string, agent string) ([]*SkillUsageSummary, error) {
	const q = `
WITH login_user_ids AS (
  -- Map login_email → user_ids via otel_events. session_records.login_email is
  -- backfilled from otel and only for single-login sessions, so the event side
  -- stays the authoritative mapping.
  SELECT DISTINCT user_id
  FROM visible_events
  WHERE $4 != '' AND login_email = $4 AND user_id != ''
),
session_ctx AS (
  SELECT
    sr.session_id,
    COALESCE(NULLIF(MAX(sr.project_hash), ''), '') AS project_hash,
    COALESCE(NULLIF(MAX(p.project_name), ''), '') AS project_name,
    COALESCE(NULLIF(MAX(sr.repository_id), ''), NULLIF(MAX(p.repository_id), ''), '') AS repository_id,
    COALESCE(NULLIF(MAX(sr.repository_name), ''), NULLIF(MAX(p.repository_name), ''), '') AS repository_name,
    COALESCE(NULLIF(MAX(sr.repo_subpath), ''), NULLIF(MAX(p.repo_subpath), ''), '') AS repo_subpath,
    BOOL_OR(
      NULLIF(sr.commit_sha, '') IS NOT NULL
      OR NULLIF(sr.branch, '') IS NOT NULL
      OR NULLIF(sr.repo_subpath, '') IS NOT NULL
      OR (
        COALESCE(NULLIF(sr.repository_id, ''), NULLIF(p.repository_id, ''), '') != ''
        AND COALESCE(NULLIF(sr.repository_id, ''), NULLIF(p.repository_id, ''), '') NOT LIKE 'local:%'
      )
    ) AS has_git
  FROM session_records sr
  LEFT JOIN projects p ON p.agent = COALESCE(NULLIF(sr.agent, ''), 'claude') AND p.project_hash = sr.project_hash
  WHERE sr.session_id != ''
    AND sr.ts >= $1 AND sr.ts < $2
  GROUP BY sr.session_id
),
metric_rows AS (
  SELECT
    COALESCE(NULLIF(dimensions->>'skill', ''), '') AS skill_name,
    COALESCE(NULLIF(dimensions->>'invoke_type', ''), 'explicit') AS invoke_type,
    COALESCE(NULLIF(m.agent, ''), 'codex') AS agent,
    COALESCE(m.profile_email, '') AS profile_email,
    COALESCE(m.login_email, '') AS login_email,
    COALESCE(m.user_id, '') AS user_id,
    COALESCE(sc.project_hash, '') AS project_hash,
    COALESCE(sc.project_name, '') AS project_name,
    COALESCE(sc.repository_id, '') AS repository_id,
    COALESCE(sc.repository_name, '') AS repository_name,
    COALESCE(sc.repo_subpath, '') AS repo_subpath,
    COALESCE(sc.has_git, false) AS has_git,
    COALESCE(m.dimensions->>'status', '') AS status,
    COALESCE(m.value_double, m.value_int::double precision, 0) AS metric_value
  FROM visible_metrics m
  LEFT JOIN session_ctx sc ON sc.session_id = m.session_id
  WHERE m.metric_name = 'codex.skill.injected'
    AND m.ts >= $1 AND m.ts < $2
    AND ($3 = '' OR m.profile_email = $3)
    AND ($4 = '' OR m.login_email = $4)
    AND ($5 = '' OR m.user_id = $5)
    AND ($6 = '' OR COALESCE(NULLIF(m.agent, ''), 'codex') = $6)
),
jsonl_rows AS (
  -- One row per record, not per stored copy. The same record turns up under more
  -- than one session_id when a session is forked or re-recorded -- 33,322 uuids in
  -- our own data -- and each copy would otherwise add another invocation. Rows that
  -- carry no uuid keep their own identity via ctid, so nothing collapses that this
  -- cannot actually prove is the same record (#57). Rows with no uuid fall back to
  -- the columns the storage unique index already keeps distinct.
  SELECT DISTINCT ON (
    COALESCE(
      NULLIF(sr.uuid, ''),
      sr.session_id || '|' || sr.ts::text || '|' || sr.record_type || '|' || sr.profile_email
    )
  )
    sr.command_name AS skill_name,
    COALESCE(NULLIF(sr.command_invoke, ''), 'explicit') AS invoke_type,
    COALESCE(NULLIF(sr.agent, ''), 'claude') AS agent,
    COALESCE(sr.profile_email, '') AS profile_email,
    COALESCE(sr.login_email, '') AS login_email,
    COALESCE(sr.user_id, '') AS user_id,
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
    'ok' AS status,
    1::double precision AS metric_value
  FROM visible_session_records sr
  LEFT JOIN projects p ON p.agent = COALESCE(NULLIF(sr.agent, ''), 'claude') AND p.project_hash = sr.project_hash
  WHERE sr.command_name != ''
    -- Codex keeps its existing behaviour: its skills arrive as command_name and the
    -- metric arm above dedupes them. Claude joins only where the client said the
    -- name resolved to a skill -- without that, /clear and /model would be counted
    -- as skill usage, which is why Claude was excluded outright until now (#57).
    AND (
      COALESCE(NULLIF(sr.agent, ''), 'claude') = 'codex'
      OR (
        COALESCE(NULLIF(sr.agent, ''), 'claude') = 'claude'
        AND sr.command_kind = 'skill'
      )
    )
    AND sr.ts >= $1 AND sr.ts < $2
    AND ($3 = '' OR sr.profile_email = $3)
    AND ($4 = '' OR sr.user_id IN (SELECT user_id FROM login_user_ids))
    AND ($5 = '' OR sr.user_id = $5)
    AND ($6 = '' OR COALESCE(NULLIF(sr.agent, ''), 'claude') = $6)
    -- Dedupe against the metric copy of the same skill call. This reads the raw
    -- otel_metrics on purpose: on visible_metrics an excluded account's metrics
    -- disappear, the anti-join starts passing, and its JSONL rows come back — the
    -- exclusion would add rows instead of removing them.
    AND NOT EXISTS (
      SELECT 1
      FROM otel_metrics m
      WHERE m.metric_name = 'codex.skill.injected'
        AND m.ts >= $1 AND m.ts < $2
        AND m.session_id = sr.session_id
        AND COALESCE(NULLIF(m.dimensions->>'skill', ''), '') = sr.command_name
    )
),
combined_rows AS (
  SELECT * FROM metric_rows
  UNION ALL
  SELECT * FROM jsonl_rows
)
SELECT
  skill_name,
  agent,
  invoke_type,
  profile_email,
  login_email,
  user_id,
  project_hash,
  project_name,
  repository_id,
  repository_name,
  repo_subpath,
  has_git,
  ROUND(SUM(CASE WHEN status = 'ok' THEN metric_value ELSE 0 END))::bigint AS success_count,
  ROUND(SUM(CASE WHEN status = 'error' THEN metric_value ELSE 0 END))::bigint AS fail_count,
  ROUND(SUM(metric_value))::bigint AS total_count
FROM combined_rows
WHERE skill_name != ''
GROUP BY skill_name, agent, invoke_type, profile_email, login_email, user_id,
  project_hash, project_name, repository_id, repository_name, repo_subpath, has_git
ORDER BY total_count DESC, skill_name ASC, invoke_type ASC, profile_email ASC, user_id ASC
`
	rows, err := s.pool.Query(ctx, q, since, until, profileEmail, loginEmail, userID, agent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*SkillUsageSummary
	for rows.Next() {
		summary := &SkillUsageSummary{}
		if err := rows.Scan(
			&summary.SkillName,
			&summary.Agent,
			&summary.InvokeType,
			&summary.ProfileEmail,
			&summary.LoginEmail,
			&summary.UserID,
			&summary.ProjectHash,
			&summary.ProjectName,
			&summary.RepositoryID,
			&summary.RepositoryName,
			&summary.RepoSubpath,
			&summary.HasGit,
			&summary.SuccessCount,
			&summary.FailCount,
			&summary.TotalCount,
		); err != nil {
			return nil, err
		}
		results = append(results, summary)
	}
	return results, rows.Err()
}
