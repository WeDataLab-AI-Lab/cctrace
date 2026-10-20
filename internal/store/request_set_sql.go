package store

import (
	"fmt"
	"strings"
)

// The request set (p1t-design §2). Claude Code writes one API response as several
// JSONL lines -- 1.95 to 2.51 per response on production, by month -- and every
// line repeats the response's usage, so a token sum over lines counts a response
// two or three times. The fragments below fold a response's lines into one
// request, and give every other row the one token rule of p1t-design §2.2.
//
// Everything they are given is SQL written in this package: the alias, the source
// table, the caller's predicate, the column names and the filter expressions. A
// caller's values travel as $n placeholders inside those, never formatted into the
// text.

// claudeRequestLineSQL is "this row is a line of a Claude API request", as SQL over
// a session_records row aliased to alias.
//
// It is the one predicate for both questions the request set asks: which lines are
// grouped into requests, and which rows are request lines at all. Two predicates
// that disagree on a <synthetic> line split a request -- the line sits in the
// response's partition, where it can take the head position or the MAX, while the
// line test leaves it out (p1t critic). <synthetic> lines are not requests and pass
// through as they are.
func claudeRequestLineSQL(alias string) string {
	return fmt.Sprintf(`(%[1]s.agent = 'claude' AND %[1]s.record_type = 'assistant'
		AND %[1]s.model IS DISTINCT FROM '<synthetic>')`, alias)
}

// claudeRequestKeySQL is the request a line of claudeRequestLineSQL belongs to
// within its session: its API message id, or the line itself when it has none.
//
// message_id (P1t-1) comes before raw, and COALESCE stops at the first operand that
// is not NULL, so a filled row never reads raw. NULL is a row the backfill has not
// reached; it falls back to the same id in raw, so a read before, during or after
// the backfill sees the same requests. The empty string is a row filled with no id:
// NULLIF sends it, like a raw without one, to the row's own key. No line has been
// without an id across the whole history, and folding every id-less line of a
// session into one request would be the wrong guess for any that appear.
func claudeRequestKeySQL(alias string) string {
	return fmt.Sprintf(`COALESCE(NULLIF(COALESCE(%[1]s.message_id, %[1]s.raw->'message'->>'id'), ''),
		'row:' || %[1]s.id::text)`, alias)
}

// claudeRequestWindowSQL is the request lines of source that pass lineWhere, each
// with cols, req_key (claudeRequestKeySQL), req_line -- its place in its request by
// ts, then an empty uuid last, then id -- and its request's MAX of each token field.
// Both shapes below read this one subquery, so they cannot pick different heads,
// and their token sums agree.
//
// One window, so one WindowAgg over the one sort. Its frame is the whole
// partition: row_number() ignores a frame, and max() over the whole partition is
// the request's MAX, as it was over a window with no ORDER BY. Two windows -- the
// ordered one for row_number() and a bare partition for max() -- cost a second
// WindowAgg pass over every request line (P1t-8: 72 ms of 196 ms on seven days).
func claudeRequestWindowSQL(source, lineWhere string, cols []string) string {
	return fmt.Sprintf(`SELECT %[1]s
		%[5]s AS req_key,
		row_number() OVER w AS req_line,
		max(sr.input_tokens) OVER w AS req_input_tokens,
		max(sr.output_tokens) OVER w AS req_output_tokens,
		max(sr.cache_read_tokens) OVER w AS req_cache_read_tokens,
		max(sr.cache_create_tokens) OVER w AS req_cache_create_tokens
	FROM %[2]s sr
	WHERE %[3]s AND (%[4]s)
	WINDOW w AS (PARTITION BY sr.session_id, %[5]s ORDER BY sr.ts, (sr.uuid = ''), sr.id
		ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING)`,
		selectColumnsSQL("sr", cols), source, claudeRequestLineSQL("sr"), lineWhere, claudeRequestKeySQL("sr"))
}

