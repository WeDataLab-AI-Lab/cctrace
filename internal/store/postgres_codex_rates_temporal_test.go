package store

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"cctrace/internal/codexrates"
)

// The three gpt-5.6 price cuts OpenAI shipped in July and August 2026, as the
// migration backfills them. Everything before a cut is billed at the row the
// seed left at -infinity.
var (
	lunaOld = codexrates.Rate{Input: 1.00, Output: 6.00, CacheRead: 0.10}
	lunaNew = codexrates.Rate{Input: 0.20, Output: 1.20, CacheRead: 0.02}
	solOld  = codexrates.Rate{Input: 5.00, Output: 30.00, CacheRead: 0.50}
	solNew  = codexrates.Rate{Input: 4.00, Output: 20.00, CacheRead: 0.40}
)

func codexUsage(session string, ts time.Time, model string, in, out, cacheRead int) *SessionRecord {
	return &SessionRecord{
		Ts:              ts,
		SessionID:       session,
		RecordType:      "usage",
		ProfileEmail:    "codex-temporal@ex.com",
		UserID:          "uid-temporal",
		Model:           model,
		InputTokens:     ptrInt(in),
		OutputTokens:    ptrInt(out),
		CacheReadTokens: ptrInt(cacheRead),
		Agent:           "codex",
		BillingProvider: "openai",
	}
}

// price is the imputation formula from codexImputedSelect, in Go, so a test can
// state what an era's rate should produce without restating the SQL.
func price(r codexrates.Rate, in, out, cacheRead int) float64 {
	billableIn := in - cacheRead
	if billableIn < 0 {
		billableIn = 0
	}
	return (float64(billableIn)*r.Input + float64(out)*r.Output + float64(cacheRead)*r.CacheRead) / 1e6
}

func codexCost(t *testing.T, s *PgStore, model string) float64 {
	t.Helper()
	var cost float64
	if err := s.pool.QueryRow(context.Background(),
		`SELECT COALESCE(sum(cost_usd), 0) FROM codex_imputed_cost WHERE model = $1`, model,
	).Scan(&cost); err != nil {
		t.Fatalf("sum cost for %s: %v", model, err)
	}
	return cost
}

func closeTo(t *testing.T, got, want float64, what string) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s = %.10f, want %.10f", what, got, want)
	}
}

// A rate row is not a fact about today, it is a fact about a period. An event
// from before a price cut must be billed at the price that was in force when it
// happened -- pricing all history at today's rate is as wrong as pricing it all
// at last year's, just in the other direction.
func TestCodexImputedCost_PricesEachEventAtItsEraRate(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	const in, out, cacheRead = 1_000_000, 400_000, 250_000
	before := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	after := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		codexUsage("codex-era-before", before, "gpt-5.6-luna", in, out, cacheRead),
		codexUsage("codex-era-after", after, "gpt-5.6-luna", in, out, cacheRead),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	refreshCodexImputed(t, s)

	wantOld := price(lunaOld, in, out, cacheRead)
	wantNew := price(lunaNew, in, out, cacheRead)
	closeTo(t, codexCost(t, s, "gpt-5.6-luna"), wantOld+wantNew, "gpt-5.6-luna total")

	var got float64
	if err := s.pool.QueryRow(ctx,
		`SELECT cost_usd FROM codex_imputed_cost WHERE session_id = 'codex-era-before'`).Scan(&got); err != nil {
		t.Fatalf("read pre-cut row: %v", err)
	}
	closeTo(t, got, wantOld, "the 2026-07-29 event")
	if err := s.pool.QueryRow(ctx,
		`SELECT cost_usd FROM codex_imputed_cost WHERE session_id = 'codex-era-after'`).Scan(&got); err != nil {
		t.Fatalf("read post-cut row: %v", err)
	}
	closeTo(t, got, wantNew, "the 2026-07-31 event")
}

