package store

import (
	"context"
	"log"

	"github.com/jackc/pgx/v5"
)

// Codex JSONL carries token counts but no cost, so cost is imputed from
// codex_model_rates. unified_events used to do that at read time: a LATERAL LIKE
// join evaluated once per row across 267k rows, plus a second full scan of
// session_records for a day-level model fallback. That one arm cost as much as the
// entire 2.45M-row view -- 2.6s, paid by every chart and by the session view's nine
// seconds (#245).
//
// claude_imputed_cost had already answered this: make it a table. 167k rows read in
// 7ms there. These functions fill the codex equivalent.

// codexImputedSelect is the imputation itself, shared by both refresh paths so they
// cannot compute different numbers. $1, when not null, restricts the rebuild to one
// set of sessions; the full path passes null.
//
// Two things this deliberately keeps from the old view:
//
//   - input_tokens excludes cache_read_tokens, and cache reads are priced at their
//     own rate. Codex reports the total including cache reads; charging the input
//     rate for them would overstate cost several-fold on cache-heavy sessions.
//   - the rate match is a prefix LIKE ordered by prefix length descending. Model
//     names nest (gpt-5.1-codex-max vs gpt-5.1-codex-mini), so the longest match is
//     the right one; taking any match would price max rows at mini rates.
//   - the rate table is temporal, so the match is also bounded by the event's own
//     date and takes the latest row in force on that date. Prefix length is still
//     the first sort key: a longer prefix is a better model match, and only within
//     one model does recency decide. Rows dated '-infinity' are the baseline that
//     applies until the first documented change.
//
// What it drops is the day-model fallback for rows with an empty model. Those 26
// legacy rows now carry a real model, frozen in by migration -- see migrations.go.
const codexImputedSelect = `
SELECT sr.id, sr.session_id, sr.ts, COALESCE(sr.user_id,''), sr.profile_email, COALESCE(sr.login_email,''),
	COALESCE(sr.model,''),
	(GREATEST(COALESCE(sr.input_tokens,0) - COALESCE(sr.cache_read_tokens,0), 0) * COALESCE(rate.input_rate, 0)
		+ COALESCE(sr.output_tokens,0) * COALESCE(rate.output_rate, 0)
		+ COALESCE(sr.cache_read_tokens,0) * COALESCE(rate.cache_read_rate, 0)) / 1000000.0,
	GREATEST(COALESCE(sr.input_tokens,0) - COALESCE(sr.cache_read_tokens,0), 0),
	COALESCE(sr.output_tokens,0),
	COALESCE(sr.cache_read_tokens,0), COALESCE(sr.cache_create_tokens,0),
	sr.agent, sr.billing_provider,
	-- Carried through so unified_events can attribute a codex row without
	-- re-joining session_records, which would double-count this arm.
	COALESCE(sr.account_id,'')
FROM (
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
		AND ($1::text[] IS NULL OR sr.session_id = ANY($1::text[]))
		AND ($2::bigint IS NULL OR sr.id <= $2::bigint)
) sr
LEFT JOIN LATERAL (
	SELECT input_rate, output_rate, cache_read_rate
	FROM codex_model_rates cmr
	WHERE LOWER(COALESCE(sr.model,'')) LIKE cmr.model_prefix || '%'
		AND cmr.effective_from <= sr.ts::date
	ORDER BY LENGTH(cmr.model_prefix) DESC, cmr.effective_from DESC
	LIMIT 1
) rate ON TRUE
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
)`

const codexImputedColumns = `(srec_id, session_id, ts, user_id, profile_email, login_email, model, cost_usd,
	 input_tokens, output_tokens, cache_read_tokens, cache_create_tokens, agent, billing_provider, account_id)`