// claudeRequestHeadsSQL is one row per request -- (session_id, claudeRequestKeySQL)
// over the lines of source that pass lineWhere -- for a FROM clause: cols from the
// request's head line, req_key, then input_tokens, output_tokens, cache_read_tokens
// and cache_create_tokens, each the MAX of that field over the request's lines.
//
// The head is the first line by ts, then an empty uuid last, then id, so identity,
// project, model and time are the first line's. Of twins -- one line stored twice
// in the same slot, once with an empty uuid and once with its uuid -- the head is
// the one with the uuid.
//
// MAX per field, not the head's own counts: the lines of one response can carry
// output that was still growing -- 1, 1, then 523 -- and the response's output is
// the largest. Where lines under one key disagree on input, cache_read or model --
// 122 groups across the history, none with a <synthetic> line -- the fold still
// takes each field's MAX, which undercounts if they were separate requests. That is
// pinned by a test and reported as a diagnostic (p1t-design §2.2, §9).
//
// The window runs over request lines only, after lineWhere: a request whose lines
// straddle a period boundary counts in both periods (p1t-design §2.4).
//
// source is a table or view with session_records' columns. visible_session_records
// already hides deleted sessions and excluded accounts; session_records does not, so
// a caller reading it puts the deleted-session exclusion in lineWhere. cols are
// column names of source; the four token columns are not among them, since they are
// what this computes. lineWhere is a predicate, "TRUE" for every line, and three
// rules hold for it here and in attributedRowsSQL:
//
//   - Of the fragment's own names it sees only the source row, as sr; any other
//     alias it names is the caller's. An outer alias named sr is shadowed, so
//     lineWhere cannot mean one. Qualify its columns with sr. for the reader.
//   - Period and identity predicates belong in it. Outside it, the planner pushes
//     only a predicate on session_id -- a partition column -- below the window;
//     anything else waits until the whole history is sorted, and changes the answer
//     as well, since the requests were folded from lines it would have dropped.
//   - A lineWhere correlated with an outer row sorts the window again for every
//     outer row. Build the fragment once over a set of sessions and join it.
func claudeRequestHeadsSQL(source, lineWhere string, cols []string) string {
	return fmt.Sprintf(`SELECT %[1]s
		q.req_key,
		q.req_input_tokens AS input_tokens, q.req_output_tokens AS output_tokens,
		q.req_cache_read_tokens AS cache_read_tokens, q.req_cache_create_tokens AS cache_create_tokens
	FROM (%[2]s) q
	WHERE q.req_line = 1`,
		selectColumnsSQL("q", cols), claudeRequestWindowSQL(source, lineWhere, cols))
}

// attributedRowsSQL is every row of source that passes lineWhere, for a FROM clause:
// cols, req_key -- the request's key on every line of a request, head or not, and
// NULL on every other row; a key within its session, since one message id recurs
// across sessions, so join on (session_id, req_key) -- then req_input_tokens,
// req_output_tokens, req_cache_read_tokens and req_cache_create_tokens under the one
// rule of p1t-design §2.2:
//
//   - a request line (claudeRequestLineSQL): its request's MAX on the head line, 0
//     on the request's other lines. The head is the one claudeRequestHeadsSQL
//     returns, so this shape and that one sum to the same tokens.
//   - a Codex usage row: its codex_imputed_cost row, joined on srec_id -- input net
//     of cache reads, and behind the whole-session defences of codexUsageRowsSQL --
//     or 0 when there is none: a mirror or a repeated snapshot the imputation
//     dropped, or a row its refresh has not reached yet. Joined, not recomputed: the
//     defences compare a row with the rows before it in its session, and lineWhere
//     may have cut those away.
//   - any other Codex row: 0.
//   - every other row -- Claude's other rows, <synthetic> lines, gjc, omo, agents
//     added later: its own counts, NULL included. A row the request-line predicate
//     answers NULL for is one of these (IS NOT TRUE, not NOT), so a source that
//     carries NULLs cannot drop a row from both arms.
//
// For read paths only. Because of that join, a fact written at sync time -- task
// segment tokens, plugin facts (p1t-design §2.1 rows 2 and 6) -- would freeze a
// Codex row the refresh has not reached yet at 0; those read
// syncAttributedRowsSQL instead, and so does the plugin raw query, which stands
// in for the plugin facts until they are built and gives their numbers.
//
// Only cols, req_key and those four columns leave the fragment; raw is read by the
// request key and goes no further. source, lineWhere and cols are as for
// claudeRequestHeadsSQL.
//
// Three arms, one per kind of row, each applying lineWhere to the source itself,
// so lineWhere sees the same scope in all three. A request line is a Claude row,
// so it is never a Codex usage row, and the middle arm takes every other row by
// IS NOT TRUE on both predicates, so each row comes out once. Only the Codex usage
// arm meets codex_imputed_cost: joined to every row, it hashed the whole table or
// probed its index once per row, for rows that can never match (P1t-8: 39,535
// probes for one user's week, 654,108 rows hashed for one user's 100 sessions).
//
// The window arm has no parallel plan, and neither has a UNION ALL holding it, so
// every row of this fragment reaches the one process that runs the window. A read
// that only sums tokens over many rows takes attributedLinesSQL and
// claudeRequestHeadsSQL instead.
func attributedRowsSQL(source, lineWhere string, cols []string) string {
	return fmt.Sprintf(`SELECT %[1]s
		a.req_key,
		CASE WHEN a.req_line = 1 THEN a.req_input_tokens ELSE 0 END AS req_input_tokens,
		CASE WHEN a.req_line = 1 THEN a.req_output_tokens ELSE 0 END AS req_output_tokens,
		CASE WHEN a.req_line = 1 THEN a.req_cache_read_tokens ELSE 0 END AS req_cache_read_tokens,
		CASE WHEN a.req_line = 1 THEN a.req_cache_create_tokens ELSE 0 END AS req_cache_create_tokens
	FROM (%[2]s) a
	UNION ALL
	SELECT %[3]s
		NULL::text,
		CASE WHEN sr.agent = 'codex' THEN 0 ELSE sr.input_tokens END,
		CASE WHEN sr.agent = 'codex' THEN 0 ELSE sr.output_tokens END,
		CASE WHEN sr.agent = 'codex' THEN 0 ELSE sr.cache_read_tokens END,
		CASE WHEN sr.agent = 'codex' THEN 0 ELSE sr.cache_create_tokens END
	FROM %[4]s sr
	WHERE %[5]s IS NOT TRUE AND %[6]s IS NOT TRUE AND (%[7]s)
	UNION ALL
	%[8]s`,
		selectColumnsSQL("a", cols), claudeRequestWindowSQL(source, lineWhere, cols),
		selectColumnsSQL("sr", cols), source, claudeRequestLineSQL("sr"), codexUsageRowSQL, lineWhere,
		codexUsageAttributedSQL(source, lineWhere, selectColumnsSQL("sr", cols)+"NULL::text,"))
}

