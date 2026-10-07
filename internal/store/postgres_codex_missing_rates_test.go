package store

import (
	"context"
	"testing"
	"time"

	"cctrace/internal/codexrates"
)

func TestUnpricedCodexRules(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := recentDay(3).Add(12 * time.Hour)
	// A rate takes effect on its effective_from date: today's rows are priced by a rule
	// dated the same day and not by one dated the day after.
	rateDay, nextDay := ts.Format("2006-01-02"), ts.AddDate(0, 0, 1).Format("2006-01-02")
	cases := []struct {
		id, model      string
		in, out, cache int
		want           bool
	}{
		{"TC13_cache_only", "zz-791-cache", 100, 0, 100, true},
		{"TC14_valid_zero_rate", "zz-791-free", 100, 10, 0, false},
		{"TC15_future_rate", "zz-791-future", 100, 10, 0, true},
		{"TC16_dated_prefix", "ZZ-791-PREFIX-snapshot", 100, 10, 0, false},
		{"TC17_other_agent_flat", "zz-791-other-flat", 100, 10, 0, true},
		{"TC17_codex_flat", "zz-791-flat", 100, 10, 0, false},
		{"TC19_no_usage", "zz-791-empty", 0, 0, 0, false},
		{"TC19_empty_model", "", 100, 10, 0, true}, // retain admin diagnosis, not an external pricing key.
	}
	for _, tc := range cases {
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{
			codexUsageRecord(tc.id, tc.model, ts, tc.in, tc.out, tc.cache),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{codexUsageRecord("duplicate-cache-key", "zz-791-cache", ts.Add(time.Minute), 100, 0, 100)}); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct{ prefix, date string }{
		{"zz-791-free", rateDay},
		{"zz-791-future", nextDay},
		{"zz-791-prefix", rateDay},
		{"zz-791-provider", rateDay},
	} {
		if _, err := s.pool.Exec(ctx, `INSERT INTO codex_model_rates
			(model_prefix, input_rate, output_rate, cache_read_rate, effective_from)
			VALUES ($1, 0, 0, 0, $2::date)`, r.prefix, r.date); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _, _ = s.pool.Exec(ctx, `DELETE FROM codex_model_rates WHERE model_prefix LIKE 'zz-791-%'`) })
	if err := s.MarkFlatRateModel(ctx, "gjc", "zz-791-other-flat", "", "test@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkFlatRateModel(ctx, "codex", "zz-791-flat", "", "test@example.com"); err != nil {
		t.Fatal(err)
	}
	refreshCodexImputed(t, s)
	// Codex-specific improvements must not change another provider's predicate.
	if _, err := s.pool.Exec(ctx, `INSERT INTO otel_events
		(ts, event_name, agent, model, cost_usd, input_tokens, output_tokens, cache_read_tokens)
		VALUES ($1, 'test_usage', 'gjc', 'zz-791-provider', 0, 100, 0, 0),
		($1, 'test_usage', 'gjc', 'zz-791-provider-cache', 0, 0, 0, 100)`, ts); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListUnpricedModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := s.ListUnpricedCodexModelKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			if got := findUnpriced(list, "codex", tc.model) != nil; got != tc.want {
				t.Errorf("admin contains %s = %v, want %v", tc.model, got, tc.want)
			}
			present := false
			for _, key := range keys {
				if key == (UnpricedModelKey{Agent: "codex", Model: tc.model}) {
					present = true
				}
			}
			wantKey := tc.want && tc.model != ""
			if present != wantKey {
				t.Errorf("scheduler contains %s = %v, want %v: %+v", tc.model, present, wantKey, keys)
			}
		})
	}
	if len(keys) != 3 {
		t.Errorf("unique Codex keys = %v, want cache/future/other-flat only", keys)
	}
	t.Run("TC18_non_codex_unchanged", func(t *testing.T) {
		if findUnpriced(list, "gjc", "zz-791-provider") == nil || findUnpriced(list, "gjc", "zz-791-provider-cache") != nil {
			t.Errorf("non-Codex rules changed: %+v", list)
		}
	})
}

func TestCodexRates_TC20GPT61RepricesHistoryAndCostByUser(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const model = "gpt-6.1-sol"
	release := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	ts := release.Add(12 * time.Hour)
	t.Cleanup(func() { _, _ = s.pool.Exec(ctx, `DELETE FROM codex_model_rates WHERE model_prefix = $1`, model) })
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{codexUsageRecord("gpt61-history", model, ts, 1000, 100, 800)}); err != nil {
		t.Fatal(err)
	}
	refreshCodexImputed(t, s)
	closeTo(t, codexCost(t, s, model), 0, "before published rate")
	// Official standard <=272K pricing, published 2026-09-29: not gpt-6-sol's cache price.
	changed, err := s.UpsertCodexModelRates(ctx,
		map[string]codexrates.Rate{model: {Input: 2, Output: 10, CacheRead: 0.1}},
		map[string]time.Time{model: release}, "openai-md")
	if err != nil || changed != 1 {
		t.Fatalf("upsert = %d, %v", changed, err)
	}
	refreshCodexImputed(t, s)
	var gotModel string
	var in, out, cache int
	var cost float64
	if err := s.pool.QueryRow(ctx, `SELECT model, input_tokens, output_tokens, cache_read_tokens, cost_usd
		FROM codex_imputed_cost WHERE session_id = 'gpt61-history'`).Scan(&gotModel, &in, &out, &cache, &cost); err != nil {
		t.Fatal(err)
	}
	if gotModel != model || in != 200 || out != 100 || cache != 800 {
		t.Fatalf("row = %s %d/%d/%d", gotModel, in, out, cache)
	}
	closeTo(t, cost, 0.00148, "repriced historical cost")
	summaries, err := s.CostByUser(ctx, release, release.Add(24*time.Hour), "", "", "uid-codex-imputed")
	if err != nil || len(summaries) != 1 {
		t.Fatalf("CostByUser = %+v, %v", summaries, err)
	}
	got := summaries[0]
	if got.Model != model || got.Agent != "codex" || got.TotalInput != 200 || got.TotalOutput != 100 || got.RequestCount != 1 {
		t.Fatalf("CostByUser row = %+v", got)
	}
	closeTo(t, got.TotalCost, 0.00148, "CostByUser historical cost")
}
