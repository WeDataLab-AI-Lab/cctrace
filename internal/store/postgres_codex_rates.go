package store

import (
	"context"
	"sort"
	"time"

	"cctrace/internal/codexrates"
)

// UpsertCodexModelRates records a fetched rate table and reports how many models
// it priced differently from what we already knew.
//
// The table is temporal -- one row per (model_prefix, effective_from) -- so a
// price change is an INSERT, never an edit. Rewriting a model's row in place
// would reprice every event that happened while the old price was in force,
// which is how a correct historical total turns into a wrong one overnight. For
// each model:
//
//   - the latest row already says this: only source and fetched_at are written,
//     recording that the price was confirmed today. Not counted as a change.
//   - the price differs (or the model is new): a row is inserted at its
//     effective date. Earlier rows are left exactly as they are.
//   - a row already exists at that date: it is updated, so re-running the sync
//     the same day is idempotent rather than additive.
//
// It never deletes. The published table drops retired models, and removing
// their rows would reprice their history to $0 -- a model with no matching
// prefix costs nothing.
//
// effective is the date each model's price took effect, usually from the
// changelog (see codexrates.EffectiveDate). A model missing from it, or dated no
// later than the row it would supersede, takes effect today: the change is real
// but undated, and dating it from today is the option that leaves everything
// already billed alone.
//
// The returned count is the rows inserted, which is the caller's trigger for
// repricing codex_imputed_cost.
func (s *PgStore) UpsertCodexModelRates(ctx context.Context, rates map[string]codexrates.Rate, effective map[string]time.Time, source string) (int, error) {
	if len(rates) == 0 {
		return 0, nil
	}
	prefixes := make([]string, 0, len(rates))
	for prefix := range rates {
		prefixes = append(prefixes, prefix)
	}
	sort.Strings(prefixes)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	changed := 0
	for _, prefix := range prefixes {
		r := rates[prefix]
		var eff *time.Time
		if d, ok := effective[prefix]; ok && !d.IsZero() {
			eff = &d
		}
		var inserted int
		if err := tx.QueryRow(ctx, upsertCodexRateSQL, prefix, r.Input, r.Output, r.CacheRead, eff, source).Scan(&inserted); err != nil {
			return 0, err
		}
		changed += inserted
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return changed, nil
}

// upsertCodexRateSQL applies one model's fetched rate.
//
// One statement so the comparison and the write see the same snapshot. `target`
// decides both questions at once: whether the newest row already carries this
// price, and which date a new row would take. The date is the caller's unless it
// would land at or before the row it is meant to supersede -- a superseded row
// never takes effect, so an out-of-order date falls back to today.
const upsertCodexRateSQL = `
WITH latest AS (
	SELECT effective_from, input_rate, output_rate, cache_read_rate
	FROM codex_model_rates
	WHERE model_prefix = $1
	ORDER BY effective_from DESC
	LIMIT 1
), target AS (
	SELECT
		l.effective_from AS latest_from,
		(l.effective_from IS NOT NULL
			AND l.input_rate = $2::float8
			AND l.output_rate = $3::float8
			AND l.cache_read_rate = $4::float8) AS unchanged,
		CASE
			WHEN $5::date IS NULL THEN CURRENT_DATE
			WHEN l.effective_from IS NULL OR $5::date > l.effective_from THEN $5::date
			ELSE CURRENT_DATE
		END AS effective_from
	FROM (SELECT 1) one LEFT JOIN latest l ON TRUE
), confirmed AS (
	UPDATE codex_model_rates c
	SET source = $6, fetched_at = now()
	FROM target t
	WHERE t.unchanged AND c.model_prefix = $1 AND c.effective_from = t.latest_from
	RETURNING 1
), inserted AS (
	INSERT INTO codex_model_rates
		(model_prefix, input_rate, output_rate, cache_read_rate, effective_from, source, fetched_at)
	SELECT $1, $2::float8, $3::float8, $4::float8, t.effective_from, $6, now()
	FROM target t WHERE NOT t.unchanged
	ON CONFLICT (model_prefix, effective_from) DO UPDATE SET
		input_rate = EXCLUDED.input_rate,
		output_rate = EXCLUDED.output_rate,
		cache_read_rate = EXCLUDED.cache_read_rate,
		source = EXCLUDED.source,
		fetched_at = EXCLUDED.fetched_at
	RETURNING 1
)
-- confirmed is a data-modifying CTE: it runs whether or not anything selects
-- from it. Only the insert is counted.
SELECT count(*) FROM inserted`
