package store

import (
	"context"
	"testing"
	"time"
)

// --- Part A: gjc/omo arms of unified_events ---

// TestPgStore_GjcOmoUnifiedEvents_CostFromRaw verifies that gjc/omo assistant
// records with tokens present pull their cost straight from raw's
// message.usage.cost.total, not from any rate table.
func TestPgStore_GjcOmoUnifiedEvents_CostFromRaw(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{
			Ts:              now,
			SessionID:       "gjc-cost-from-raw",
			RecordType:      "assistant",
			ProfileEmail:    "gjc@ex.com",
			UserID:          "uid-gjc",
			Model:           "claude-sonnet-5",
			InputTokens:     ptrInt(100),
			OutputTokens:    ptrInt(20),
			Agent:           "gjc",
			BillingProvider: "anthropic",
			Raw:             []byte(`{"message":{"usage":{"cost":{"total":1.2345}}}}`),
		},
		{
			Ts:              now,
			SessionID:       "omo-cost-from-raw",
			RecordType:      "assistant",
			ProfileEmail:    "omo@ex.com",
			UserID:          "uid-omo",
			Model:           "gpt-5.5",
			InputTokens:     ptrInt(50),
			OutputTokens:    ptrInt(10),
			Agent:           "omo",
			BillingProvider: "openai",
			Raw:             []byte(`{"message":{"usage":{"cost":{"total":0.6789}}}}`),
		},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	rows := queryUnifiedEventCosts(t, s, "gjc-cost-from-raw")
	if len(rows) != 1 {
		t.Fatalf("gjc rows = %d, want 1", len(rows))
	}
	if rows[0].costUSD == nil || *rows[0].costUSD < 1.2345-1e-9 || *rows[0].costUSD > 1.2345+1e-9 {
		t.Fatalf("gjc cost_usd = %v, want 1.2345", rows[0].costUSD)
	}
	if rows[0].eventName != "gjc_usage" {
		t.Fatalf("gjc event_name = %q, want gjc_usage", rows[0].eventName)
	}

	rows = queryUnifiedEventCosts(t, s, "omo-cost-from-raw")
	if len(rows) != 1 {
		t.Fatalf("omo rows = %d, want 1", len(rows))
	}
	if rows[0].costUSD == nil || *rows[0].costUSD < 0.6789-1e-9 || *rows[0].costUSD > 0.6789+1e-9 {
		t.Fatalf("omo cost_usd = %v, want 0.6789", rows[0].costUSD)
	}
	if rows[0].eventName != "omo_usage" {
		t.Fatalf("omo event_name = %q, want omo_usage", rows[0].eventName)
	}
}

// TestPgStore_GjcOmoUnifiedEvents_RawPathMiss verifies that a gjc/omo
// assistant record whose raw JSON lacks message.usage.cost.total surfaces as
// a NULL cost_usd, not a silent $0 row indistinguishable from real zero-cost
// usage.
func TestPgStore_GjcOmoUnifiedEvents_RawPathMiss(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{
			Ts:              now,
			SessionID:       "gjc-raw-path-miss",
			RecordType:      "assistant",
			ProfileEmail:    "gjc@ex.com",
			UserID:          "uid-gjc",
			Model:           "claude-sonnet-5",
			InputTokens:     ptrInt(100),
			OutputTokens:    ptrInt(20),
			Agent:           "gjc",
			BillingProvider: "anthropic",
			Raw:             []byte(`{"message":{"usage":{"input_tokens":100}}}`), // no cost.total
		},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	rows := queryUnifiedEventCosts(t, s, "gjc-raw-path-miss")
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].costUSD != nil {
		t.Fatalf("cost_usd = %v, want NULL (missing raw path must not silently price to $0)", *rows[0].costUSD)
	}
}

