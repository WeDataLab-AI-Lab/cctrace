package store

import (
	"context"
	"time"
)

// organizationToolRowsCTE is every tool call in [$1, $2) with a known identity,
// read from toolCallRowsSQL so this page counts what the Tools page counts.
const organizationToolRowsCTE = `
WITH tool_rows AS (
  SELECT tool_name,
    COALESCE(NULLIF(user_id, ''), NULLIF(profile_email, ''), NULLIF(login_email, '')) AS identity,
    uses, successes, fails
  FROM ` + toolCallRowsSQL + `
  WHERE ts >= $1 AND ts < $2
    AND COALESCE(NULLIF(user_id, ''), NULLIF(profile_email, ''), NULLIF(login_email, '')) IS NOT NULL
)`

// OrganizationInsights returns only dimensions contributed by at least minUsers
// distinct identifiable users. Unknown identities are never counted as a cohort.
func (s *PgStore) OrganizationInsights(ctx context.Context, since, until time.Time, minUsers int64) (*OrganizationInsights, error) {
	result := &OrganizationInsights{MinimumUsers: minUsers}
	const activeUsersQuery = `
WITH identities AS (
	  SELECT COALESCE(NULLIF(user_id, ''), NULLIF(profile_email, ''), NULLIF(login_email, '')) AS identity
	  FROM visible_events
	  WHERE ts >= $1 AND ts < $2
	  UNION
	  SELECT COALESCE(NULLIF(user_id, ''), NULLIF(profile_email, ''), NULLIF(login_email, '')) AS identity
	  FROM visible_session_records
	  WHERE ts >= $1 AND ts < $2
	  UNION
	  -- Codex reports through metrics, not events (#698). Only the two that mean
	  -- someone worked: the app also emits websocket, sqlite and model-list
	  -- counters while idle, and reading all of them took 1.15s for 7 days on
	  -- prod against 0.14s for these two (the query without them: 0.44s).
	  SELECT COALESCE(NULLIF(user_id, ''), NULLIF(profile_email, ''), NULLIF(login_email, '')) AS identity
	  FROM visible_metrics
	  WHERE ts >= $1 AND ts < $2 AND metric_name IN ('codex.turn.token_usage', 'codex.tool.call')
)
SELECT COUNT(*) FROM identities WHERE identity IS NOT NULL AND identity != ''`
	if err := s.pool.QueryRow(ctx, activeUsersQuery, since, until).Scan(&result.ActiveUsers); err != nil {
		return nil, err
	}
	if result.ActiveUsers < minUsers {
		result.ActiveUsers = 0
		return result, nil
	}
	result.Available = true

	const modelsQuery = `
SELECT model,
  COUNT(DISTINCT COALESCE(NULLIF(user_id, ''), NULLIF(profile_email, ''), NULLIF(login_email, ''))) AS contributors,
  COALESCE(SUM(input_tokens), 0) AS input_tokens,
  COALESCE(SUM(output_tokens), 0) AS output_tokens,
  COALESCE(SUM(cost_usd), 0) AS total_cost
FROM visible_events
WHERE ts >= $1 AND ts < $2 AND model != ''
  AND COALESCE(NULLIF(user_id, ''), NULLIF(profile_email, ''), NULLIF(login_email, '')) IS NOT NULL
GROUP BY model
HAVING COUNT(DISTINCT COALESCE(NULLIF(user_id, ''), NULLIF(profile_email, ''), NULLIF(login_email, ''))) >= $3
ORDER BY total_cost DESC, model ASC`
	rows, err := s.pool.Query(ctx, modelsQuery, since, until, minUsers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		row := OrganizationModelInsight{}
		if err := rows.Scan(&row.Model, &row.ContributorCount, &row.InputTokens, &row.OutputTokens, &row.CostUSD); err != nil {
			return nil, err
		}
		result.Models = append(result.Models, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	const toolsQuery = organizationToolRowsCTE + `
SELECT tool_name,
  COUNT(DISTINCT identity) AS contributors,
  SUM(uses)::bigint AS use_count,
  SUM(successes)::bigint AS success_count,
  SUM(fails)::bigint AS fail_count
FROM tool_rows
GROUP BY tool_name
HAVING COUNT(DISTINCT identity) >= $3
ORDER BY use_count DESC, tool_name ASC`
	rows, err = s.pool.Query(ctx, toolsQuery, since, until, minUsers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		row := OrganizationToolInsight{}
		if err := rows.Scan(&row.ToolName, &row.ContributorCount, &row.UseCount, &row.SuccessCount, &row.FailCount); err != nil {
			return nil, err
		}
		result.Tools = append(result.Tools, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	const hoursQuery = `
SELECT EXTRACT(HOUR FROM ts AT TIME ZONE 'UTC')::int AS hour,
  COUNT(DISTINCT COALESCE(NULLIF(user_id, ''), NULLIF(profile_email, ''), NULLIF(login_email, ''))) AS contributors,
  COUNT(DISTINCT session_id) AS session_count
FROM visible_session_records
WHERE ts >= $1 AND ts < $2
  AND COALESCE(NULLIF(user_id, ''), NULLIF(profile_email, ''), NULLIF(login_email, '')) IS NOT NULL
GROUP BY hour
HAVING COUNT(DISTINCT COALESCE(NULLIF(user_id, ''), NULLIF(profile_email, ''), NULLIF(login_email, ''))) >= $3
ORDER BY session_count DESC, hour ASC`
	rows, err = s.pool.Query(ctx, hoursQuery, since, until, minUsers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		row := OrganizationHourInsight{}
		if err := rows.Scan(&row.Hour, &row.ContributorCount, &row.SessionCount); err != nil {
			return nil, err
		}
		result.Hours = append(result.Hours, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	const projectsQuery = `
WITH scoped_projects AS (
	SELECT sr.project_hash,
		sr.session_id,
		sr.input_tokens,
		sr.output_tokens,
		COALESCE(NULLIF(sr.user_id, ''), NULLIF(sr.profile_email, ''), NULLIF(sr.login_email, '')) AS contributor,
		CASE WHEN BTRIM(COALESCE(p.repository_id, '')) <> ''
				AND LOWER(BTRIM(p.repository_id)) NOT LIKE 'local:%'
			THEN BTRIM(p.repository_id) ELSE '' END AS identity_repository_id,
		-- Same key as the weekly report. Fixing one and not the other leaves the
		-- two screens disagreeing about the same week.
		CASE WHEN BTRIM(COALESCE(p.repository_id, '')) <> ''
				AND LOWER(BTRIM(p.repository_id)) NOT LIKE 'local:%'
			THEN '' ELSE sr.project_hash END AS identity_group
	FROM visible_session_records sr
	LEFT JOIN projects p
		ON p.agent = COALESCE(NULLIF(sr.agent, ''), 'claude')
		AND p.project_hash = sr.project_hash
	WHERE sr.ts >= $1 AND sr.ts < $2 AND sr.project_hash <> ''
), project_groups AS (
	SELECT identity_repository_id, identity_group,
		MIN(project_hash) AS project_hash,
		ARRAY_AGG(DISTINCT project_hash ORDER BY project_hash) AS project_hashes,
		COUNT(DISTINCT contributor) AS contributors,
		COUNT(DISTINCT session_id) AS session_count,
		COALESCE(SUM(COALESCE(input_tokens, 0) + COALESCE(output_tokens, 0)), 0) AS total_tokens
	FROM scoped_projects
	WHERE contributor IS NOT NULL
	GROUP BY identity_repository_id, identity_group
)
SELECT project_hash, project_hashes, contributors, session_count, total_tokens
FROM project_groups
WHERE contributors >= $3
ORDER BY total_tokens DESC, project_hash ASC`
	rows, err = s.pool.Query(ctx, projectsQuery, since, until, minUsers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		row := OrganizationProjectInsight{}
		if err := rows.Scan(&row.ProjectHash, &row.ProjectHashes, &row.ContributorCount, &row.SessionCount, &row.TotalTokens); err != nil {
			return nil, err
		}
		result.Projects = append(result.Projects, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	const bottlenecksQuery = organizationToolRowsCTE + `
SELECT tool_name,
  COUNT(DISTINCT identity) AS contributors,
  SUM(uses)::bigint AS use_count,
  SUM(fails)::bigint AS fail_count
FROM tool_rows
GROUP BY tool_name
HAVING COUNT(DISTINCT identity) >= $3
  AND SUM(fails) > 0
ORDER BY fail_count DESC, use_count DESC, tool_name ASC`
	rows, err = s.pool.Query(ctx, bottlenecksQuery, since, until, minUsers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		row := OrganizationBottleneckInsight{}
		if err := rows.Scan(&row.ToolName, &row.ContributorCount, &row.UseCount, &row.FailCount); err != nil {
			return nil, err
		}
		result.Bottlenecks = append(result.Bottlenecks, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	const tasksQuery = `
SELECT task_type,
  COUNT(DISTINCT COALESCE(NULLIF(user_id, ''), NULLIF(profile_email, ''), NULLIF(login_email, ''))) AS contributors,
  COUNT(*) AS prompt_count
FROM visible_session_records
WHERE ts >= $1 AND ts < $2 AND task_type <> ''
  AND COALESCE(NULLIF(user_id, ''), NULLIF(profile_email, ''), NULLIF(login_email, '')) IS NOT NULL
GROUP BY task_type
HAVING COUNT(DISTINCT COALESCE(NULLIF(user_id, ''), NULLIF(profile_email, ''), NULLIF(login_email, ''))) >= $3
ORDER BY prompt_count DESC, task_type ASC`
	rows, err = s.pool.Query(ctx, tasksQuery, since, until, minUsers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		row := OrganizationTaskInsight{}
		if err := rows.Scan(&row.TaskType, &row.ContributorCount, &row.PromptCount); err != nil {
			return nil, err
		}
		result.Tasks = append(result.Tasks, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Same change as WeeklyInsights, for the same reason: the denominator has to
	// be counted over the rows Tasks is counted over. Summing segment facts gave
	// a number that could be smaller than what it divides, and both surfaces
	// render it as a percentage. Fixing one and not the other would leave the two
	// screens disagreeing about the same week.
	typedTurnQuery := `
SELECT COUNT(*)
FROM visible_session_records sr
WHERE sr.ts >= $1 AND sr.ts < $2
  AND sr.record_type = 'user'
  AND ` + typedTurnPredicateSQL("sr")
	if err := s.pool.QueryRow(ctx, typedTurnQuery, since, until).Scan(&result.TypedTurnCount); err != nil {
		return nil, err
	}
	return result, nil
}