// The regression this whole change exists to prevent, in miniature. Production
// carries spend on both sides of two cut-overs; billing it all at one rate is
// wrong by thousands of dollars either way, and the two wrong answers bracket
// the right one. Real token counts are not available to a container test, so
// this reproduces the shape with synthetic ones: what matters is that the table
// lands on neither bracket.
func TestCodexImputedCost_TemporalTotalSitsBetweenBothFlatAnswers(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	type row struct {
		session          string
		ts               time.Time
		model            string
		in, out, cache   int
		old, current     codexrates.Rate
		wantEraIsCurrent bool
	}
	rows := []row{
		{"luna-pre", time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC), "gpt-5.6-luna", 8_000_000, 2_000_000, 5_000_000, lunaOld, lunaNew, false},
		{"luna-post", time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC), "gpt-5.6-luna", 40_000_000, 9_000_000, 25_000_000, lunaOld, lunaNew, true},
		{"sol-pre", time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC), "gpt-5.6-sol", 30_000_000, 4_000_000, 20_000_000, solOld, solNew, false},
		{"sol-post", time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC), "gpt-5.6-sol", 12_000_000, 1_500_000, 7_000_000, solOld, solNew, true},
	}
	var recs []*SessionRecord
	var wantTemporal, allOld, allCurrent float64
	for _, r := range rows {
		recs = append(recs, codexUsage(r.session, r.ts, r.model, r.in, r.out, r.cache))
		allOld += price(r.old, r.in, r.out, r.cache)
		allCurrent += price(r.current, r.in, r.out, r.cache)
		if r.wantEraIsCurrent {
			wantTemporal += price(r.current, r.in, r.out, r.cache)
		} else {
			wantTemporal += price(r.old, r.in, r.out, r.cache)
		}
	}
	if err := s.InsertSessionRecords(ctx, recs); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	refreshCodexImputed(t, s)

	var total float64
	if err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(sum(cost_usd), 0) FROM codex_imputed_cost WHERE model LIKE 'gpt-5.6-%'`).Scan(&total); err != nil {
		t.Fatalf("sum: %v", err)
	}
	closeTo(t, total, wantTemporal, "temporal total")
	if !(total < allOld && total > allCurrent) {
		t.Errorf("temporal total %.2f should sit between the all-current (%.2f) and all-old (%.2f) answers", total, allCurrent, allOld)
	}
}

// The seeded -infinity rows must still hold the OLD prices. Overwriting them
// with today's is the naive deployment that swaps an overcharge for an
// undercharge across all history.
func TestMigration_BackfillsCutOverRowsWithoutTouchingHistory(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()

	for _, tc := range []struct {
		prefix string
		eff    string
		want   codexrates.Rate
	}{
		{"gpt-5.6-luna", "-infinity", lunaOld},
		{"gpt-5.6-luna", "2026-07-30", lunaNew},
		{"gpt-5.6-terra", "-infinity", codexrates.Rate{Input: 2.50, Output: 15.00, CacheRead: 0.25}},
		{"gpt-5.6-terra", "2026-07-30", codexrates.Rate{Input: 2.00, Output: 12.00, CacheRead: 0.20}},
		{"gpt-5.6-sol", "-infinity", solOld},
		{"gpt-5.6-sol", "2026-08-21", solNew},
	} {
		var got codexrates.Rate
		err := s.pool.QueryRow(ctx,
			`SELECT input_rate, output_rate, cache_read_rate FROM codex_model_rates
			WHERE model_prefix = $1 AND effective_from = $2::date`, tc.prefix, tc.eff,
		).Scan(&got.Input, &got.Output, &got.CacheRead)
		if err != nil {
			t.Errorf("%s @ %s: %v", tc.prefix, tc.eff, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s @ %s = %+v, want %+v", tc.prefix, tc.eff, got, tc.want)
		}
	}
}

// A price change is a new row, not an edit. Rewriting the row in place would
// reprice every event that happened while the old price was in force.
func TestUpsertCodexModelRates_PriceChangeAddsARowAndLeavesHistoryAlone(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	const fake = "zz-codexrates-temporal"
	t.Cleanup(func() {
		_, _ = s.pool.Exec(ctx, `DELETE FROM codex_model_rates WHERE model_prefix = $1`, fake)
	})

	first := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	second := time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)
	old := codexrates.Rate{Input: 5, Output: 30, CacheRead: 0.5}
	if n, err := s.UpsertCodexModelRates(ctx,
		map[string]codexrates.Rate{fake: old},
		map[string]time.Time{fake: first}, "openai-md"); err != nil || n != 1 {
		t.Fatalf("first upsert = %d, %v; want 1, nil", n, err)
	}

	cut := codexrates.Rate{Input: 4, Output: 20, CacheRead: 0.4}
	if n, err := s.UpsertCodexModelRates(ctx,
		map[string]codexrates.Rate{fake: cut},
		map[string]time.Time{fake: second}, "openai-md"); err != nil || n != 1 {
		t.Fatalf("price-change upsert = %d, %v; want 1, nil", n, err)
	}

	rows, err := s.pool.Query(ctx,
		`SELECT effective_from, input_rate, output_rate, cache_read_rate FROM codex_model_rates
		WHERE model_prefix = $1 ORDER BY effective_from`, fake)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var eff time.Time
		var r codexrates.Rate
		if err := rows.Scan(&eff, &r.Input, &r.Output, &r.CacheRead); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, eff.Format("2006-01-02")+" "+formatRate(r))
	}
	want := []string{"2026-06-01 " + formatRate(old), "2026-08-21 " + formatRate(cut)}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("rows = %v, want %v", got, want)
	}

	// Same fetch again on the same day: the latest row already says this, so
	// nothing is added and nothing is repriced.
	if n, err := s.UpsertCodexModelRates(ctx,
		map[string]codexrates.Rate{fake: cut},
		map[string]time.Time{fake: second}, "openai-md"); err != nil || n != 0 {
		t.Fatalf("repeat upsert = %d, %v; want 0, nil", n, err)
	}
	var count int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM codex_model_rates WHERE model_prefix = $1`, fake).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("row count = %d after a repeat sync, want 2", count)
	}
}

