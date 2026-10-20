package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// claudeRateAt resolves one row's $/weighted-token, in preference order, as a
// correlated subquery per candidate so COALESCE stops at the first hit.
//
//  1. the week the row falls in
//  2. the nearest earlier week, then the nearest later week -- a model billed
//     in June prices a March row with June's scale, which is the closest thing
//     to an observation that exists for a month with no OTEL at all
//  3. Anthropic's published price, for a model never billed here
//
// A model that matches none of these gets no cost rather than a guessed one:
// local and third-party models run through Claude Code (ollama, kimi, qwen) are
// not Anthropic-priced, and inventing a number for them is worse than the gap
// (#441).
const claudeRateAt = `
(SELECT b.scale FROM model_rate_buckets b
  WHERE b.model = sr.model AND b.bucket_start <= sr.ts::date
  ORDER BY b.bucket_start DESC LIMIT 1)`

const claudeRateBack = `
(SELECT b.scale FROM model_rate_buckets b
  WHERE b.model = sr.model AND b.bucket_start > sr.ts::date
  ORDER BY b.bucket_start ASC LIMIT 1)`

const claudeRateOfficial = `
(SELECT r.input_rate / 1e6 FROM claude_model_rates r
  WHERE sr.model LIKE r.model_prefix || '%' AND r.effective_from <= sr.ts::date
  ORDER BY length(r.model_prefix) DESC, r.effective_from DESC LIMIT 1)`

// claudeRateSource names which of the three answered, so the dashboard can tell
// spend measured against real billing from spend carried in from another week
// or read off a price list.
const claudeRateSource = `
CASE
  WHEN (SELECT b.bucket_start FROM model_rate_buckets b
         WHERE b.model = sr.model AND b.bucket_start <= sr.ts::date
         ORDER BY b.bucket_start DESC LIMIT 1) = date_trunc('week', sr.ts)::date THEN 'bucket'
  WHEN ` + claudeRateAt + ` IS NOT NULL THEN 'carry_forward'
  WHEN ` + claudeRateBack + ` IS NOT NULL THEN 'carry_back'
  ELSE 'official'
END`