// RefreshCodexImputedCost rebuilds the whole table. Runs at boot and periodically.
//
// The incremental path only looks at rows newer than what the table already holds,
// so it cannot see a row that was rewritten in place -- `sync --reenrich` does that,
// and adding a model to codex_model_rates reprices history without touching
// session_records at all. This rebuild is what eventually corrects both.
func (s *PgStore) RefreshCodexImputedCost(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return err
	}
	affected, err := refreshCodexImputedCostTx(ctx, tx)
	if err != nil {
		return err
	}
	if err := refreshSessionOverviewRollups(ctx, tx, affected); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.warnOnUnpricedCodexRows(ctx)
	return nil
}

// refreshCodexImputedCostTx is the rebuild itself, inside a transaction the caller
// owns, returning the sessions whose imputed rows it may have moved so the caller
// can refresh their derived rows.
//
// Taking the shared source-mutation lock is left to the caller. RefreshCodexImputedCost
// takes it immediately above; the #524 repair's completion batch is already holding
// it when it calls in, and re-taking a pg advisory xact lock it owns would be a
// no-op rather than a correctness gain.
func refreshCodexImputedCostTx(ctx context.Context, tx pgx.Tx) ([]string, error) {
	// Every writer takes this row first, so a rebuild and an incremental pass can
	// never be inside the table at the same time. Without it they interleave badly:
	// under read committed a rebuild's DELETE cannot see rows another transaction
	// inserted after that statement began, so it leaves them, and then its INSERT
	// adds them again -- a session materialised twice, its cost doubled on every
	// chart, with no error anywhere. A concurrency test reproduced that within two
	// rounds before this line existed.
	var cursor int64
	if err := tx.QueryRow(ctx, `SELECT last_srec_id FROM codex_imputed_cursor FOR UPDATE`).Scan(&cursor); err != nil {
		return nil, err
	}

	// Read the ceiling before writing and reuse that one value for both the write and
	// the cursor. Re-reading max(id) afterwards would be a second snapshot: a row
	// committed by ordinary sync traffic in between is never inserted, yet the cursor
	// advances past it, so the incremental pass skips it too and that session's cost
	// is missing until the next rebuild.
	var ceiling *int64
	if err := tx.QueryRow(ctx, `SELECT max(id) FROM session_records
		WHERE agent = 'codex' AND record_type = 'usage'`).Scan(&ceiling); err != nil {
		return nil, err
	}
	affected, err := querySessionIDs(ctx, tx, `
		SELECT session_id FROM codex_imputed_cost WHERE session_id <> ''
		UNION
		SELECT session_id FROM session_records
		WHERE agent = 'codex' AND record_type = 'usage' AND session_id <> ''`)
	if err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx, `DELETE FROM codex_imputed_cost`); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO codex_imputed_cost `+codexImputedColumns+codexImputedSelect, nil, ceiling); err != nil {
		return nil, err
	}
	var next int64
	if ceiling != nil {
		next = *ceiling
	}
	if _, err := tx.Exec(ctx, `UPDATE codex_imputed_cursor SET last_srec_id = $1`, next); err != nil {
		return nil, err
	}
	return affected, nil
}

// RefreshCodexImputedCostIncremental rebuilds the sessions that gained source rows
// since the last run, and returns how many rows it wrote.
//
// The cursor is a source-row id held in codex_imputed_cursor, not a timestamp and
// not anything read back out of codex_imputed_cost. Every other candidate was tried
// and each one loses rows:
//
//   - max(ts) of the output: session_records permits timestamp ties (its uniqueness
//     key includes uuid), so a new row sharing the newest timestamp is never `>` it.
//   - any timestamp cursor: rows do not arrive in timestamp order. A client syncing
//     a machine for the first time uploads months of history -- new ids, old
//     timestamps. Two such rows were sitting in prod when this was written.
//   - max(srec_id) of the output: duplicate token snapshots are dropped and never
//     materialised, so the cursor would park behind one and rebuild that session
//     every 10 seconds for as long as the row existed.
//
// Whole sessions are recomputed, not individual rows: the dedup test compares a row
// against the previous row of the same session (LAG), so a session refreshed
// piecewise would judge its first new row against nothing.
//
// Residual: ids are assigned before commit, so a transaction that takes an id below
// the ceiling read here but commits after it is missed until the periodic rebuild.
// That rebuild exists for this and for rows rewritten in place.
func (s *PgStore) RefreshCodexImputedCostIncremental(ctx context.Context) (int, error) {
	// Cheap gate, taken by almost every tick: this runs every 10 seconds and new
	// codex rows arrive a few hundred times a day. Deliberately outside the
	// transaction and without the lock -- an idle tick should not queue behind a
	// rebuild that is mid-write. idx_srec_codex_usage_id keeps it off the full table.
	var cursor int64
	if err := s.pool.QueryRow(ctx, `SELECT last_srec_id FROM codex_imputed_cursor`).Scan(&cursor); err != nil {
		return 0, err
	}
	var pending bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM session_records
		WHERE agent = 'codex' AND record_type = 'usage' AND id > $1)`, cursor).Scan(&pending); err != nil {
		return 0, err
	}
	if !pending {
		return 0, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return 0, err
	}

	// Same row the rebuild takes, so the two can never be inside the table together.
	// The cursor is re-read here rather than trusted from the gate above: a rebuild
	// may have committed in between and already covered this range.
	if err := tx.QueryRow(ctx, `SELECT last_srec_id FROM codex_imputed_cursor FOR UPDATE`).Scan(&cursor); err != nil {
		return 0, err
	}
	var ceiling *int64
	if err := tx.QueryRow(ctx, `SELECT max(id) FROM session_records
		WHERE agent = 'codex' AND record_type = 'usage' AND id > $1`, cursor).Scan(&ceiling); err != nil {
		return 0, err
	}
	if ceiling == nil {
		return 0, tx.Commit(ctx)
	}

	// Bounded by the ceiling read above so that a row arriving mid-transaction is
	// left for the next tick rather than being skipped by an advanced cursor.
	rows, err := tx.Query(ctx, `SELECT DISTINCT session_id FROM session_records
		WHERE agent = 'codex' AND record_type = 'usage' AND id > $1 AND id <= $2`, cursor, *ceiling)
	if err != nil {
		return 0, err
	}
	var sessions []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		sessions = append(sessions, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	written := 0
	if len(sessions) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM codex_imputed_cost WHERE session_id = ANY($1::text[])`, sessions); err != nil {
			return 0, err
		}
		// No id ceiling here: the whole session is recomputed, including rows past the
		// ceiling, so the table is never left short. Those rows are simply considered
		// again on the next tick, which is idempotent.
		tag, err := tx.Exec(ctx, `INSERT INTO codex_imputed_cost `+codexImputedColumns+codexImputedSelect, sessions, nil)
		if err != nil {
			return 0, err
		}
		written = int(tag.RowsAffected())
	}

	// The cursor advances to the ceiling even when nothing was written -- every row
	// up to it has been considered, and some were deliberately dropped.
	if _, err := tx.Exec(ctx, `UPDATE codex_imputed_cursor SET last_srec_id = $1`, *ceiling); err != nil {
		return 0, err
	}
	if err := refreshSessionOverviewRollups(ctx, tx, sessions); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	if written > 0 {
		s.warnOnUnpricedCodexRows(ctx)
	}
	return written, nil
}

// warnOnUnpricedCodexRows reports rows that priced to zero because no rate matched.
//
// The day-model fallback used to cover rows with an empty model; it was removed
// because it served 26 legacy rows at 514ms per query. If a client ever starts
// writing empty models again, or a model ships that no rate prefix matches, those
// rows silently cost $0 -- and a cost that is quietly wrong is worse than one that
// is obviously missing. This is the line that makes it noisy.
func (s *PgStore) warnOnUnpricedCodexRows(ctx context.Context) {
	var n int64
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM codex_imputed_cost WHERE cost_usd = 0 AND (input_tokens > 0 OR output_tokens > 0)`,
	).Scan(&n); err != nil {
		return
	}
	if n > 0 {
		log.Printf("[codex-imputed] %d row(s) priced at $0 -- no codex_model_rates prefix matched their model", n)
	}
}