// attributedLinesSQL is every row of attributedRowsSQL -- the same rows, with the
// same cols -- but every request line carries 0 in its four req_ token columns,
// the head too. A request's tokens are claudeRequestHeadsSQL's over the same
// source and lineWhere, which a caller adds wherever it sums tokens: for any
// grouping over the head's columns, this fragment's sum plus the heads' sum is
// attributedRowsSQL's sum. That holds reading NULL as 0, and only so: a request
// with no count in a field is NULL at its head in attributedRowsSQL and among
// the heads, and 0 on every line here, so one side's sum can be NULL where the
// other's is 0. Columns are cols, then the four req_ token columns; no req_key.
//
// It is for a read that sums tokens over many rows. Without the window it has a
// parallel plan, and so does a UNION ALL it sits in, so a read made of a union of
// sources keeps the shape it had before the request set: one Gather over every
// source, the scans and projections in the workers. The window then runs once,
// over request lines alone and narrow columns, in claudeRequestHeadsSQL. With
// attributedRowsSQL as one arm, the 30-day session list's union became a serial
// Append whose rows the leader projected one by one: in the P1t-8 rehearsal its
// leader time collecting the union rose from 0.09 s to 1.30 s, materialising it
// from 0.93 s to 3.85 s, and the list from 4.27 s to 7.20 s. A union of sources
// takes attributedLinesUnionSQL, which places these arms beside its own.
func attributedLinesSQL(source, lineWhere string, cols []string) string {
	arms := attributedLinesArmsSQL(source, lineWhere, cols)
	return arms[0] + "\n\tUNION ALL\n\t" + arms[1]
}

// attributedLinesUnionSQL is SELECT sel FROM (attributedLinesSQL(source,
// lineWhere, cols)) sr WHERE where, written as one SELECT per arm joined by UNION
// ALL, for a caller that places the rows in a UNION ALL of its own. The arms then
// stand beside the caller's other arms, as the plain scan of session records did
// before the request set, rather than inside one subquery with its own UNION
// ALL. Locally that kept the planner's single Gather over all the sources where
// the nested union lost it: a user's 30-day list ran in 0.59 s spliced, 0.76 s
// nested.
//
// sel and where are SQL over the arm's columns -- cols and the four req_ token
// columns -- named bare or as sr.<column>; where is "" for none. The rows are
// attributedLinesSQL's rows, each once.
//
// A caller with no arm of its own takes it for sel: what sel computes is
// computed in each arm, so a row leaves the arm as narrow as the caller reads
// it. An aggregate over attributedLinesSQL as a subquery computes its argument
// itself, from columns carried up to it.
func attributedLinesUnionSQL(source, lineWhere string, cols []string, sel, where string) string {
	if where != "" {
		where = "\n\tWHERE " + where
	}
	arms := attributedLinesArmsSQL(source, lineWhere, cols)
	return "SELECT " + sel + "\n\tFROM (" + arms[0] + ") sr" + where +
		"\n\tUNION ALL\n\tSELECT " + sel + "\n\tFROM (" + arms[1] + ") sr" + where
}

