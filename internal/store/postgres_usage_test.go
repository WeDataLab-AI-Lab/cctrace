package store

import (
	"context"
	"testing"
	"time"
)

func TestPgStore_UsageAggregates_CacheOnlyCodexUsesNormalizedVisibleTokens(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	start := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		codexUsageRecord("codex-cache-only", "gpt-5.5", start, 100, 0, 100),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	refreshCodexImputed(t, s)

	var rawInput, rawCache int
	if err := s.pool.QueryRow(ctx, `
		SELECT input_tokens, cache_read_tokens
		FROM session_records
		WHERE session_id = $1`, "codex-cache-only").Scan(&rawInput, &rawCache); err != nil {
		t.Fatalf("read raw session record: %v", err)
	}
	if rawInput != 100 || rawCache != 100 {
		t.Fatalf("raw tokens = %d/%d, want input/cache 100/100", rawInput, rawCache)
	}

	var visibleInput, visibleOutput, visibleCache int
	if err := s.pool.QueryRow(ctx, `
		SELECT input_tokens, output_tokens, cache_read_tokens
		FROM visible_events
		WHERE session_id = $1 AND event_name = 'codex_usage'`, "codex-cache-only").Scan(
		&visibleInput, &visibleOutput, &visibleCache,
	); err != nil {
		t.Fatalf("read normalized visible event: %v", err)
	}
	if visibleInput != 0 || visibleOutput != 0 || visibleCache != 100 {
		t.Fatalf("visible tokens = %d/%d/%d, want normalized input/output/cache 0/0/100", visibleInput, visibleOutput, visibleCache)
	}

	since, until := start.Add(-time.Minute), start.Add(time.Minute)
	usage, err := s.UsageAggregates(ctx, SessionOverviewFilter{Since: &since, Until: &until})
	if err != nil {
		t.Fatalf("UsageAggregates: %v", err)
	}
	if usage.SessionCount != 1 || usage.InputTokens != 0 || usage.OutputTokens != 0 {
		t.Fatalf("ungrouped session/tokens = %d/%d/%d, want 1/0/0", usage.SessionCount, usage.InputTokens, usage.OutputTokens)
	}
	if len(usage.ByModel) != 1 || usage.ByModel[0].InputTokens != 0 || usage.ByModel[0].OutputTokens != 0 {
		t.Fatalf("ungrouped by-model = %+v, want one normalized 0/0 row", usage.ByModel)
	}

	grouped, err := s.CostByModel(ctx, since, until, "", "", "")
	if err != nil {
		t.Fatalf("CostByModel: %v", err)
	}
	if len(grouped) != 1 {
		t.Fatalf("grouped rows = %d, want 1", len(grouped))
	}
	if grouped[0].InputTokens != 0 || grouped[0].OutputTokens != 0 {
		t.Fatalf("grouped tokens = %d/%d, want normalized 0/0", grouped[0].InputTokens, grouped[0].OutputTokens)
	}
	wantCost := 100.0 * 0.5 / 1_000_000.0
	if grouped[0].TotalCost < wantCost-1e-12 || grouped[0].TotalCost > wantCost+1e-12 {
		t.Fatalf("grouped cost = %.10f, want cache cost %.10f", grouped[0].TotalCost, wantCost)
	}
}

func TestPgStore_UsageAggregates_ClaudeSessionRecordFallbackRemainsAvailable(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	start := time.Date(2026, 8, 15, 11, 0, 0, 0, time.UTC)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
		Ts: start, SessionID: "claude-session-only", RecordType: "assistant",
		ProfileEmail: "claude@example.com", Model: "claude-sonnet", InputTokens: ptrInt(42),
		OutputTokens: ptrInt(7), Agent: "claude", BillingProvider: "anthropic",
	}}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	since, until := start.Add(-time.Minute), start.Add(time.Minute)
	usage, err := s.UsageAggregates(ctx, SessionOverviewFilter{Since: &since, Until: &until})
	if err != nil {
		t.Fatalf("UsageAggregates: %v", err)
	}
	if usage.InputTokens != 42 || usage.OutputTokens != 7 {
		t.Fatalf("Claude fallback tokens = %d/%d, want 42/7", usage.InputTokens, usage.OutputTokens)
	}
}

