package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"cctrace/internal/activitylabel"
)

const taskSegmentFactsBackfill = "task_segment_facts_v1"

func lockTaskSegmentFacts(ctx context.Context, tx pgx.Tx, exclusive bool) error {
	q := `SELECT pg_advisory_xact_lock_shared(hashtext($1))`
	if exclusive {
		q = `SELECT pg_advisory_xact_lock(hashtext($1))`
	}
	if _, err := tx.Exec(ctx, q, taskSegmentFactsBackfill); err != nil {
		return fmt.Errorf("lock task segment facts: %w", err)
	}
	return nil
}

// refreshTaskSegmentFacts replaces all segments for touched sessions. Recomputing
// the session, rather than appending, is required because a boundary earlier in the
// session can change where every later segment starts (a new project switch, or a
// record arriving out of order during sync).
// Empty input, including nil, is a no-op. Full rebuilds use a separate entry point.
func refreshTaskSegmentFacts(ctx context.Context, tx pgx.Tx, sessionIDs []string) error {
	sessionIDs = factRefreshSessions(sessionIDs)
	if len(sessionIDs) == 0 {
		return nil
	}
	if err := lockTaskSegmentFacts(ctx, tx, false); err != nil {
		return err
	}
	if err := lockTaskSegmentSessions(ctx, tx, sessionIDs); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM task_segment_facts
		WHERE session_id = ANY($1::text[])`, sessionIDs); err != nil {
		return fmt.Errorf("delete task segment facts: %w", err)
	}
	// Unprepared, so each list is planned for itself (taskSegmentFactsRefreshSQL).
	if _, err := tx.Exec(ctx, taskSegmentFactsRefreshSQL, pgx.QueryExecModeExec, sessionIDs, activitylabel.Version); err != nil {
		return fmt.Errorf("insert task segment facts: %w", err)
	}
	return nil
}

// lockTaskSegmentSessions takes the per-session locks of the sessions' task
// segment facts, in session_id order, after the shared fact lock:
// refreshTaskSegmentFacts takes them before it deletes a session's segments, and
// step 8 of the P1t transition before it updates their tokens, so the two do not
// interleave on a session. Not every writer takes them first:
// CleanupOrphanSessionRecords deletes orphaned segment rows before its refresh
// takes them.
func lockTaskSegmentSessions(ctx context.Context, tx pgx.Tx, sessionIDs []string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(session_id, 953))
		FROM unnest($1::text[]) session_id ORDER BY session_id`, sessionIDs); err != nil {
		return fmt.Errorf("lock task segment sessions: %w", err)
	}
	return nil
}

