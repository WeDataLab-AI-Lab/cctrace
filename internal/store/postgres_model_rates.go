package store

import (
	"context"
)

// RefreshModelRateBuckets derives, per model and week, the dollars Anthropic
// actually billed per weighted token, from the OTEL events that carry a real
// cost. Offline sessions have no cost of their own and are priced with these.
//
// Fitting the scale rather than reading a price list is what keeps it honest:
// it absorbs the 1h-vs-5m cache write mix, any residency multiplier, and any
// surcharge the published table does not mention -- measured against billing,
// a published-price calculation runs 6-21% low on this deployment's traffic
// while the fitted scale moves 2-3% week to week.
//
// Only the weeks that gained rows are rebuilt. Every other week keeps the
// number it already had, which is the property the 90-day rolling view it
// replaces did not have: there, a March row's cost moved whenever June traffic
// did, so spend that had already been reported changed underneath it.
func (s *PgStore) RefreshModelRateBuckets(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// max() rather than a WHERE on the seeded row: a restore or a truncate can
	// leave the table empty, and "start from the beginning" is the right answer
	// there, not a failed refresh.
	var cursor int64
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(max(last_event_id), 0) FROM model_rate_cursor`).Scan(&cursor); err != nil {
		return err
	}

	// The id of the newest row this pass may claim, read before the aggregate so
	// anything inserted while it runs is left for the next pass rather than
	// marked done without being folded in.
	var head int64
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(max(id), 0) FROM otel_events WHERE agent = 'claude' AND cost_usd > 0`).Scan(&head); err != nil {
		return err
	}

	// HAVING count >= 20 keeps a handful of rows from setting a week's price. A
	// week that does not clear it simply has no bucket, and the lookup carries a
	// neighbouring week in rather than inventing one.
	//
	// The whole week is re-aggregated, not just its new rows: a scale is a ratio
	// over the week's totals, so rows arriving mid-week have to be folded into
	// the sum rather than appended to a finished number.
	_, err = tx.Exec(ctx, `
WITH touched AS (
	SELECT DISTINCT model, date_trunc('week', ts)::date AS bucket_start
	FROM otel_events
	WHERE agent = 'claude' AND cost_usd > 0 AND id > $1 AND id <= $2
)
INSERT INTO model_rate_buckets (model, bucket_start, scale, sample_rows, computed_at)
SELECT o.model,
       date_trunc('week', o.ts)::date,
       sum(o.cost_usd) / sum(claude_weighted_tokens(o.model, o.input_tokens, o.output_tokens,
                                                    o.cache_read_tokens, o.cache_create_tokens)),
       count(*),
       now()
FROM otel_events o
JOIN touched t ON t.model = o.model AND t.bucket_start = date_trunc('week', o.ts)::date
WHERE o.agent = 'claude' AND o.cost_usd > 0
GROUP BY 1, 2
HAVING count(*) >= 20
   AND sum(claude_weighted_tokens(o.model, o.input_tokens, o.output_tokens,
                                  o.cache_read_tokens, o.cache_create_tokens)) > 0
ON CONFLICT (model, bucket_start) DO UPDATE
SET scale = EXCLUDED.scale, sample_rows = EXCLUDED.sample_rows, computed_at = EXCLUDED.computed_at`,
		cursor, head)
	if err != nil {
		return err
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO model_rate_cursor (only_row, last_event_id) VALUES (TRUE, $1)
		 ON CONFLICT (only_row) DO UPDATE SET last_event_id = EXCLUDED.last_event_id`, head); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