// attributedLinesArmsSQL is attributedLinesSQL's two arms: every row but a Codex
// usage row, and the Codex usage rows. The first takes the rows the Codex usage
// predicate does not answer TRUE for, so a row it answers NULL for stays in it.
func attributedLinesArmsSQL(source, lineWhere string, cols []string) [2]string {
	return [2]string{
		fmt.Sprintf(`SELECT %[1]s
		CASE WHEN %[2]s OR sr.agent = 'codex' THEN 0 ELSE sr.input_tokens END AS req_input_tokens,
		CASE WHEN %[2]s OR sr.agent = 'codex' THEN 0 ELSE sr.output_tokens END AS req_output_tokens,
		CASE WHEN %[2]s OR sr.agent = 'codex' THEN 0 ELSE sr.cache_read_tokens END AS req_cache_read_tokens,
		CASE WHEN %[2]s OR sr.agent = 'codex' THEN 0 ELSE sr.cache_create_tokens END AS req_cache_create_tokens
	FROM %[3]s sr
	WHERE %[4]s IS NOT TRUE AND (%[5]s)`,
			selectColumnsSQL("sr", cols), claudeRequestLineSQL("sr"), source, codexUsageRowSQL, lineWhere),
		codexUsageAttributedSQL(source, lineWhere, selectColumnsSQL("sr", cols)),
	}
}

// codexUsageRowSQL is "this row is a Codex usage row", over the source row sr.
const codexUsageRowSQL = `(sr.agent = 'codex' AND sr.record_type = 'usage')`

// codexUsageAttributedSQL is the Codex usage rows of source that pass lineWhere,
// each with lead -- a select list over sr, ending in a comma -- then the four
// req_ token columns of its codex_imputed_cost row, or 0 when it has none.
// lineWhere is applied to the source before the join, so it cannot see the
// imputed row.
func codexUsageAttributedSQL(source, lineWhere, lead string) string {
	return fmt.Sprintf(`SELECT %[1]s
		COALESCE(rs_cic.input_tokens, 0) AS req_input_tokens,
		COALESCE(rs_cic.output_tokens, 0) AS req_output_tokens,
		COALESCE(rs_cic.cache_read_tokens, 0) AS req_cache_read_tokens,
		COALESCE(rs_cic.cache_create_tokens, 0) AS req_cache_create_tokens
	FROM (SELECT * FROM %[2]s sr WHERE %[3]s AND (%[4]s)) sr
	LEFT JOIN codex_imputed_cost rs_cic ON rs_cic.srec_id = sr.id`,
		lead, source, codexUsageRowSQL, lineWhere)
}