// RefreshClaudeImputedCost recomputes imputed cost for offline Claude sessions
// (JSONL tokens x the OTEL-derived weekly scale, sessions with no OTEL) into
// claude_imputed_cost. Runs periodically off the hot path; see cmd/cctraced.
func (s *PgStore) RefreshClaudeImputedCost(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	affected, err := refreshClaudeImputedCost(ctx, tx)
	if err != nil {
		return err
	}
	if err := markSessionFactsDirty(ctx, tx, affected, nil, dirtyImputed); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// refreshClaudeImputedCost is the refresh inside a transaction the caller owns,
// so step 3 of the P1t transition commits it with its index and marker. It
// writes one row per request (p1t-design §6.1): the head line's srec_id,
// identity, model, ts and agent, and each token field's MAX over the request's
// lines (claudeRequestHeadsSQL). A row per line priced a response written as
// three lines three times.
//
// It returns sessions whose per-request imputed rows changed, including changes
// to their analysis-relevant metadata, for rollup refresh and dirty queueing.
func refreshClaudeImputedCost(ctx context.Context, tx pgx.Tx) ([]string, error) {
	// Writers take turns, first thing: step 3 and the 30-minute refresh are
	// different goroutines of one process. A second DELETE cannot see the rows the
	// first inserted after its snapshot, so it would leave them and insert them
	// again -- every request twice, or, once uniq_claude_imputed_srec exists, a
	// 23505 that rolls the refresh back.
	if err := lockClaudeImputedCostWriters(ctx, tx); err != nil {
		return nil, err
	}
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return nil, err
	}
	// No rate test here. The rows it would exclude are the handful of non-Anthropic
	// models, so filtering for them costs more than carrying them: over-including a
	// session only means its rollup is refreshed with the same numbers it had.
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE claude_imputed_refresh ON COMMIT DROP AS
		SELECT * FROM claude_imputed_cost WITH NO DATA`); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO claude_imputed_refresh `+claudeImputedSelectSQL); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE claude_imputed_changed ON COMMIT DROP AS
		SELECT COALESCE(old.srec_id, new.srec_id) AS srec_id
		FROM claude_imputed_cost old FULL JOIN claude_imputed_refresh new USING (srec_id)
		WHERE old.srec_id IS NULL OR new.srec_id IS NULL OR to_jsonb(old) IS DISTINCT FROM to_jsonb(new)`); err != nil {
		return nil, err
	}
	affected, err := querySessionIDs(ctx, tx, `
		SELECT DISTINCT affected.session_id
		FROM claude_imputed_changed changed
		LEFT JOIN claude_imputed_cost old USING (srec_id)
		LEFT JOIN claude_imputed_refresh new USING (srec_id)
		CROSS JOIN LATERAL unnest(ARRAY[old.session_id,new.session_id]) affected(session_id)
		WHERE affected.session_id <> ''`)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM claude_imputed_cost old USING claude_imputed_changed changed WHERE old.srec_id=changed.srec_id`); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO claude_imputed_cost SELECT new.* FROM claude_imputed_refresh new
		JOIN claude_imputed_changed changed USING (srec_id)`); err != nil {
		return nil, err
	}
	return affected, refreshSessionOverviewRollups(ctx, tx, affected)
}

// lockClaudeImputedCostWriters takes the lock the writers of claude_imputed_cost
// take turns on (refreshClaudeImputedCost says why). It is a transaction-level
// advisory lock, so a transaction that already holds it is granted it again at
// once.
func lockClaudeImputedCostWriters(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('claude_imputed_cost_writers'))`); err != nil {
		return fmt.Errorf("lock claude imputed cost: %w", err)
	}
	return nil
}

// claudeImputedSelectSQL prices each request of an offline Claude session at its
// head. The heads are aliased sr, so the rate subqueries above read the head's
// model and ts. The deleted-session and OTEL-session exclusions are in lineWhere:
// they hold for a whole session, so they cut no request, and applied outside it
// they would wait until every OTEL session's lines had been sorted into requests.
var claudeImputedSelectSQL = `
SELECT sr.id, sr.session_id, sr.ts, COALESCE(sr.user_id,''), sr.profile_email, COALESCE(sr.login_email,''),
	COALESCE(sr.model,''),
	claude_weighted_tokens(sr.model, sr.input_tokens, sr.output_tokens,
	                       sr.cache_read_tokens, sr.cache_create_tokens) * rate.scale,
	COALESCE(sr.input_tokens,0), COALESCE(sr.output_tokens,0),
	COALESCE(sr.cache_read_tokens,0), COALESCE(sr.cache_create_tokens,0),
	sr.agent, sr.billing_provider,
	rate.source
FROM (` + claudeRequestHeadsSQL("session_records", `
	NOT EXISTS (SELECT 1 FROM deleted_sessions d WHERE d.session_id = sr.session_id)
	AND NOT EXISTS (SELECT 1 FROM otel_events o WHERE o.session_id = sr.session_id)`,
	[]string{"id", "session_id", "ts", "user_id", "profile_email", "login_email", "model", "agent", "billing_provider"}) + `) sr
CROSS JOIN LATERAL (
	SELECT COALESCE(` + claudeRateAt + `, ` + claudeRateBack + `, ` + claudeRateOfficial + `) AS scale,
	       ` + claudeRateSource + ` AS source
) rate
WHERE rate.scale IS NOT NULL`

// claudeImputedRequestsBackfill marks step 3 of the P1t transition:
// claude_imputed_cost rewritten one row per request (p1t-design §6.1). Until it
// exists cmd/cctraced skips RefreshClaudeImputedCost, at boot and on the
// 30-minute tick (p1t-design §4.1).
const claudeImputedRequestsBackfill = "claude_imputed_requests_v1"

