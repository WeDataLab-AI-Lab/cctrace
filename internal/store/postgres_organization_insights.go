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

	// The tokens count requests, as in WeeklyInsights. The period, the project
	// filter and the contributor filter are the lineWhere of both fragments below:
	// a request is folded from its lines in the period that have a contributor,
	// and only those lines are sorted to fold them.
	//
	// The rows are attributedLinesUnionSQL's, every request line at 0, and
	// request_tokens is each request's tokens (claudeRequestHeadsSQL) under the
	// key its head line has among the rows. A head's line is one of the rows, so
	// its group is there already and has counted its contributor, its session and
	// its hash: the join after the aggregation adds the request's tokens, once,
	// as the bigint the rows' total is.
	// Together they are attributedRowsSQL's sum, which this statement read as one
	// subquery until P1t-8. Its window arm has no parallel plan, so neither had
	// the UNION ALL holding it, and thirty days of rows were projected in the
	// leader: 2.7 s before the request set, 5.7 to 6.1 s with it.
	//
	// The heads are not one more UNION ALL arm of scoped_projects. That puts the
	// window back inside the union, which the planner then gathers arm by arm
	// under a serial Append, and it makes a head a second row of its line for any
	// count that is not DISTINCT.
	//
	// Planned for each call (plannedPerCall), as the reads this shape comes from
	// are: locally the generic plan of this statement is serial too.
	const projectsLineWhere = `sr.ts >= $1 AND sr.ts < $2 AND sr.project_hash <> ''
		AND COALESCE(NULLIF(sr.user_id, ''), NULLIF(sr.profile_email, ''), NULLIF(sr.login_email, '')) IS NOT NULL`
	// The repository identity of a row sr with its projects row p, and that join.
	// One text for the rows and for the requests' heads: a head whose key differed
	// from its line's would add its tokens to no group. Same key as the weekly
	// report. Fixing one and not the other leaves the two screens disagreeing
	// about the same week.
	const projectIdentity = `CASE WHEN BTRIM(COALESCE(p.repository_id, '')) <> ''
				AND LOWER(BTRIM(p.repository_id)) NOT LIKE 'local:%'
			THEN BTRIM(p.repository_id) ELSE '' END AS identity_repository_id,
		CASE WHEN BTRIM(COALESCE(p.repository_id, '')) <> ''
				AND LOWER(BTRIM(p.repository_id)) NOT LIKE 'local:%'
			THEN '' ELSE sr.project_hash END AS identity_group`
	const projectJoin = `LEFT JOIN projects p
		ON p.agent = COALESCE(NULLIF(sr.agent, ''), 'claude')
		AND p.project_hash = sr.project_hash`
	// The arms give a row's contributor, not the three columns it is read from,
	// so the rows are narrower on their way through the sort.
	projectsQuery := `
WITH scoped_projects AS (
	SELECT sr.project_hash,
		sr.session_id,
		sr.input_tokens,
		sr.output_tokens,
		sr.contributor,
		` + projectIdentity + `
	FROM (` + attributedLinesUnionSQL("visible_session_records", projectsLineWhere,
		[]string{"project_hash", "session_id", "agent", "user_id", "profile_email", "login_email"},
		`project_hash, session_id, agent, req_input_tokens AS input_tokens, req_output_tokens AS output_tokens,
		COALESCE(NULLIF(user_id, ''), NULLIF(profile_email, ''), NULLIF(login_email, '')) AS contributor`, "") + `) sr
	` + projectJoin + `
), project_groups AS (
	SELECT identity_repository_id, identity_group,
		MIN(project_hash) AS project_hash,
		ARRAY_AGG(DISTINCT project_hash ORDER BY project_hash) AS project_hashes,
		COUNT(DISTINCT contributor) AS contributors,
		COUNT(DISTINCT session_id) AS session_count,
		COALESCE(SUM(COALESCE(input_tokens, 0) + COALESCE(output_tokens, 0)), 0) AS total_tokens
	FROM scoped_projects
	GROUP BY identity_repository_id, identity_group
), request_tokens AS (
	SELECT ` + projectIdentity + `,
		SUM(COALESCE(sr.input_tokens, 0) + COALESCE(sr.output_tokens, 0))::bigint AS total_tokens
	FROM (` + claudeRequestHeadsSQL("visible_session_records", projectsLineWhere, []string{"project_hash", "agent"}) + `) sr
	` + projectJoin + `
	GROUP BY 1, 2
)
SELECT g.project_hash, g.project_hashes, g.contributors, g.session_count,
	g.total_tokens + COALESCE(rt.total_tokens, 0) AS total_tokens
FROM project_groups g
LEFT JOIN request_tokens rt
	ON rt.identity_repository_id = g.identity_repository_id AND rt.identity_group = g.identity_group
WHERE g.contributors >= $3
ORDER BY total_tokens DESC, project_hash ASC`
	rows, err = s.pool.Query(ctx, projectsQuery, plannedPerCall, since, until, minUsers)
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