// TestPgStore_GjcOmoUnifiedEvents_DoubleCountGuard is the load-bearing
// regression test: an omo assistant record with NULL tokens (exactly what
// omosyncer.toStoreRecord produces for claude-sdk-oauth, per
// usageIsDoubleCounted) but a real cost sitting in raw must NOT contribute
// any cost to unified_events. That work is already billed once through the
// ordinary Claude Code path; resurrecting the cost from raw here would
// double-count it.
func TestPgStore_GjcOmoUnifiedEvents_DoubleCountGuard(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{
			Ts:              now,
			SessionID:       "omo-double-count-guard",
			RecordType:      "assistant",
			ProfileEmail:    "omo@ex.com",
			UserID:          "uid-omo",
			Model:           "claude-sonnet-5",
			InputTokens:     nil, // dropped by omosyncer as double-counted
			OutputTokens:    nil,
			Agent:           "omo",
			BillingProvider: "anthropic",
			Raw:             []byte(`{"message":{"usage":{"cost":{"total":9.99}}}}`), // raw still has it
		},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	rows := queryUnifiedEventCosts(t, s, "omo-double-count-guard")
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].costUSD != nil {
		t.Fatalf("cost_usd = %v, want NULL -- tokens were dropped as double-counted, cost must follow tokens not raw", *rows[0].costUSD)
	}
	if rows[0].inputTokens != nil || rows[0].outputTokens != nil {
		t.Fatalf("tokens = %v/%v, want NULL/NULL", rows[0].inputTokens, rows[0].outputTokens)
	}
}

type unifiedEventCostRow struct {
	eventName    string
	costUSD      *float64
	inputTokens  *int
	outputTokens *int
}