// ClaudeImputedRequestsReady reports whether step 3 of the P1t transition has
// committed.
func (s *PgStore) ClaudeImputedRequestsReady(ctx context.Context) (bool, error) {
	ready, err := backfillMarked(ctx, s.pool, claudeImputedRequestsBackfill)
	if err != nil {
		return false, fmt.Errorf("read claude imputed requests marker: %w", err)
	}
	return ready, nil
}

// rewriteClaudeImputedCostStep is step 3 of the P1t transition (p1t-design
// §6.1): the refresh -- one row per request, and the overview rows of the
// sessions it touches -- then uniq_claude_imputed_srec, then the marker, in one
// transaction. A failure anywhere leaves the table as it was, without the index
// or the marker, and the next call does it all again. It reports whether it
// committed: false when the marker was already there.
//
// The writers' lock comes before the marker is read in the transaction. Of two
// first calls at once, the second then waits for the first to commit and finds
// its marker, instead of rewriting the table a second time and reporting
// progress too. The refresh takes the lock again, which it already holds.
//
// The index makes a request priced twice an error rather than a cost counted
// twice, as uniq_codex_imputed_srec does for Codex; production has no duplicate
// for it to refuse (R6). claude_imputed_cost is a plain table, so this is one
// index build, not one per chunk. Once the marker commits, cmd/cctraced runs the
// boot and 30-minute refreshes again.
func (s *PgStore) rewriteClaudeImputedCostStep(ctx context.Context) (bool, error) {
	done, err := s.ClaudeImputedRequestsReady(ctx)
	if err != nil || done {
		return false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockClaudeImputedCostWriters(ctx, tx); err != nil {
		return false, err
	}
	done, err = backfillMarked(ctx, tx, claudeImputedRequestsBackfill)
	if err != nil {
		return false, fmt.Errorf("read claude imputed requests marker: %w", err)
	}
	if done {
		return false, tx.Commit(ctx)
	}
	// The request-set transition also changes unpriced request aggregation.
	// Rebuild every affected overview once here; periodic pricing refreshes only
	// refresh sessions whose imputed rows actually changed.
	affected, err := querySessionIDs(ctx, tx, `
		SELECT session_id FROM claude_imputed_cost WHERE session_id <> ''
		UNION SELECT sr.session_id FROM session_records sr
		WHERE sr.agent='claude' AND sr.record_type='assistant' AND sr.session_id<>''
		AND NOT EXISTS (SELECT 1 FROM otel_events e WHERE e.session_id=sr.session_id)`)
	if err != nil {
		return false, fmt.Errorf("find request-set overview sessions: %w", err)
	}
	if _, err := refreshClaudeImputedCost(ctx, tx); err != nil {
		return false, fmt.Errorf("rewrite claude imputed cost by request: %w", err)
	}
	if err := refreshSessionOverviewRollups(ctx, tx, affected); err != nil {
		return false, fmt.Errorf("rebuild request-set overview: %w", err)
	}
	if _, err := tx.Exec(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS uniq_claude_imputed_srec
		ON claude_imputed_cost (srec_id)`); err != nil {
		return false, fmt.Errorf("index claude imputed cost by srec_id: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_backfills (name) VALUES ($1) ON CONFLICT DO NOTHING`,
		claudeImputedRequestsBackfill); err != nil {
		return false, fmt.Errorf("record claude imputed requests marker: %w", err)
	}
	// Last, after the index: queue rows held while it waits for its SHARE lock
	// could close a cycle with a transaction that holds ROW EXCLUSIVE on the table
	// and is queueing the same sessions -- the retention reconciliation deletes
	// imputed orphans before it does.
	if err := markSessionFactsDirty(ctx, tx, affected, nil, dirtyImputed); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
