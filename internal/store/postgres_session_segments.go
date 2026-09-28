package store

import (
	"context"
	"time"
)

// SessionAccountSegment is one contiguous stretch of a session that belonged to
// a single account.
type SessionAccountSegment struct {
	// Account is the login email (Claude) or account uuid (Codex). Empty means
	// the rows in this stretch carry no account identity -- they predate the
	// account observation log and were never backfillable.
	Account      string    `json:"account"`
	Agent        string    `json:"agent"`
	StartTime    time.Time `json:"start_time"`
	EndTime      time.Time `json:"end_time"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	CostUSD      float64   `json:"cost_usd"`
	EventCount   int64     `json:"event_count"`
}

// SessionAccountSegments breaks one session into the stretches its records
// belonged to each account, in order.
//
// The session list deliberately shows one row per session -- a session is one
// conversation, and splitting the list row would break its identity, its
// duration and its link. This is the drill-down where the split is readable:
// which account held which stretch, and what each spent.
//
// Returning to an earlier account is a new segment rather than a merge with the
// first one. Grouping by account alone would collapse them and lose the ordering
// this exists to show, which is why the grouping is over runs, not values.
//
// Rows with no account identity form their own segment instead of being dropped
// or folded into a neighbour. The gap is a fact about the session, and hiding it
// would make the segments silently fail to sum to the session's totals.
func (s *PgStore) SessionAccountSegments(ctx context.Context, sessionID string) ([]*SessionAccountSegment, error) {
	if sessionID == "" {
		return nil, nil
	}

	// Account identity is per agent: Codex has no Anthropic login, so its
	// identity is the account uuid, while Claude's is the login email. Reading
	// both columns unconditionally would split one Claude account into two,
	// because OTEL rows carry only the email and synced rows may carry both.
	// The two sources are alternatives, not addends -- the same precedence
	// ListSessionOverviews uses. unified_events already surfaces the codex
	// session_records as usage rows, so adding session_records unconditionally
	// would count every codex row twice, inflating the totals and doubling the
	// segment count as the duplicate copies alternate account identity. Falling
	// back to session_records only when the session has no unified_events row is
	// what keeps offline, JSONL-only Claude sessions visible here at all.
	const q = `
	WITH ev AS (
		SELECT ts, COALESCE(agent,'claude') AS agent,
			NULLIF(CASE WHEN COALESCE(agent,'claude') = 'codex'
			            THEN COALESCE(account_id,'') ELSE COALESCE(login_email,'') END, '') AS account,
			COALESCE(input_tokens,0) AS input_tokens,
			COALESCE(output_tokens,0) AS output_tokens,
			COALESCE(cost_usd,0)::double precision AS cost_usd
		FROM visible_events WHERE session_id = $1
	), sr AS (
		SELECT ts, COALESCE(agent,'claude') AS agent,
			NULLIF(CASE WHEN COALESCE(agent,'claude') = 'codex'
			            THEN COALESCE(account_id,'') ELSE COALESCE(login_email,'') END, '') AS account,
			COALESCE(input_tokens,0) AS input_tokens,
			COALESCE(output_tokens,0) AS output_tokens,
			0::double precision AS cost_usd
		FROM visible_session_records
		WHERE session_id = $1
			-- A timestamp-less state record has no place on a timeline: ordered at the
			-- zero instant it opened a leading 1970 segment with no account, and the
			-- session list would then disagree with its own drill-down (see
			-- sessionStartExpr for where that ts comes from). Dropping it does not break
			-- the promise that the segments sum to the session: state records carry no
			-- tokens and no cost, only a place in the record count.
			--
			-- The NOT EXISTS is the same fallback sessionStartExpr makes -- a session
			-- whose rows are all state records keeps them, because an empty drill-down
			-- would say the session has no records at all.
			AND (ts > 'epoch' OR NOT EXISTS (
				SELECT 1 FROM visible_session_records real_ts
				WHERE real_ts.session_id = $1 AND real_ts.ts > 'epoch'))
	), src AS (
		SELECT * FROM ev
		UNION ALL
		SELECT * FROM sr WHERE NOT EXISTS (SELECT 1 FROM ev)
	),
	marked AS (
		SELECT *,
			CASE WHEN account IS DISTINCT FROM
			          LAG(account) OVER (ORDER BY ts, account NULLS FIRST)
			     THEN 1 ELSE 0 END AS starts_run
		FROM src
	),
	runs AS (
		SELECT *,
			SUM(starts_run) OVER (ORDER BY ts, account NULLS FIRST
			                      ROWS UNBOUNDED PRECEDING) AS run_id
		FROM marked
	)
	SELECT COALESCE(MAX(account), '') AS account,
		COALESCE(MAX(agent), 'claude') AS agent,
		MIN(ts), MAX(ts),
		SUM(input_tokens), SUM(output_tokens), SUM(cost_usd), COUNT(*)
	FROM runs
	GROUP BY run_id
	ORDER BY MIN(ts), run_id`

	rows, err := s.pool.Query(ctx, q, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*SessionAccountSegment, 0)
	for rows.Next() {
		seg := &SessionAccountSegment{}
		if err := rows.Scan(&seg.Account, &seg.Agent, &seg.StartTime, &seg.EndTime,
			&seg.InputTokens, &seg.OutputTokens, &seg.CostUSD, &seg.EventCount); err != nil {
			return nil, err
		}
		result = append(result, seg)
	}
	return result, rows.Err()
}