// A changed price whose date we could not establish -- no changelog, or nothing
// tagging that model -- still has to land somewhere. It lands today, which
// leaves everything already billed alone.
func TestUpsertCodexModelRates_UndatedChangeTakesEffectToday(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	const fake = "zz-codexrates-undated"
	t.Cleanup(func() {
		_, _ = s.pool.Exec(ctx, `DELETE FROM codex_model_rates WHERE model_prefix = $1`, fake)
	})

	if _, err := s.UpsertCodexModelRates(ctx,
		map[string]codexrates.Rate{fake: {Input: 1, Output: 2, CacheRead: 0.1}}, nil, "openai-md"); err != nil {
		t.Fatalf("UpsertCodexModelRates: %v", err)
	}
	var eff time.Time
	var isToday bool
	if err := s.pool.QueryRow(ctx,
		`SELECT effective_from, effective_from = CURRENT_DATE FROM codex_model_rates WHERE model_prefix = $1`,
		fake).Scan(&eff, &isToday); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !isToday {
		t.Errorf("effective_from = %s, want today", eff.Format("2006-01-02"))
	}
}

func formatRate(r codexrates.Rate) string {
	return fmt.Sprintf("%g/%g/%g", r.Input, r.Output, r.CacheRead)
}

// Migrations run on every boot, against a database that already has the old
// single-column key and rows in it. This walks the table back to that shape and
// re-migrates, which is the only way to find out whether the key swap works
// anywhere but a fresh container.
func TestMigration_TemporalKeySwapIsIdempotent(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()

	for _, stmt := range []string{
		`DELETE FROM codex_model_rates WHERE effective_from <> '-infinity'`,
		`ALTER TABLE codex_model_rates DROP CONSTRAINT codex_model_rates_pkey`,
		`ALTER TABLE codex_model_rates DROP COLUMN effective_from`,
		`ALTER TABLE codex_model_rates ADD CONSTRAINT codex_model_rates_pkey PRIMARY KEY (model_prefix)`,
	} {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("revert to the pre-temporal shape (%s): %v", stmt, err)
		}
	}

	// Twice: the first run performs the swap, the second must be a no-op.
	for i := 0; i < 2; i++ {
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("Migrate run %d: %v", i+1, err)
		}
	}

	var cols string
	if err := s.pool.QueryRow(ctx, `
		SELECT string_agg(a.attname, ',' ORDER BY k.ord)
		FROM pg_constraint c
		JOIN LATERAL unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord) ON TRUE
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
		WHERE c.conrelid = 'codex_model_rates'::regclass AND c.contype = 'p'`).Scan(&cols); err != nil {
		t.Fatalf("read primary key: %v", err)
	}
	if cols != "model_prefix,effective_from" {
		t.Errorf("primary key = (%s), want (model_prefix,effective_from)", cols)
	}

	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM codex_model_rates WHERE source = 'changelog-backfill'`).Scan(&n); err != nil {
		t.Fatalf("count backfill rows: %v", err)
	}
	if n != 3 {
		t.Errorf("backfill rows = %d, want 3", n)
	}

	var r codexrates.Rate
	if err := s.pool.QueryRow(ctx,
		`SELECT input_rate, output_rate, cache_read_rate FROM codex_model_rates
		WHERE model_prefix = 'gpt-5.6-sol' AND effective_from = '-infinity'`,
	).Scan(&r.Input, &r.Output, &r.CacheRead); err != nil {
		t.Fatalf("read the -infinity baseline: %v", err)
	}
	if r != solOld {
		t.Errorf("gpt-5.6-sol baseline = %+v, want the pre-cut %+v", r, solOld)
	}
}
