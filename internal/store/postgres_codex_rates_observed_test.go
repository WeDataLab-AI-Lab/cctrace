package store

import (
	"context"
	"testing"
	"time"

	"cctrace/internal/codexrates"
)

func TestUpsertCodexRatesAt_TC23PinsObservationFallbackForCachedRetries(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	// A fixed historical clock, not today's wall time: retry date must be the
	// accepted table's observation even when the database calendar is later.
	observed := time.Date(2000, 1, 2, 23, 59, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		seed      bool
		effective map[string]time.Time
	}{
		{name: "missing_date"},
		{name: "same_date", seed: true, effective: map[string]time.Time{"zz-791-observed": observed}},
		{name: "older_date", seed: true, effective: map[string]time.Time{"zz-791-observed": observed.Add(-24 * time.Hour)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const model = "zz-791-observed"
			t.Cleanup(func() { _, _ = s.pool.Exec(ctx, `DELETE FROM codex_model_rates WHERE model_prefix = $1`, model) })
			if tc.seed {
				if _, err := s.pool.Exec(ctx, `INSERT INTO codex_model_rates
					(model_prefix,input_rate,output_rate,cache_read_rate,effective_from)
					VALUES ($1,1,5,.1,$2::date)`, model, observed); err != nil {
					t.Fatal(err)
				}
			}
			rates := map[string]codexrates.Rate{model: {Input: 2, Output: 10, CacheRead: .1}}
			if n, err := s.UpsertCodexModelRatesAt(ctx, rates, tc.effective, "test", observed); err != nil || n != 1 {
				t.Fatalf("upsert = %d, %v", n, err)
			}
			var date string
			var in, out float64
			var count int
			if err := s.pool.QueryRow(ctx, `SELECT effective_from::text,input_rate,output_rate FROM codex_model_rates
				WHERE model_prefix = $1 ORDER BY effective_from DESC LIMIT 1`, model).Scan(&date, &in, &out); err != nil {
				t.Fatal(err)
			}
			if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM codex_model_rates WHERE model_prefix=$1`, model).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if date != "2000-01-02" || in != 2 || out != 10 || count != 1 {
				t.Fatalf("rate = %s %v/%v, rows %d; want accepted observation 2000-01-02 2/10, one row", date, in, out, count)
			}
		})
	}
}