// syncAttributedRowsSQL is attributedRowsSQL for a fact written at sync time --
// task segment tokens and plugin facts (p1t-design §2.1 rows 2 and 6): every row
// of the sessions sessionFilter names, for a FROM clause, with cols, req_key and
// the four req_ token columns under the same rule, but for a Codex usage row.
// Its callers are taskSegmentTokensCTEs (the task segment INSERT and step 8's
// UPDATE), pluginInvocationFactsInsertSQL, and pluginUsageRawQuery, which stands
// in for the plugin facts until they are built and so reads what they read.
//
// That row reads codexUsageRowsSQL, not codex_imputed_cost: the imputed table's
// refresh runs on its own tick, and a fact written before it reaches a row keeps
// the 0 it read until the session is written again. So a Codex usage row counts
// what codexImputedSelect would price -- input net of cache reads, the other
// fields their own, NULL as 0 -- and 0 when the whole-session defences drop it.
// They compare a row with the rows before it in its session, which is why the
// filter names sessions, never a period: a caller that wants part of a session
// filters the result.
//
// The source is session_records, deleted sessions excluded. sessionFilter is a
// text[] expression, NULL for every session, as for codexUsageRowsSQL; cols are
// as for attributedRowsSQL. The filter is applied before the request window, so
// a session it leaves out is never sorted into requests.
//
// sessionFilter has to be a value the planner sees: a parameter, or a NULL
// constant. Each arm reads (filter IS NULL OR session_id = ANY(filter)); a
// subquery there is an InitPlan, which the planner can neither fold nor use as an
// index condition, so every arm scans all of session_records, a sync's refresh of
// one session included. A statement run both with a list and with NULL needs a
// text for each (pluginInvocationFactsRefreshSQL says why). pluginUsageRawQuery
// is the one caller that passes a subquery, and scans so: it serves only until
// the plugin facts are built.
func syncAttributedRowsSQL(sessionFilter string, cols []string) string {
	lineWhere := fmt.Sprintf(`NOT EXISTS (SELECT 1 FROM deleted_sessions d WHERE d.session_id = sr.session_id)
		AND (%[1]s IS NULL OR sr.session_id = ANY(%[1]s))`, sessionFilter)
	return fmt.Sprintf(`SELECT %[1]s
		a.req_key,
		CASE WHEN a.req_line = 1 THEN a.req_input_tokens ELSE 0 END AS req_input_tokens,
		CASE WHEN a.req_line = 1 THEN a.req_output_tokens ELSE 0 END AS req_output_tokens,
		CASE WHEN a.req_line = 1 THEN a.req_cache_read_tokens ELSE 0 END AS req_cache_read_tokens,
		CASE WHEN a.req_line = 1 THEN a.req_cache_create_tokens ELSE 0 END AS req_cache_create_tokens
	FROM (%[2]s) a
	UNION ALL
	SELECT %[3]s
		NULL::text,
		CASE WHEN sr.agent = 'codex' AND sr.record_type = 'usage'
				THEN GREATEST(COALESCE(rs_cu.input_tokens, 0) - COALESCE(rs_cu.cache_read_tokens, 0), 0)
			WHEN sr.agent = 'codex' THEN 0 ELSE sr.input_tokens END,
		CASE WHEN sr.agent = 'codex' AND sr.record_type = 'usage' THEN COALESCE(rs_cu.output_tokens, 0)
			WHEN sr.agent = 'codex' THEN 0 ELSE sr.output_tokens END,
		CASE WHEN sr.agent = 'codex' AND sr.record_type = 'usage' THEN COALESCE(rs_cu.cache_read_tokens, 0)
			WHEN sr.agent = 'codex' THEN 0 ELSE sr.cache_read_tokens END,
		CASE WHEN sr.agent = 'codex' AND sr.record_type = 'usage' THEN COALESCE(rs_cu.cache_create_tokens, 0)
			WHEN sr.agent = 'codex' THEN 0 ELSE sr.cache_create_tokens END
	FROM (SELECT * FROM session_records sr WHERE %[4]s IS NOT TRUE AND (%[5]s)) sr
	LEFT JOIN (%[6]s) rs_cu
		ON rs_cu.id = sr.id AND sr.agent = 'codex' AND sr.record_type = 'usage'`,
		selectColumnsSQL("a", cols), claudeRequestWindowSQL("session_records", lineWhere, cols),
		selectColumnsSQL("sr", cols), claudeRequestLineSQL("sr"), lineWhere,
		codexUsageRowsSQL(sessionFilter, "NULL::bigint"))
}

// selectColumnsSQL is cols qualified by alias, each followed by a comma, to open a
// select list. Empty when cols is.
func selectColumnsSQL(alias string, cols []string) string {
	var b strings.Builder
	for _, c := range cols {
		b.WriteString(alias + "." + c + ", ")
	}
	return b.String()
}

