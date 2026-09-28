package store

import (
	"context"
	"strconv"
	"time"

	"cctrace/internal/gitctx"
)

// A session is treated as having no usable timeline when this many records share
// a span this short. The measurement behind the thresholds is in WeeklyInsights.
const (
	collapsedTimelineMinRecords = 20
	collapsedTimelineMaxSpan    = "1 second"
)

// WeeklyInsights reads only one caller's aggregate session and tool metadata.
// tz is an IANA zone name (e.g. "Asia/Seoul"); an empty tz defaults to UTC.
func (s *PgStore) WeeklyInsights(ctx context.Context, since, until time.Time, profileEmail, userID, tz string) (*WeeklyInsights, error) {
	if tz == "" {
		tz = "UTC"
	}
	r := &WeeklyInsights{AgentSessions: []WeeklyInsightAgentSessions{}}
	const scope = `AND (($3 <> '' AND user_id = $3) OR ($4 <> '' AND profile_email = $4))`
	agents, err := s.pool.Query(ctx, `SELECT COALESCE(NULLIF(agent, ''), 'claude'), COUNT(DISTINCT session_id)
FROM visible_session_records WHERE ts >= $1 AND ts < $2 `+scope+`
GROUP BY 1 ORDER BY 1`, since, until, userID, profileEmail)
	if err != nil {
		return nil, err
	}
	defer agents.Close()
	for agents.Next() {
		var v WeeklyInsightAgentSessions
		if err := agents.Scan(&v.Agent, &v.SessionCount); err != nil {
			return nil, err
		}
		r.AgentSessions = append(r.AgentSessions, v)
	}
	if err := agents.Err(); err != nil {
		return nil, err
	}
	// task_segment_facts is outside the exclusion views; the boundary record is
	// how TaskSegmentsByType asks whether a segment is still visible.
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM task_segment_facts f
WHERE start_ts >= $1 AND start_ts < $2 `+scope+`
AND EXISTS (SELECT 1 FROM visible_session_records vsr WHERE vsr.id = f.boundary_record_id)`,
		since, until, userID, profileEmail).Scan(&r.SegmentCount); err != nil {
		return nil, err
	}
	// Coverage is session-wide: a fact starting before this week still covers it.
	// Facts are built only from typed user turns, so only sessions with one can
	// be uncovered.
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(DISTINCT session_id)
FROM visible_session_records sr WHERE ts >= $1 AND ts < $2 `+scope+`
AND sr.record_type = 'user' AND sr.session_id <> ''
AND `+typedTurnPredicateSQL("sr")+`
AND NOT EXISTS (SELECT 1 FROM task_segment_facts
 WHERE session_id = sr.session_id `+scope+`)`,
		since, until, userID, profileEmail).Scan(&r.UncoveredSessionCount); err != nil {
		return nil, err
	}
	// What the exclusion views took from this caller's week, read as the
	// difference of two counts over the same window and scope.
	//
	// The difference is exact, not an approximation: visible_session_records is
	// SELECT * FROM session_records with a WHERE of four NOT EXISTS clauses and
	// nothing else -- the three account predicates of excludedAccountPredicateSQL
	// plus the excluded_sessions one (see dependentViews in migrations.go). A view
	// of that shape is a row filter -- it has no join that could duplicate a row or
	// drop one for any reason other than the exclusion -- so over any predicate
	// that reads only session_records columns, hidden = all - visible by
	// construction. The window and scope here read only such columns, so both
	// halves keep the same $1..$4.
	//
	// NULL cannot drop a row through those four either, and that is a property of
	// NOT EXISTS specifically: a comparison against NULL inside the subquery does
	// not match, the subquery finds nothing, NOT EXISTS is true, and the row stays
	// visible. NOT IN would invert exactly this -- one NULL in its list makes the
	// whole predicate NULL and silently hides every row -- so anyone editing those
	// predicates has to keep them NOT EXISTS for this identity to hold.
	//
	// This replaces an anti-join, NOT EXISTS (SELECT 1 FROM
	// visible_session_records v WHERE v.id = sr.id), which asked the same question
	// per row. Measured on prod for the heaviest week: 1955ms for the anti-join
	// against 29ms + 102ms for the two counts, same answer (31,281). The anti-join
	// had to evaluate the whole exclusion predicate once per outer row and probe
	// id for each; the counts evaluate it once per row, once, and the planner
	// aggregates each side on its own.
	//
	// A second reason to prefer this shape: the anti-join is only equivalent to
	// "this row is hidden" while session_records.id is unique. id is a BIGSERIAL
	// with a plain (non-unique) index -- session_records is a hypertable and
	// cannot carry a primary key that excludes ts -- so nothing in the schema
	// enforces it. The subtraction does not depend on id at all.
	if err := s.pool.QueryRow(ctx, `SELECT
 (SELECT COUNT(*) FROM session_records sr WHERE sr.ts >= $1 AND sr.ts < $2 `+scope+`)
-(SELECT COUNT(*) FROM visible_session_records sr WHERE sr.ts >= $1 AND sr.ts < $2 `+scope+`)`,
		since, until, userID, profileEmail).Scan(&r.ExcludedRecordCount); err != nil {
		return nil, err
	}

	hours, err := s.pool.Query(ctx, `SELECT EXTRACT(HOUR FROM ts AT TIME ZONE $5)::int, COUNT(DISTINCT session_id)
FROM visible_session_records WHERE ts >= $1 AND ts < $2 `+scope+` GROUP BY 1 ORDER BY 1`, since, until, userID, profileEmail, tz)
	if err != nil {
		return nil, err
	}
	defer hours.Close()
	for hours.Next() {
		var v WeeklyInsightHour
		if err := hours.Scan(&v.Hour, &v.SessionCount); err != nil {
			return nil, err
		}
		r.Hours = append(r.Hours, v)
	}
	if err := hours.Err(); err != nil {
		return nil, err
	}
	projects, err := s.pool.Query(ctx, `
WITH scoped_projects AS (
	SELECT sr.project_hash,
		sr.session_id,
		sr.input_tokens,
		sr.output_tokens,
		CASE WHEN BTRIM(COALESCE(p.repository_id, '')) <> ''
				AND LOWER(BTRIM(p.repository_id)) NOT LIKE 'local:%'
			THEN BTRIM(p.repository_id) ELSE '' END AS identity_repository_id,
		-- One row per repository. repo_subpath used to sit in this key, which kept
		-- a monorepo subdirectory as its own identity (#257) -- and also split a
		-- repo the moment someone started a session from inside one of its
		-- folders: claude-plugins and claude-plugins/staging/ showed as two
		-- projects with separate token totals. A repository is the unit people
		-- think in, so the subpath leaves the key.
		--
		-- Unresolved ids still fall back to project_hash, so two unrelated local
		-- checkouts never merge into one line.
		CASE WHEN BTRIM(COALESCE(p.repository_id, '')) <> ''
				AND LOWER(BTRIM(p.repository_id)) NOT LIKE 'local:%'
			THEN '' ELSE sr.project_hash END AS identity_group,
		COALESCE(NULLIF(p.repository_name, ''), NULLIF(p.project_name, ''), '') AS project_name
	FROM visible_session_records sr
	LEFT JOIN projects p
		ON p.agent = COALESCE(NULLIF(sr.agent, ''), 'claude')
		AND p.project_hash = sr.project_hash
	WHERE sr.ts >= $1 AND sr.ts < $2 `+scope+` AND sr.project_hash <> ''
)
SELECT MIN(project_hash), ARRAY_AGG(DISTINCT project_hash ORDER BY project_hash),
	identity_repository_id,
	COALESCE(NULLIF(MAX(project_name), ''), ''), COUNT(DISTINCT session_id),
	COALESCE(SUM(COALESCE(input_tokens, 0) + COALESCE(output_tokens, 0)), 0)
FROM scoped_projects
GROUP BY identity_repository_id, identity_group
ORDER BY 6 DESC, 1`, since, until, userID, profileEmail)
	if err != nil {
		return nil, err
	}
	defer projects.Close()
	for projects.Next() {
		var v WeeklyInsightProject
		var repositoryID string
		if err := projects.Scan(&v.ProjectHash, &v.ProjectHashes, &repositoryID,
			&v.ProjectName, &v.SessionCount, &v.TotalTokens); err != nil {
			return nil, err
		}
		// Derived from the id rather than read from the row, and derived here
		// rather than in SQL so there is one definition of the rule. Storing the
		// name beside the id let the two disagree: on prod 17 repositories carried
		// two names each -- one of them the worktree's own directory, so a
		// repository displayed under its worktree codename once MAX() over the
		// group picked the later string (#691).
		if name := gitctx.RepositoryNameFromID(repositoryID); name != "" {
			v.ProjectName = name
		}
		r.Projects = append(r.Projects, v)
	}
	if err := projects.Err(); err != nil {
		return nil, err
	}
	tasks, err := s.pool.Query(ctx, `SELECT task_type, COUNT(*) FROM visible_session_records WHERE ts >= $1 AND ts < $2 `+scope+` AND task_type <> '' GROUP BY task_type ORDER BY 2 DESC, 1`, since, until, userID, profileEmail)
	if err != nil {
		return nil, err
	}
	defer tasks.Close()
	for tasks.Next() {
		var v WeeklyInsightTask
		if err := tasks.Scan(&v.TaskType, &v.PromptCount); err != nil {
			return nil, err
		}
		r.Tasks = append(r.Tasks, v)
	}
	if err := tasks.Err(); err != nil {
		return nil, err
	}
	// Both agents' calls, as the Tools page counts them (toolCallRowsSQL).
	tools, err := s.pool.Query(ctx, `SELECT tool_name, SUM(uses)::bigint, SUM(fails)::bigint
FROM `+toolCallRowsSQL+` WHERE ts >= $1 AND ts < $2 `+scope+`
GROUP BY tool_name ORDER BY 3 DESC, 2 DESC, 1`, since, until, userID, profileEmail)
	if err != nil {
		return nil, err
	}
	defer tools.Close()
	for tools.Next() {
		var v WeeklyInsightTool
		if err := tools.Scan(&v.ToolName, &v.UseCount, &v.FailCount); err != nil {
			return nil, err
		}
		r.Tools = append(r.Tools, v)
	}
	if err := tools.Err(); err != nil {
		return nil, err
	}
	// Counted from visible_session_records, the same rows Tasks is counted from.
	// This used to sum task_segment_facts.typed_turn_count -- cheaper, because
	// that table is small and this scan measured 209ms on prod (no index on the
	// jsonb expression) -- but it is a different population and the dashboard
	// divides by it. Segments carry only the turns a reconciler has already
	// folded in, and they are windowed by the segment's start_ts rather than by
	// each turn's own ts, so a prompt outside a segment is counted by Tasks and
	// not by its denominator. On dev with production data the card read
	// "Testing 243 prompts (486%)" and "Request of no listed type 1,020 (2040%)",
	// and the suggestion beneath it repeated "486% of what you asked for was
	// testing". Reconciler lag is not an edge case -- it is the subject of #435.
	//
	// Correctness over the 209ms: this runs once for a weekly page, and a
	// denominator smaller than what it divides is not a slower answer, it is a
	// wrong one.
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*)
FROM visible_session_records sr
WHERE sr.ts >= $1 AND sr.ts < $2
  AND (($3 <> '' AND sr.user_id = $3) OR ($4 <> '' AND sr.profile_email = $4))
  AND sr.record_type = 'user'
  AND `+typedTurnPredicateSQL("sr"),
		since, until, userID, profileEmail).
		Scan(&r.TypedTurnCount); err != nil {
		return nil, err
	}

	// Sessions whose whole record span fits inside a second while holding twenty
	// or more records. Measured on a production copy: 33 of 5,556 sessions over 90
	// days (0.59%) -- selective, and not zero.
	//
	// The thresholds are deliberately conservative. Twenty records inside one
	// second is not a fast conversation, it is a file whose timestamps were all
	// written at once; a looser cut (ten records, or five seconds) adds only a
	// handful and starts to touch short real sessions.
	if err := s.pool.QueryRow(ctx, `WITH spans AS (
	SELECT session_id, count(*) AS recs, max(ts) - min(ts) AS span
	FROM visible_session_records
	WHERE ts >= $1 AND ts < $2 `+scope+`
	GROUP BY session_id)
SELECT count(*) FROM spans WHERE recs >= `+strconv.Itoa(collapsedTimelineMinRecords)+`
  AND span < interval '`+collapsedTimelineMaxSpan+`'`,
		since, until, userID, profileEmail).Scan(&r.CollapsedTimelineSessions); err != nil {
		return nil, err
	}
	return r, nil
}
