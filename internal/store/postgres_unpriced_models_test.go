package store

import (
	"context"
	"testing"
	"time"
)

func findUnpriced(list []UnpricedModel, agent, model string) *UnpricedModel {
	for i := range list {
		if list[i].Agent == agent && list[i].Model == model {
			return &list[i]
		}
	}
	return nil
}

// #286: codex-auto-review is a routing alias no rate prefix matches, so its
// hundred million tokens priced at $0 and only a server log line said so. The
// listing is what lets an admin see it without reading logs -- and it has to find
// the alias without knowing its name in advance.
func TestListUnpricedModels_surfacesARoutingAliasWithNoRate(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	first := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)
	last := first.Add(3 * time.Hour)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		codexUsageRecord("unpriced-alias", "codex-auto-review", first, 1000, 100, 200),
		codexUsageRecord("unpriced-alias", "codex-auto-review", last, 3000, 300, 0),
		codexUsageRecord("unpriced-priced", "gpt-5.5", first, 1000, 100, 0),
		// No tokens: nothing was under-costed, so there is nothing to report.
		codexUsageRecord("unpriced-empty", "no-usage-model", first, 0, 0, 0),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.RefreshCodexImputedCost(ctx); err != nil {
		t.Fatalf("RefreshCodexImputedCost: %v", err)
	}

	list, err := s.ListUnpricedModels(ctx)
	if err != nil {
		t.Fatalf("ListUnpricedModels: %v", err)
	}
	got := findUnpriced(list, "codex", "codex-auto-review")
	if got == nil {
		t.Fatalf("codex-auto-review missing from %+v -- a model with no rate must be listed", list)
	}
	if got.Rows != 2 {
		t.Errorf("rows = %d, want 2", got.Rows)
	}
	// input_tokens in codex_imputed_cost excludes cache reads (1000-200 + 3000).
	if got.InputTokens != 3800 || got.OutputTokens != 400 || got.CacheReadTokens != 200 {
		t.Errorf("tokens = in %d / out %d / cache %d, want 3800 / 400 / 200",
			got.InputTokens, got.OutputTokens, got.CacheReadTokens)
	}
	if !got.FirstTs.Equal(first) || !got.LastTs.Equal(last) {
		t.Errorf("seen %v .. %v, want %v .. %v", got.FirstTs, got.LastTs, first, last)
	}
	if findUnpriced(list, "codex", "gpt-5.5") != nil {
		t.Error("gpt-5.5 has a rate and must not be listed")
	}
	if findUnpriced(list, "codex", "no-usage-model") != nil {
		t.Error("a model with no token usage was listed; $0 on zero tokens is not under-costing")
	}
}

// A model an admin has declared flat-rate is $0 by design -- a local model, a
// subscription SKU. It leaves the list, so the list keeps meaning "price unknown",
// and it comes back when the mark is removed. The mark is keyed on the agent too:
// the same raw id under another agent is another model.
func TestFlatRateModel_leavesTheUnpricedListUntilUnmarked(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC().Add(-time.Hour)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		codexUsageRecord("flat-local", "gemma4:12b", now, 500, 50, 0),
		codexUsageRecord("flat-alias", "codex-auto-review", now, 500, 50, 0),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.RefreshCodexImputedCost(ctx); err != nil {
		t.Fatalf("RefreshCodexImputedCost: %v", err)
	}

	if err := s.MarkFlatRateModel(ctx, "codex", "gemma4:12b", "local ollama", "admin@example.com"); err != nil {
		t.Fatalf("MarkFlatRateModel: %v", err)
	}
	if err := s.MarkFlatRateModel(ctx, "gjc", "codex-auto-review", "", "admin@example.com"); err != nil {
		t.Fatalf("MarkFlatRateModel: %v", err)
	}

	list, err := s.ListUnpricedModels(ctx)
	if err != nil {
		t.Fatalf("ListUnpricedModels: %v", err)
	}
	if findUnpriced(list, "codex", "gemma4:12b") != nil {
		t.Error("a model marked flat-rate is still listed as unpriced")
	}
	if findUnpriced(list, "codex", "codex-auto-review") == nil {
		t.Error("a mark under another agent hid the codex model")
	}

	marks, err := s.ListFlatRateModels(ctx)
	if err != nil {
		t.Fatalf("ListFlatRateModels: %v", err)
	}
	if len(marks) != 2 {
		t.Fatalf("listed %d marks, want 2: %+v", len(marks), marks)
	}
	var local *FlatRateModel
	for i := range marks {
		if marks[i].Agent == "codex" && marks[i].Model == "gemma4:12b" {
			local = &marks[i]
		}
	}
	if local == nil || local.Reason != "local ollama" || local.CreatedBy != "admin@example.com" {
		t.Errorf("mark = %+v, want the reason and actor preserved", local)
	}

	if err := s.UnmarkFlatRateModel(ctx, "codex", "gemma4:12b"); err != nil {
		t.Fatalf("UnmarkFlatRateModel: %v", err)
	}
	list, err = s.ListUnpricedModels(ctx)
	if err != nil {
		t.Fatalf("ListUnpricedModels: %v", err)
	}
	if findUnpriced(list, "codex", "gemma4:12b") == nil {
		t.Error("an unmarked model did not return to the unpriced list")
	}
}