// rebuildAllTaskSegmentFacts replaces every fact under the exclusive maintenance lock.
// Keep maintenance before per-session and relation locks, as in scoped refreshes.
func rebuildAllTaskSegmentFacts(ctx context.Context, tx pgx.Tx) error {
	if err := lockTaskSegmentFacts(ctx, tx, true); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM task_segment_facts`); err != nil {
		return fmt.Errorf("delete task segment facts: %w", err)
	}
	if _, err := tx.Exec(ctx, taskSegmentFactsRebuildSQL, activitylabel.Version); err != nil {
		return fmt.Errorf("insert task segment facts: %w", err)
	}
	return nil
}

// BackfillTaskSegmentFacts performs the initial unbounded segmentation scan. Its marker
// commits with the facts, making retries crash-safe and exactly once.
func (s *PgStore) BackfillTaskSegmentFacts(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return err
	}
	if err := lockTaskSegmentFacts(ctx, tx, true); err != nil {
		return err
	}
	var done bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM schema_backfills WHERE name = $1)`, taskSegmentFactsBackfill).Scan(&done); err != nil {
		return err
	}
	if done {
		return tx.Commit(ctx)
	}
	if err := rebuildAllTaskSegmentFacts(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_backfills (name) VALUES ($1)`, taskSegmentFactsBackfill); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ReconcileTaskSegmentFactsForRetention removes facts whose typed turns were dropped
// by Timescale retention. Retention drops the oldest time chunks, so facts starting
// before the surviving source minimum are a bounded candidate set; touched sessions
// are recomputed in case newer segments remain.
func (s *PgStore) ReconcileTaskSegmentFactsForRetention(ctx context.Context) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return 0, err
	}
	var oldest *time.Time
	if err := tx.QueryRow(ctx, `SELECT min(ts) FROM session_records`).Scan(&oldest); err != nil {
		return 0, err
	}
	q := `SELECT DISTINCT session_id FROM task_segment_facts`
	var args []interface{}
	if oldest != nil {
		q += ` WHERE start_ts < $1`
		args = append(args, *oldest)
	}
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	var sessionIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		sessionIDs = append(sessionIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(sessionIDs) == 0 {
		return 0, tx.Commit(ctx)
	}
	if err := refreshTaskSegmentFacts(ctx, tx, sessionIDs); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(sessionIDs), nil
}

// MATERIALIZED is load-bearing here for the same reason it is in
// pluginInvocationFactsInsertSQL: Postgres inlines a CTE referenced once, and a
// correlated per-row computation pushed into a later join's filter is re-evaluated
// per candidate row pair instead of once per row. `turns` never contains a
// correlated subquery -- the boundary flag is a plain LAG comparison -- but `segs`
// does the same trick as plugin facts learned the hard way: end_ts is computed by
// LEAD over the small per-segment result, never by a per-turn scalar subquery
// against the full session_records table. Keeping every intermediate CTE
// materialized keeps that shape locked in even if a future edit adds one.
//
// The token columns come from seg_tokens (taskSegmentTokensCTEs), which counts a
// request once, at its head line; every other column comes from the range
// LATERALs below.
//
// sessions names the sessions to rebuild: $1::text[], or NULL::text[] for all --
// a value the planner sees, as syncAttributedRowsSQL requires -- and labeler the
// parameter carrying activitylabel.Version.
func taskSegmentFactsInsertSQL(sessions, labeler string) string {
	return `
WITH turns AS MATERIALIZED (
	SELECT sr.id, sr.session_id, sr.ts,
		COALESCE(sr.project_hash, '') AS project_hash,
		COALESCE(sr.repository_id, '') AS repository_id,
		COALESCE(sr.repository_name, '') AS repository_name,
		COALESCE(sr.repo_subpath, '') AS repo_subpath,
		COALESCE(sr.commit_sha, '') AS commit_sha,
		COALESCE(sr.branch, '') AS branch,
		sr.profile_email, COALESCE(sr.login_email, '') AS login_email,
		COALESCE(sr.user_id, '') AS user_id,
		COALESCE(NULLIF(sr.agent, ''), 'claude') AS agent,
		LAG(sr.ts) OVER w AS prev_ts,
		LAG(sr.project_hash) OVER w AS prev_project_hash
	FROM session_records sr
	-- Codex has no message.content at all -- its typed text lives under
	-- payload.content (an array), never a plain string -- so this condition alone
	-- would exclude every Codex turn. Same rule pluginInvocationFactsInsertSQL
	-- already uses for next_user_ts. Confirmed on prod: all 50,518 Codex user rows
	-- have message.content = NULL and payload.content = array.
	--
	-- A plain-string check alone also misses real Claude turns: measured on prod,
	-- 8,375 of 370,858 array-shaped message.content values carry a real
	-- text/input_text block (the rest are tool_result) -- see insights.IsTypedTurn.
	WHERE sr.record_type = 'user'
	  AND NOT EXISTS (SELECT 1 FROM deleted_sessions d WHERE d.session_id = sr.session_id)
	  AND (COALESCE(NULLIF(sr.agent, ''), 'claude') = 'codex'
	    OR jsonb_typeof(sr.raw->'message'->'content') = 'string'
	    OR (jsonb_typeof(sr.raw->'message'->'content') = 'array' AND EXISTS (
	          SELECT 1 FROM jsonb_array_elements(sr.raw->'message'->'content') e
	          WHERE e->>'type' IN ('text', 'input_text')
	        )))
	  AND (` + sessions + ` IS NULL OR sr.session_id = ANY(` + sessions + `))
	WINDOW w AS (PARTITION BY sr.session_id ORDER BY sr.ts)
),
marked AS MATERIALIZED (
	SELECT *,
		CASE WHEN prev_ts IS NULL
		       OR EXTRACT(EPOCH FROM ts - prev_ts) >= 1800
		       OR project_hash IS DISTINCT FROM prev_project_hash
		     THEN 1 ELSE 0 END AS starts_run
	FROM turns
),
runs AS MATERIALIZED (
	SELECT *, SUM(starts_run) OVER (PARTITION BY session_id ORDER BY ts
	                                ROWS UNBOUNDED PRECEDING) AS seg_num
	FROM marked
),
segs AS MATERIALIZED (
	SELECT session_id, seg_num,
		MAX(id) FILTER (WHERE starts_run = 1) AS boundary_record_id,
		MIN(ts) AS start_ts,
		COUNT(*) AS typed_turn_count,
		MAX(project_hash) AS project_hash, MAX(repository_id) AS repository_id,
		MAX(repository_name) AS repository_name, MAX(repo_subpath) AS repo_subpath,
		MAX(commit_sha) AS commit_sha, MAX(branch) AS branch,
		MAX(profile_email) AS profile_email, MAX(login_email) AS login_email,
		MAX(user_id) AS user_id, MAX(agent) AS agent
	FROM runs
	GROUP BY session_id, seg_num
),
bounded AS MATERIALIZED (
	SELECT *, LEAD(start_ts) OVER (PARTITION BY session_id ORDER BY start_ts) AS end_ts
	FROM segs
),
` + taskSegmentTokensCTEs("bounded", sessions) + `
INSERT INTO task_segment_facts (
	boundary_record_id, session_id, start_ts, end_ts, boundary_reason,
	profile_email, login_email, user_id, agent,
	project_hash, repository_id, repository_name, repo_subpath, commit_sha, branch,
	turn_count, typed_turn_count, tool_call_count, tool_fail_count,
	command_count, had_compact, input_tokens, output_tokens,
	activity_type, labeler_version)
SELECT b.boundary_record_id, b.session_id, b.start_ts, b.end_ts, 'gap_or_project',
	b.profile_email, b.login_email, b.user_id, b.agent,
	b.project_hash, b.repository_id, b.repository_name, b.repo_subpath, b.commit_sha, b.branch,
	b.typed_turn_count + COALESCE(sr.record_count, 0), b.typed_turn_count,
	COALESCE(jt.tool_call_count, pi.tool_call_count, ev.tool_call_count, 0),
	COALESCE(pi.tool_fail_count, ev.tool_fail_count, 0),
	COALESCE(sr.command_count, 0), COALESCE(sr.had_compact, false),
	COALESCE(st.input_tokens, 0), COALESCE(st.output_tokens, 0),
	-- Mirrors internal/activitylabel.Classify exactly (first-match-wins); see
	-- that package for the Stage 3 cluster-centroid citation behind each rule
	-- and why ship_signal/test_signal/doc_signal are not computed here.
	CASE
		WHEN COALESCE(ev.tool_fail_count, 0) > 0 THEN 'diagnose'
		WHEN COALESCE(ev.write_calls, 0) = 0 AND COALESCE(ev.tool_call_count, 0) > 0
		     AND COALESCE(ev.bash_calls, 0)::float8 / ev.tool_call_count > 0.8 THEN 'ops'
		WHEN COALESCE(ev.write_calls, 0) > 0 THEN 'author'
		WHEN COALESCE(ev.read_calls, 0) > 0 THEN 'explore'
		ELSE 'unknown'
	END,
	` + labeler + `
FROM bounded b
LEFT JOIN LATERAL (
	SELECT count(*) AS record_count,
		count(*) FILTER (WHERE r.command_name <> '') AS command_count,
		bool_or(r.is_compact_summary) AS had_compact
	FROM session_records r
	WHERE r.session_id = b.session_id AND r.ts >= b.start_ts
	  AND (b.end_ts IS NULL OR r.ts < b.end_ts)
) sr ON true
LEFT JOIN seg_tokens st ON st.boundary_record_id = b.boundary_record_id
-- Tool events are OTEL, delivered on a separate ingest path from session_records
-- sync, and this whole INSERT only runs when refreshTaskSegmentFacts is called from
-- a session_records write -- OTEL ingest does not trigger it. So a segment's
-- tool_call_count reflects whatever otel_events held at the time of that write, not
-- necessarily every event that will eventually land in this time range. In practice
-- OTEL push outruns periodic JSONL sync, and any lag self-corrects on the session's
-- next session_records write, which recomputes the segment from scratch.
LEFT JOIN LATERAL (
	SELECT count(*) AS tool_call_count,
		count(*) FILTER (WHERE e.tool_success = false) AS tool_fail_count,
		count(*) FILTER (WHERE e.tool_name IN ('Edit', 'Write', 'NotebookEdit')) AS write_calls,
		count(*) FILTER (WHERE e.tool_name IN ('Read', 'Grep', 'Glob')) AS read_calls,
		count(*) FILTER (WHERE e.tool_name = 'Bash') AS bash_calls
	FROM visible_events e
	WHERE e.session_id = b.session_id AND e.event_name = 'tool_result'
	  AND e.ts >= b.start_ts AND (b.end_ts IS NULL OR e.ts < b.end_ts)
) ev ON true
-- Codex exports no OTEL log events (JSONL-first, adr-codex-support.md), so ev is
-- empty for it and its calls come from the JSONL tool_call rows instead. Only the
-- call count: JSONL records no outcome, so tool_fail_count stays 0 and the read
-- path reports it as unobserved (tool_outcome_evidence). The write/read/Bash
-- splits are left out too -- code-mode exec wraps apply_patch and exec_command in
-- one call (#698) -- so activity_type keeps its 'unknown' for Codex.
LEFT JOIN LATERAL (
	SELECT count(*) AS tool_call_count
	FROM visible_session_records t
	WHERE b.agent = 'codex' AND t.session_id = b.session_id AND t.record_type = 'tool_call'
	  AND t.ts >= b.start_ts AND (b.end_ts IS NULL OR t.ts < b.end_ts)
	HAVING b.agent = 'codex'
) jt ON true
-- gjc and omo export no OTEL either, and their calls come from the same pi-style
-- JSONL both harnesses write (internal/sessionview.piView). Every call leaves a
-- 'tool_result' row carrying message.isError, so unlike Codex the outcome is
-- recorded and tool_fail_count is a measurement. gjc also writes a sibling
-- 'tool_call' row per call and omo writes none, so tool_result is the one basis
-- that counts each call exactly once for both.
--
-- The trade-off is deliberate: a call that was interrupted or denied and left no
-- result row is not counted. gjc's tool_call rows would count those exactly, but
-- then gjc's tool_call_count would include calls with no observed outcome, and
-- tool_call_count is the denominator of a failure rate over outcome-observed
-- calls (tool_outcome_observed_count). Counting results keeps it that population.
--
-- activity_type is deliberately left out of this: its CASE reads ev, and the
-- rules other than 'diagnose' need write/read/Bash splits that these harnesses'
-- tool names do not map onto. Feeding only their failures in would make
-- 'diagnose' the sole label they could ever get, so they keep 'unknown' like
-- Codex until the splits are defined.
LEFT JOIN LATERAL (
	SELECT count(*) AS tool_call_count,
		count(*) FILTER (WHERE t.raw->'message'->>'isError' = 'true') AS tool_fail_count
	FROM visible_session_records t
	WHERE b.agent IN ('gjc', 'omo') AND t.session_id = b.session_id AND t.record_type = 'tool_result'
	  AND t.ts >= b.start_ts AND (b.end_ts IS NULL OR t.ts < b.end_ts)
	HAVING b.agent IN ('gjc', 'omo')
) pi ON true`
}

// The per-session refresh runs unprepared and the full rebuild has a text of its
// own, for the reasons pluginInvocationFactsRefreshSQL gives. Here a whole-table
// execution also sorts the whole history into requests, which makes it dearer
// still beside a generic plan, and a prepared refresh likelier to tip as the
// table grows.
var (
	taskSegmentFactsRefreshSQL = taskSegmentFactsInsertSQL("$1::text[]", "$2")
	taskSegmentFactsRebuildSQL = taskSegmentFactsInsertSQL("NULL::text[]", "$1")
)

// taskSegmentTokensCTEs is the WITH entries that count each segment's tokens by
// request (p1t-design §6.2), ending in seg_tokens(boundary_record_id,
// input_tokens, output_tokens) with a row for every segment of segs. segs names
// a relation of segments -- session_id, boundary_record_id, start_ts, end_ts --
// of the sessions sessionFilter names, and covers each session whole;
// sessionFilter is a text[] expression, NULL for every session.
//
// The token rows are syncAttributedRowsSQL's, so a request counts once, at its
// head line, and a Codex usage row with its defended, normalised tokens. Each
// becomes a mark, as does each segment's start, and a row belongs to the last
// segment that starts at or before it -- the range join start_ts <= ts < end_ts
// this replaced, with the ties ordered:
//   - at one instant, segment marks sort before token marks, and segments that
//     start together sort by end, earliest first and an open one last. Of [T, T)
//     and [T, next), a row at T goes to [T, next), and [T, T) counts 0.
//   - a row before the session's first segment belongs to none and is dropped.
//   - input and output are each 0 when NULL, so a line with output and no input
//     still counts its output.
//
// Each segment's own mark falls in its group, so a segment no row falls in still
// gets its row in seg_tokens, with 0. Only rows with input or output become marks:
// the rest add nothing and would only lengthen the sort.
func taskSegmentTokensCTEs(segs, sessionFilter string) string {
	return `heads AS MATERIALIZED (
	SELECT session_id, ts, COALESCE(req_input_tokens, 0) AS input, COALESCE(req_output_tokens, 0) AS output
	FROM (` + syncAttributedRowsSQL(sessionFilter, []string{"session_id", "ts"}) + `) a
	WHERE req_input_tokens <> 0 OR req_output_tokens <> 0
),
marks AS MATERIALIZED (
	SELECT session_id, ts, 1 AS kind, input, output, NULL::bigint AS seg_id, NULL::timestamptz AS seg_end
	FROM heads
	UNION ALL
	SELECT session_id, start_ts, 0, 0, 0, boundary_record_id, end_ts FROM ` + segs + `
),
ordered AS MATERIALIZED (
	SELECT *, count(seg_id) OVER (PARTITION BY session_id
		ORDER BY ts, kind, seg_end NULLS LAST, seg_id ROWS UNBOUNDED PRECEDING) AS grp
	FROM marks
),
assigned AS MATERIALIZED (
	SELECT input, output, max(seg_id) OVER (PARTITION BY session_id, grp) AS seg_id
	FROM ordered
),
seg_tokens AS MATERIALIZED (
	SELECT seg_id AS boundary_record_id, sum(input)::bigint AS input_tokens, sum(output)::bigint AS output_tokens
	FROM assigned
	WHERE seg_id IS NOT NULL
	GROUP BY seg_id
)`
}

// taskSegmentTokensUpdateSQL is step 8 of the P1t transition for the sessions
// $1 names: the token columns of their stored segments recounted by request,
// segment by segment as they stand, and no other column. That is why it is an
// UPDATE and not refreshTaskSegmentFacts: a session whose OTEL has expired would
// have its tool counts and activity type recomputed from nothing.
var taskSegmentTokensUpdateSQL = `
WITH stored AS MATERIALIZED (
	SELECT session_id, boundary_record_id, start_ts, end_ts FROM task_segment_facts
	WHERE session_id = ANY($1::text[])
),
` + taskSegmentTokensCTEs("stored", "$1::text[]") + `
UPDATE task_segment_facts f
SET input_tokens = t.input_tokens, output_tokens = t.output_tokens
FROM seg_tokens t
WHERE f.boundary_record_id = t.boundary_record_id`

// codexSegmentToolCountsBackfill names the one-time recompute of Codex facts built
// before their tool calls were counted from JSONL, which still say 0.
const codexSegmentToolCountsBackfill = "codex_segment_tool_counts_v1"

// BackfillCodexSegmentToolCounts recomputes task segment facts for Codex sessions
// in session_id batches, the way BackfillCodexDeveloperFacts does and for the same
// reason: a full rebuild holds the exclusive fact lock every record write waits on.
func (s *PgStore) BackfillCodexSegmentToolCounts(ctx context.Context) error {
	return s.backfillSegmentToolCounts(ctx, codexSegmentToolCountsBackfill,
		"codex_segment_tool_counts_cursor", []string{"codex"})
}

// piSegmentToolCountsBackfill names the one-time recompute of gjc and omo facts
// built before their tool_result rows were counted, which still say 0.
const piSegmentToolCountsBackfill = "pi_segment_tool_counts_v1"

// BackfillPiSegmentToolCounts does for gjc and omo what
// BackfillCodexSegmentToolCounts does for Codex. It carries its own marker and
// cursor so it replays independently of that pass.
func (s *PgStore) BackfillPiSegmentToolCounts(ctx context.Context) error {
	return s.backfillSegmentToolCounts(ctx, piSegmentToolCountsBackfill,
		"pi_segment_tool_counts_cursor", []string{"gjc", "omo"})
}

// backfillSegmentToolCounts recomputes every fact of the named agents, in
// session_id batches, resuming from its own cursor table and writing its own
// once-marker when the last batch is done. Both are passed in rather than derived
// so each pass stays independently replayable.
func (s *PgStore) backfillSegmentToolCounts(ctx context.Context, marker, cursorTable string, agents []string) error {
	for {
		finished, err := s.segmentToolCountsStep(ctx, marker, cursorTable, agents)
		if err != nil || finished {
			return err
		}
	}
}

func (s *PgStore) segmentToolCountsStep(ctx context.Context, marker, cursorTable string, agents []string) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	var done bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_backfills WHERE name = $1)`,
		marker).Scan(&done); err != nil {
		return false, err
	}
	if done {
		return true, tx.Commit(ctx)
	}
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return false, err
	}
	var after string
	// cursorTable is a package constant, never caller input: identifiers cannot be
	// parameters.
	if err := tx.QueryRow(ctx, `SELECT last_session_id FROM `+cursorTable+` FOR UPDATE`).Scan(&after); err != nil {
		return false, fmt.Errorf("read %s: %w", cursorTable, err)
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT session_id FROM task_segment_facts
		WHERE agent = ANY($1::text[]) AND session_id > $2 ORDER BY session_id LIMIT $3`,
		agents, after, codexDeveloperFactsBatchSize)
	if err != nil {
		return false, fmt.Errorf("list %s segment sessions: %w", marker, err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return false, fmt.Errorf("list %s segment sessions: %w", marker, err)
	}
	if len(ids) == 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO schema_backfills (name) VALUES ($1) ON CONFLICT DO NOTHING`,
			marker); err != nil {
			return false, err
		}
		return true, tx.Commit(ctx)
	}
	if err := refreshTaskSegmentFacts(ctx, tx, ids); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE `+cursorTable+` SET last_session_id = $1`, ids[len(ids)-1]); err != nil {
		return false, fmt.Errorf("advance %s: %w", cursorTable, err)
	}
	return false, tx.Commit(ctx)
}

// segmentToolEvidenceColumns reads, for a task_segment_facts row f, whether its
// tool_call_count and tool_fail_count were measured (TaskSegment.ToolEvidence and
// ToolOutcomeEvidence). A Claude segment's calls and outcomes both come from
// OTEL, so both hold when the session emitted any. A Codex segment's calls come
// from JSONL, which every Codex segment has; JSONL carries no outcome. A gjc or
// omo segment's calls come from its JSONL tool_result rows, which every such
// segment has and which carry isError, so both halves are measured.
const segmentToolEvidenceColumns = `(` + segmentToolCallEvidenceSQL + `),
		(` + segmentToolOutcomeEvidenceSQL + `)`

// segmentToolCallEvidenceSQL and segmentToolOutcomeEvidenceSQL are the two halves
// of segmentToolEvidenceColumns, as boolean expressions over f.
const (
	segmentToolCallEvidenceSQL = `f.agent IN ('codex', 'gjc', 'omo')
			OR EXISTS (SELECT 1 FROM visible_events e WHERE e.session_id = f.session_id)`
	segmentToolOutcomeEvidenceSQL = `f.agent IN ('gjc', 'omo')
			OR (f.agent <> 'codex' AND EXISTS (SELECT 1 FROM visible_events e WHERE e.session_id = f.session_id))`
)

// lateOTELBatch bounds one reconciliation pass. A client that was offline for a
// week uploads its whole backlog at once, and an unbounded pass would hold the
// segment lock for as long as that took.
const lateOTELBatch = 5000

// ReconcileTaskSegmentFactsForLateOTEL recomputes segments whose tool events
// arrived after the session_records write that built them.
//
// A segment's token counts come from session_records and its tool counts from
// otel_events, on separate ingest paths, and only a session_records write
// recomputes the fact. When OTEL lands second the fact keeps saying zero tool
// calls -- and a finished session has no next write, so its last segment stays
// wrong forever. Measured on production: 98 segments had tool events their fact
// did not count, against 7,223 whose session carries no OTEL at all (those are a
// display problem, not this one).
//
// The cursor counts rows rather than timestamps. A client syncing after days
// offline sends events that are old by ts and new by arrival, and a `ts >`
// watermark steps straight over them -- which is the case this exists for.
func (s *PgStore) ReconcileTaskSegmentFactsForLateOTEL(ctx context.Context) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return 0, err
	}

	// max() rather than a WHERE on the seeded row: a restore can leave the table
	// empty, and starting over beats a failed reconciliation.
	var cursor int64
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(max(last_event_id), 0) FROM task_segment_otel_cursor`).Scan(&cursor); err != nil {
		return 0, err
	}

	// One pass over the batch: the sessions to recompute and the id to advance to
	// have to come from the same rows, or a row that arrives between two queries
	// is marked done without being counted.
	rows, err := tx.Query(ctx, `
		WITH batch AS (
			SELECT id, session_id FROM otel_events
			WHERE event_name = 'tool_result' AND id > $1
			ORDER BY id LIMIT $2
		)
		SELECT (SELECT max(id) FROM batch),
		       COALESCE((SELECT array_agg(DISTINCT b.session_id) FROM batch b
		                 WHERE b.session_id <> ''
		                   AND EXISTS (SELECT 1 FROM task_segment_facts f
		                               WHERE f.session_id = b.session_id)), '{}')`,
		cursor, lateOTELBatch)
	if err != nil {
		return 0, err
	}
	var head *int64
	var sessionIDs []string
	if rows.Next() {
		if err := rows.Scan(&head, &sessionIDs); err != nil {
			rows.Close()
			return 0, err
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if head == nil {
		// Nothing new. Leave the cursor where it is.
		return 0, tx.Commit(ctx)
	}

	// The cursor advances even when no session needed recomputing: those rows were
	// examined, and re-examining them next tick would never make progress.
	if len(sessionIDs) > 0 {
		if err := refreshTaskSegmentFacts(ctx, tx, sessionIDs); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO task_segment_otel_cursor (only_row, last_event_id) VALUES (TRUE, $1)
		 ON CONFLICT (only_row) DO UPDATE SET last_event_id = EXCLUDED.last_event_id`, *head); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(sessionIDs), nil
}