// codexUsageRowsSQL is the Codex usage rows of session_records that count, for a
// FROM clause: every column of the row and the window columns its defences read,
// without the rows those defences drop -- a token_count event mirroring a
// token_usage_record, and a cumulative snapshot that repeats the one before it.
// codex_imputed_cost is built on it (codexImputedSelect), and so is any count of
// Codex usage taken where that table is not joined.
//
// The token columns are the row's own: input still includes cache reads. A caller
// normalises as codexImputedSelect does, GREATEST(input - cache_read, 0).
//
// sessionFilter is a text[] expression naming the sessions to read, NULL for all;
// idCeiling is a bigint expression bounding the row id, NULL for none. Placeholders
// such as $1::text[], not values.
//
// The defences compare each row with the rows before it in its session, so the
// window has to see sessions whole: the filter selects sessions, never a period. A
// caller that wants a period filters the result.
func codexUsageRowsSQL(sessionFilter, idCeiling string) string {
	return fmt.Sprintf(`
SELECT sr.* FROM (
	SELECT
		sr.*,
		sr.raw #>> '{payload,info,total_token_usage,input_tokens}' AS total_usage_input_tokens,
		sr.raw #>> '{payload,info,total_token_usage,cached_input_tokens}' AS total_usage_cached_input_tokens,
		sr.raw #>> '{payload,info,total_token_usage,output_tokens}' AS total_usage_output_tokens,
		LAG(sr.raw #>> '{payload,info,total_token_usage,input_tokens}') OVER (
			PARTITION BY sr.session_id, sr.profile_email ORDER BY sr.ts, sr.id
		) AS prev_total_usage_input_tokens,
		LAG(sr.raw #>> '{payload,info,total_token_usage,cached_input_tokens}') OVER (
			PARTITION BY sr.session_id, sr.profile_email ORDER BY sr.ts, sr.id
		) AS prev_total_usage_cached_input_tokens,
		LAG(sr.raw #>> '{payload,info,total_token_usage,output_tokens}') OVER (
			PARTITION BY sr.session_id, sr.profile_email ORDER BY sr.ts, sr.id
		) AS prev_total_usage_output_tokens,
		-- When did this session first write a token_usage_record? Same partition as
		-- the LAG above, and exact on both paths: the full rebuild passes no session
		-- filter, and the incremental path recomputes whole sessions rather than
		-- individual rows, so the window always sees the session's entire history.
		MIN(sr.ts) FILTER (WHERE sr.raw->>'type' = 'token_usage_record') OVER (
			PARTITION BY sr.session_id, sr.profile_email
		) AS first_token_usage_record_ts
	FROM session_records sr
	WHERE sr.agent = 'codex' AND sr.record_type = 'usage'
		AND NOT EXISTS (SELECT 1 FROM deleted_sessions d WHERE d.session_id = sr.session_id)
		AND (%[1]s IS NULL OR sr.session_id = ANY(%[1]s))
		AND (%[2]s IS NULL OR sr.id <= %[2]s)
) sr
-- A token_count event that follows a token_usage_record is that record's mirror.
-- Counting both doubles the session, and on a resumed session it does far worse:
-- thread_token_usage restarts at the resume while total_token_usage keeps counting
-- the whole file, so the gap is emitted as usage. One production session turned
-- eleven minutes into 1.49 billion input tokens and $24,572 (#685).
--
-- The client stopped emitting these in v0.7.51, and this guard exists anyway. The
-- fleet is never uniform: one account was observed writing from v0.7.41 and v0.7.50
-- at the same time for thirty hours, because a binary replaced on disk left a
-- resident process running (#623). A server that trusts every client to be current
-- is trusting something no deploy can make true.
--
-- Events *before* the first record still count. The client's own guard turns on when
-- it reads that line, so a file that opened with token_count events and only later
-- started writing records had those early events counted legitimately.
-- COALESCE, not a bare comparison: a token_usage_record line carries no
-- payload.type, so the operand is NULL, comparing NULL to a string yields NULL, and
-- NOT(NULL) is NULL -- which excludes the row. The first version of this guard
-- dropped the very records it was written to protect.
WHERE NOT (
	COALESCE(sr.raw #>> '{payload,type}', '') = 'token_count'
	AND sr.first_token_usage_record_ts IS NOT NULL
	AND sr.ts >= sr.first_token_usage_record_ts
)
AND NOT (
	sr.total_usage_input_tokens IS NOT NULL
	AND sr.total_usage_cached_input_tokens IS NOT NULL
	AND sr.total_usage_output_tokens IS NOT NULL
	AND sr.prev_total_usage_input_tokens IS NOT NULL
	AND sr.prev_total_usage_cached_input_tokens IS NOT NULL
	AND sr.prev_total_usage_output_tokens IS NOT NULL
	AND sr.total_usage_input_tokens = sr.prev_total_usage_input_tokens
	AND sr.total_usage_cached_input_tokens = sr.prev_total_usage_cached_input_tokens
	AND sr.total_usage_output_tokens = sr.prev_total_usage_output_tokens
)`, sessionFilter, idCeiling)
}