// ORDER BY 4 + 5 DESC is not an ordinal: an ordinal is only a column reference
// when it is a lone integer literal, and 4 + 5 is the expression 9, which names a
// column position past the end of the select list -- Postgres accepts it anyway
// because sort ordinals silently clamp, so this shipped without erroring and just
// sorted by (agent, model) instead of by token volume. Two models are seeded so
// that alphabetical order and token-volume order disagree: the fix must show the
// heavier model first even though its name sorts after the lighter one's.
func TestListUnpricedModels_ordersByTokenVolumeNotAlphabetically(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	ts := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		codexUsageRecord("order-light", "aaa-light-model", ts, 50, 10, 0),
		codexUsageRecord("order-heavy", "zzz-heavy-model", ts, 5000, 500, 0),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.RefreshCodexImputedCost(ctx); err != nil {
		t.Fatalf("RefreshCodexImputedCost: %v", err)
	}

	list, err := s.ListUnpricedModels(ctx)
	if err != nil {
		t.Fatalf("ListUnpricedModels: %v", err)
	}
	heavyIdx, lightIdx := -1, -1
	for i := range list {
		switch list[i].Model {
		case "zzz-heavy-model":
			heavyIdx = i
		case "aaa-light-model":
			lightIdx = i
		}
	}
	if heavyIdx == -1 || lightIdx == -1 {
		t.Fatalf("both seeded models must be listed, got %+v", list)
	}
	if heavyIdx > lightIdx {
		t.Errorf("zzz-heavy-model (more tokens) sorted after aaa-light-model (fewer tokens): %+v -- "+
			"ORDER BY must rank by token volume, not fall back to the alphabetical tiebreak", list)
	}
}

// NormalizeFlatRateKey folds the agent to lowercase before a mark is stored, so
// flat_rate_models.agent is always lowercase. The unpriced query's NOT EXISTS
// compared the raw, unfolded e.agent -- fine while every producer already writes
// lowercase, but a defensive fold keeps a mark effective even if one ever doesn't.
// otel_events (unlike the codex/claude/gjc/omo arms, which filter on an exact
// lowercase agent) accepts whatever case a producer sends, so it is the arm this
// seeds directly to exercise a mixed-case agent value.
func TestListUnpricedModels_flatRateMatchFoldsAgentCase(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	ts := recentDay(58).Add(9 * time.Hour)
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO otel_events (ts, event_name, agent, model, cost_usd, input_tokens, output_tokens)
		VALUES ($1, 'test_usage', 'Codex', 'mixed-case-agent-model', 0, 500, 50)`, ts); err != nil {
		t.Fatalf("insert otel_events: %v", err)
	}
	if err := s.MarkFlatRateModel(ctx, "Codex", "mixed-case-agent-model", "", "admin@example.com"); err != nil {
		t.Fatalf("MarkFlatRateModel: %v", err)
	}

	list, err := s.ListUnpricedModels(ctx)
	if err != nil {
		t.Fatalf("ListUnpricedModels: %v", err)
	}
	if findUnpriced(list, "Codex", "mixed-case-agent-model") != nil {
		t.Errorf("a model marked flat-rate under a differently-cased agent is still listed: %+v", list)
	}
}