func queryUnifiedEventCosts(t *testing.T, s *PgStore, sessionID string) []unifiedEventCostRow {
	t.Helper()
	ctx := context.Background()
	rows, err := s.pool.Query(ctx,
		`SELECT event_name, cost_usd, input_tokens, output_tokens FROM unified_events WHERE session_id = $1 ORDER BY ts`,
		sessionID)
	if err != nil {
		t.Fatalf("query unified_events: %v", err)
	}
	defer rows.Close()
	var result []unifiedEventCostRow
	for rows.Next() {
		var r unifiedEventCostRow
		if err := rows.Scan(&r.eventName, &r.costUSD, &r.inputTokens, &r.outputTokens); err != nil {
			t.Fatalf("scan: %v", err)
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}
	return result
}

// --- Part B: ModelCategory axis is billing_provider, not agent ---

// TestPgStore_ModelCategory_NoRegressionForClaudeAndCodex pins that the
// rewrite from an agent-based axis to a billing_provider-based axis keeps
// selecting the exact same claude/codex rows the old axis did.
func TestPgStore_ModelCategory_NoRegressionForClaudeAndCodex(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{
			Ts: now, EventName: "api_request", SessionID: "cat-claude-anthropic",
			ProfileEmail: "a@ex.com", Model: "claude-sonnet-5",
			CostUSD: ptrFloat(1), InputTokens: ptrInt(10), OutputTokens: ptrInt(5),
			Agent: "claude", BillingProvider: "anthropic",
		},
		{
			Ts: now, EventName: "api_request", SessionID: "cat-claude-compatible",
			ProfileEmail: "b@ex.com", Model: "kimi-k2",
			CostUSD: ptrFloat(2), InputTokens: ptrInt(20), OutputTokens: ptrInt(6),
			Agent: "claude", BillingProvider: "other",
		},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{
			Ts: now, SessionID: "cat-codex", RecordType: "usage",
			ProfileEmail: "c@ex.com", Model: "gpt-5.5",
			InputTokens: ptrInt(30), OutputTokens: ptrInt(7),
			Agent: "codex", BillingProvider: "openai",
		},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	since := now.Add(-time.Minute)
	until := now.Add(time.Minute)
	refreshCodexImputed(t, s)

	anthropic, err := s.TimeSeriesStatsByModel(ctx, EventFilter{Since: &since, Until: &until, ModelCategory: "anthropic"}, "day")
	if err != nil {
		t.Fatalf("TimeSeriesStatsByModel(anthropic): %v", err)
	}
	assertOnlySessionsSeen(t, "anthropic", sumEventCount(anthropic), 1)

	compatible, err := s.TimeSeriesStatsByModel(ctx, EventFilter{Since: &since, Until: &until, ModelCategory: "compatible"}, "day")
	if err != nil {
		t.Fatalf("TimeSeriesStatsByModel(compatible): %v", err)
	}
	assertOnlySessionsSeen(t, "compatible", sumEventCount(compatible), 1)

	codex, err := s.TimeSeriesStatsByModel(ctx, EventFilter{Since: &since, Until: &until, ModelCategory: "codex"}, "day")
	if err != nil {
		t.Fatalf("TimeSeriesStatsByModel(codex): %v", err)
	}
	assertOnlySessionsSeen(t, "codex", sumEventCount(codex), 1)
}

// TestPgStore_ModelCategory_GjcOmoBucketByBillingProvider verifies gjc/omo
// rows -- invisible under the old agent-based axis -- now land in a
// ModelCategory bucket determined by their own billing_provider, even though
// they share a single agent value across multiple billing providers.
func TestPgStore_ModelCategory_GjcOmoBucketByBillingProvider(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{
			Ts: now, SessionID: "gjc-anthropic-bucket", RecordType: "assistant",
			ProfileEmail: "gjc-a@ex.com", Model: "claude-sonnet-5",
			InputTokens: ptrInt(10), OutputTokens: ptrInt(2),
			Agent: "gjc", BillingProvider: "anthropic",
			Raw: []byte(`{"message":{"usage":{"cost":{"total":0.1}}}}`),
		},
		{
			Ts: now, SessionID: "gjc-codex-bucket", RecordType: "assistant",
			ProfileEmail: "gjc-c@ex.com", Model: "gpt-5.5",
			InputTokens: ptrInt(10), OutputTokens: ptrInt(2),
			Agent: "gjc", BillingProvider: "openai",
			Raw: []byte(`{"message":{"usage":{"cost":{"total":0.2}}}}`),
		},
		{
			Ts: now, SessionID: "omo-compatible-bucket", RecordType: "assistant",
			ProfileEmail: "omo-o@ex.com", Model: "kimi-k2",
			InputTokens: ptrInt(10), OutputTokens: ptrInt(2),
			Agent: "omo", BillingProvider: "other",
			Raw: []byte(`{"message":{"usage":{"cost":{"total":0.3}}}}`),
		},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	since := now.Add(-time.Minute)
	until := now.Add(time.Minute)

	anthropic, err := s.TimeSeriesStatsByModel(ctx, EventFilter{Since: &since, Until: &until, ModelCategory: "anthropic"}, "day")
	if err != nil {
		t.Fatalf("TimeSeriesStatsByModel(anthropic): %v", err)
	}
	assertOnlySessionsSeen(t, "anthropic", sumEventCount(anthropic), 1)

	codex, err := s.TimeSeriesStatsByModel(ctx, EventFilter{Since: &since, Until: &until, ModelCategory: "codex"}, "day")
	if err != nil {
		t.Fatalf("TimeSeriesStatsByModel(codex): %v", err)
	}
	assertOnlySessionsSeen(t, "codex", sumEventCount(codex), 1)

	compatible, err := s.TimeSeriesStatsByModel(ctx, EventFilter{Since: &since, Until: &until, ModelCategory: "compatible"}, "day")
	if err != nil {
		t.Fatalf("TimeSeriesStatsByModel(compatible): %v", err)
	}
	assertOnlySessionsSeen(t, "compatible", sumEventCount(compatible), 1)
}

func sumEventCount(rows []*ModelDailyStat) int64 {
	var total int64
	for _, r := range rows {
		total += r.EventCount
	}
	return total
}

func assertOnlySessionsSeen(t *testing.T, bucket string, got, want int64) {
	t.Helper()
	if got != want {
		t.Fatalf("bucket %q event count = %d, want %d", bucket, got, want)
	}
}