// The three surfaces that report the same window have to agree.
//
// #545 is not "one of them is wrong" but "they disagree": the ungrouped Open API
// response takes UsageAggregates' session-record fallback, while group_by=model
// and the dashboard read visible_events. For a cache-only Codex session those
// two sources differ -- the session record's input carries the cache, the
// normalized event does not -- so the same window reported two input totals
// depending on which screen asked.
//
// Nothing in the repository pinned that they agree, which is why the divergence
// could exist. This is that contract.
func TestPgStore_UsageAggregates_SurfacesAgreeOnCacheOnlyCodex(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	start := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)

	// The shape from the issue: no visible input or output, cache only, and a
	// session record whose input equals the cache read.
	records := []*SessionRecord{
		codexUsageRecord("parity-cache-only", "gpt-5.5", start, 100, 0, 100),
		// A second session with ordinary tokens, so the assertion is not satisfied
		// by everything being zero.
		codexUsageRecord("parity-normal", "gpt-5.5", start.Add(time.Minute), 40, 7, 3),
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	refreshCodexImputed(t, s)

	since := start.Add(-time.Hour)
	until := start.Add(time.Hour)
	f := SessionOverviewFilter{Since: &since, Until: &until}

	// Surface 1 and 2: the ungrouped aggregate, which both the dashboard and the
	// ungrouped Open API response are built from.
	usage, err := s.UsageAggregates(ctx, f)
	if err != nil {
		t.Fatalf("UsageAggregates: %v", err)
	}

	// Surface 3: the grouped response, built from visible_events.
	models, err := s.CostByModel(ctx, since, until, "", "", "")
	if err != nil {
		t.Fatalf("CostByModel: %v", err)
	}
	var groupedInput, groupedOutput int64
	for _, m := range models {
		groupedInput += m.InputTokens
		groupedOutput += m.OutputTokens
	}

	if usage.InputTokens != groupedInput {
		t.Errorf("input tokens disagree: ungrouped %d, group_by=model %d -- the same window reported two numbers",
			usage.InputTokens, groupedInput)
	}
	if usage.OutputTokens != groupedOutput {
		t.Errorf("output tokens disagree: ungrouped %d, group_by=model %d", usage.OutputTokens, groupedOutput)
	}
	// And the value itself has to be the normalized one. Agreement on the
	// cache-inclusive number would satisfy the checks above and still be the bug:
	// the cache-only session must contribute no visible input at all.
	//
	// 37 is the normal session's 40 raw input minus its 3 cache reads -- Codex
	// session-record input includes cache, the visible event excludes it. The
	// cache-only session's 100 contributes nothing, which is the whole point: on
	// the cache-inclusive path this total would be 140.
	if got, want := usage.InputTokens, int64(37); got != want {
		t.Errorf("input tokens = %d, want %d (the normal session's normalized input only; 140 would mean the cache-inclusive fallback was used)", got, want)
	}
}

// #543's last completion condition: a source fixture's totals have to match what
// every surface reports -- the usage aggregate the API answers from, the
// time-series the minute graph draws, and the cost aggregation.
//
// The issue is a compaction token_usage_record being dropped: 0.99% of tokens
// and 2.09% of cost in the observed window. A surface that reads a different
// source than the others hides that, because the number it shows is internally
// consistent. So the check is not "is each surface self-consistent" but "do they
// all equal the fixture".
func TestPgStore_CodexSurfacesMatchTheSourceFixture(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	start := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)

	// Three ordinary responses and one compaction-shaped record, which is the
	// row #543 found missing. Raw input includes cache; normalized input is the
	// difference.
	type row struct{ in, out, cache int }
	rows := []row{
		{in: 1200, out: 300, cache: 1000}, // normalized 200
		{in: 800, out: 150, cache: 700},   // normalized 100
		{in: 500, out: 90, cache: 450},    // normalized 50
		{in: 708, out: 106, cache: 689},   // the compaction record: normalized 19
	}
	var records []*SessionRecord
	var wantNormalizedInput, wantOutput int64
	for i, r := range rows {
		records = append(records, codexUsageRecord("fixture-parity", "gpt-5.5",
			start.Add(time.Duration(i)*time.Minute), r.in, r.out, r.cache))
		wantNormalizedInput += int64(r.in - r.cache)
		wantOutput += int64(r.out)
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	refreshCodexImputed(t, s)

	since := start.Add(-time.Hour)
	until := start.Add(time.Hour)

	// Surface 1 — the usage aggregate the API answers from.
	usage, err := s.UsageAggregates(ctx, SessionOverviewFilter{Since: &since, Until: &until})
	if err != nil {
		t.Fatalf("UsageAggregates: %v", err)
	}

	// Surface 2 — the time series the minute graph draws.
	series, err := s.TimeSeriesStats(ctx, EventFilter{Since: &since, Until: &until}, "day")
	if err != nil {
		t.Fatalf("TimeSeriesStats: %v", err)
	}
	var seriesInput, seriesOutput int64
	for _, p := range series {
		seriesInput += p.InputTokens
		seriesOutput += p.OutputTokens
	}

	// Surface 3 — the cost aggregation.
	models, err := s.CostByModel(ctx, since, until, "", "", "")
	if err != nil {
		t.Fatalf("CostByModel: %v", err)
	}
	var costInput, costOutput int64
	for _, m := range models {
		costInput += m.InputTokens
		costOutput += m.OutputTokens
	}

	for _, c := range []struct {
		surface string
		in, out int64
	}{
		{"usage aggregate", usage.InputTokens, usage.OutputTokens},
		{"time series", seriesInput, seriesOutput},
		{"cost by model", costInput, costOutput},
	} {
		if c.in != wantNormalizedInput {
			t.Errorf("%s: input %d, fixture %d -- a surface reporting less than the source is the shape of #543",
				c.surface, c.in, wantNormalizedInput)
		}
		if c.out != wantOutput {
			t.Errorf("%s: output %d, fixture %d", c.surface, c.out, wantOutput)
		}
	}
}
