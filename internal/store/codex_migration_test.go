package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func intPtr(v int) *int {
	return &v
}

// TestMigration_AgentColumn verifies agent and billing_provider columns exist
// in otel_events and session_records after migration.
func TestMigration_AgentColumn(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()

	type colCheck struct {
		table  string
		column string
	}
	checks := []colCheck{
		{"otel_events", "agent"},
		{"otel_events", "billing_provider"},
		{"session_records", "agent"},
		{"session_records", "billing_provider"},
		{"projects", "agent"},
	}

	for _, c := range checks {
		var exists bool
		err := s.pool.QueryRow(ctx,
			`SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = $1 AND column_name = $2
			)`, c.table, c.column,
		).Scan(&exists)
		if err != nil {
			t.Fatalf("query %s.%s: %v", c.table, c.column, err)
		}
		if !exists {
			t.Errorf("column %s.%s does not exist", c.table, c.column)
		}
	}
}

// TestMigration_ProjectsCompositeKey verifies (agent, project_hash) is the PK.
func TestMigration_ProjectsCompositeKey(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()

	rows, err := s.pool.Query(ctx,
		`SELECT column_name FROM information_schema.key_column_usage
		 WHERE table_name = 'projects' AND constraint_name LIKE '%pkey%'
		 ORDER BY ordinal_position`,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, col)
	}

	if len(cols) != 2 {
		t.Fatalf("expected 2 PK columns (agent, project_hash), got %v", cols)
	}
	if cols[0] != "agent" || cols[1] != "project_hash" {
		t.Errorf("PK columns = %v, want [agent project_hash]", cols)
	}
}

// TestInsertSessionRecord_WithAgent inserts a Codex session record and reads it back.
func TestInsertSessionRecord_WithAgent(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	rec := &SessionRecord{
		Ts:              time.Now().UTC().Truncate(time.Millisecond),
		SessionID:       "codex-ses-001",
		ProjectHash:     "hash-abc",
		RecordType:      "user",
		ProfileEmail:    "user@example.com",
		UserID:          "uid-codex-001",
		Agent:           "codex",
		BillingProvider: "openai",
	}

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{rec}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	var agent, billing string
	err := s.pool.QueryRow(ctx,
		`SELECT agent, billing_provider FROM session_records WHERE session_id = $1`,
		"codex-ses-001",
	).Scan(&agent, &billing)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if agent != "codex" {
		t.Errorf("agent = %q, want codex", agent)
	}
	if billing != "openai" {
		t.Errorf("billing_provider = %q, want openai", billing)
	}
}

// TestInsertSessionRecord_DefaultAgent verifies empty Agent defaults to "claude".
func TestInsertSessionRecord_DefaultAgent(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	rec := &SessionRecord{
		Ts:           time.Now().UTC().Truncate(time.Millisecond),
		SessionID:    "claude-ses-001",
		ProjectHash:  "hash-xyz",
		RecordType:   "user",
		ProfileEmail: "user@example.com",
		UserID:       "uid-001",
		// Agent and BillingProvider intentionally empty
	}

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{rec}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	var agent, billing string
	err := s.pool.QueryRow(ctx,
		`SELECT agent, billing_provider FROM session_records WHERE session_id = $1`,
		"claude-ses-001",
	).Scan(&agent, &billing)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if agent != "claude" {
		t.Errorf("agent = %q, want claude", agent)
	}
	if billing != "anthropic" {
		t.Errorf("billing_provider = %q, want anthropic", billing)
	}
}

// TestInsertOtelEvent_WithAgent inserts a Codex OTEL event and reads it back.
func TestInsertOtelEvent_WithAgent(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	e := &OtelEvent{
		Ts:              time.Now().UTC().Truncate(time.Millisecond),
		EventName:       "api_request",
		SessionID:       "codex-otel-ses-001",
		ProfileEmail:    "user@example.com",
		Agent:           "codex",
		BillingProvider: "openai",
	}

	if err := s.InsertEvent(ctx, e); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}

	var agent, billing string
	err := s.pool.QueryRow(ctx,
		`SELECT agent, billing_provider FROM otel_events WHERE session_id = $1`,
		"codex-otel-ses-001",
	).Scan(&agent, &billing)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if agent != "codex" {
		t.Errorf("agent = %q, want codex", agent)
	}
	if billing != "openai" {
		t.Errorf("billing_provider = %q, want openai", billing)
	}
}

func TestUnifiedEvents_CodexModelPricing(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	cases := []struct {
		model      string
		inputRate  float64
		cacheRate  float64
		outputRate float64
	}{
		// The gpt-5.6 rates are the post-cut ones: these records are stamped now,
		// and the rate table is temporal, so today's events are billed at today's
		// price. The pre-cut rates still exist at effective_from '-infinity' and are
		// what an event from before the cut-over gets -- see
		// TestCodexImputedCost_PricesEachEventAtItsEraRate.
		{"gpt-5.6-sol", 4.00, 0.40, 20.00},
		{"gpt-5.6-terra", 2.00, 0.20, 12.00},
		{"gpt-5.6-luna", 0.20, 0.02, 1.20},
		{"gpt-5.5", 5.00, 0.50, 30.00},
		{"gpt-5.4", 2.50, 0.25, 15.00},
		{"gpt-5.4-mini", 0.75, 0.075, 4.50},
		{"gpt-5.3-codex", 1.75, 0.175, 14.00},
		{"gpt-5.2-codex", 1.75, 0.175, 14.00},
		{"gpt-5-codex", 1.25, 0.125, 10.00},
		{"gpt-5.1-codex-mini", 0.25, 0.025, 2.00},
		{"codex-mini-latest", 1.50, 0.375, 6.00},
	}

	input, cached, output := 1000, 400, 100
	for i, tc := range cases {
		rec := &SessionRecord{
			Ts:              time.Now().UTC().Truncate(time.Millisecond).Add(time.Duration(i) * time.Millisecond),
			SessionID:       "codex-cost-001",
			RecordType:      "usage",
			ProfileEmail:    "user@example.com",
			UserID:          "uid-codex-001",
			Model:           tc.model,
			InputTokens:     &input,
			OutputTokens:    &output,
			CacheReadTokens: &cached,
			Agent:           "codex",
			BillingProvider: "openai",
		}
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{rec}); err != nil {
			t.Fatalf("InsertSessionRecords %s: %v", tc.model, err)
		}
	}
	refreshCodexImputed(t, s)

	rows, err := s.pool.Query(ctx,
		`SELECT model, cost_usd FROM unified_events WHERE session_id = $1 ORDER BY model`,
		"codex-cost-001")
	if err != nil {
		t.Fatalf("query unified_events: %v", err)
	}
	defer rows.Close()

	got := make(map[string]float64)
	for rows.Next() {
		var model string
		var cost float64
		if err := rows.Scan(&model, &cost); err != nil {
			t.Fatalf("scan unified_events: %v", err)
		}
		got[model] = cost
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows error: %v", err)
	}

	for _, tc := range cases {
		want := ((float64(input-cached) * tc.inputRate) + (float64(cached) * tc.cacheRate) + (float64(output) * tc.outputRate)) / 1000000.0
		if got[tc.model] < want-0.0000001 || got[tc.model] > want+0.0000001 {
			t.Fatalf("%s cost = %.8f, want %.8f", tc.model, got[tc.model], want)
		}
	}
}

func TestUnifiedEvents_CodexSkipsDuplicateTotalSnapshots(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Millisecond)
	records := []*SessionRecord{
		{
			Ts:              now,
			SessionID:       "codex-duplicate-001",
			RecordType:      "usage",
			ProfileEmail:    "user@example.com",
			UserID:          "uid-codex-001",
			Model:           "gpt-5.5",
			InputTokens:     intPtr(100),
			OutputTokens:    intPtr(20),
			CacheReadTokens: intPtr(40),
			Raw:             json.RawMessage(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`),
			Agent:           "codex",
			BillingProvider: "openai",
		},
		{
			Ts:              now.Add(time.Millisecond),
			SessionID:       "codex-duplicate-001",
			RecordType:      "usage",
			ProfileEmail:    "user@example.com",
			UserID:          "uid-codex-001",
			Model:           "gpt-5.5",
			InputTokens:     intPtr(100),
			OutputTokens:    intPtr(20),
			CacheReadTokens: intPtr(40),
			Raw:             json.RawMessage(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`),
			Agent:           "codex",
			BillingProvider: "openai",
		},
		{
			Ts:              now.Add(2 * time.Millisecond),
			SessionID:       "codex-duplicate-001",
			RecordType:      "usage",
			ProfileEmail:    "user@example.com",
			UserID:          "uid-codex-001",
			Model:           "gpt-5.5",
			InputTokens:     intPtr(75),
			OutputTokens:    intPtr(5),
			CacheReadTokens: intPtr(30),
			Raw:             json.RawMessage(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":175,"cached_input_tokens":70,"output_tokens":25},"last_token_usage":{"input_tokens":75,"cached_input_tokens":30,"output_tokens":5}}}}`),
			Agent:           "codex",
			BillingProvider: "openai",
		},
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	refreshCodexImputed(t, s)

	var count int
	var input, cached, output int64
	var cost float64
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(cost_usd),0)
		FROM unified_events
		WHERE session_id = $1`, "codex-duplicate-001").Scan(&count, &input, &cached, &output, &cost)
	if err != nil {
		t.Fatalf("query unified_events: %v", err)
	}
	if count != 2 {
		t.Fatalf("event count = %d, want 2", count)
	}
	if input != 105 || cached != 70 || output != 25 {
		t.Fatalf("tokens = %d/%d/%d, want 105/70/25", input, cached, output)
	}
	wantCost := ((float64(100-40) * 5.00) + (float64(40) * 0.50) + (float64(20) * 30.00) +
		(float64(75-30) * 5.00) + (float64(30) * 0.50) + (float64(5) * 30.00)) / 1000000.0
	if cost < wantCost-0.0000001 || cost > wantCost+0.0000001 {
		t.Fatalf("cost = %.8f, want %.8f", cost, wantCost)
	}
}

// TestUpsertProject_CompositeKey verifies same project_hash with different agents = separate rows.
func TestUpsertProject_CompositeKey(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	if err := s.UpsertProject(ctx, "claude", "hash-001", "my-project", "https://github.com/x/y", "", "", "", time.Time{}); err != nil {
		t.Fatalf("UpsertProject claude: %v", err)
	}
	if err := s.UpsertProject(ctx, "codex", "hash-001", "my-project", "https://github.com/x/y", "", "", "", time.Time{}); err != nil {
		t.Fatalf("UpsertProject codex: %v", err)
	}

	var count int
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM projects WHERE project_hash = 'hash-001'`,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("expected 2 rows (one per agent), got %d", count)
	}
}

// TestUpsertProject_Idempotent verifies re-upserting same (agent, project_hash) is a no-op.
func TestUpsertProject_Idempotent(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := s.UpsertProject(ctx, "claude", "hash-idem", "proj", "", "", "", "", time.Time{}); err != nil {
			t.Fatalf("UpsertProject #%d: %v", i, err)
		}
	}

	var count int
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM projects WHERE project_hash = 'hash-idem'`,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 row after 3 upserts, got %d", count)
	}
}

func TestUpsertProject_NormalizesRepository(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	if err := s.UpsertProject(ctx, "claude", "hash-repo", "cctrace", "https://github.com/ExampleOrg/cctrace.git", "", "", "", time.Time{}); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}

	var repositoryID, repositoryName string
	if err := s.pool.QueryRow(ctx,
		`SELECT repository_id, repository_name FROM projects WHERE agent = 'claude' AND project_hash = 'hash-repo'`,
	).Scan(&repositoryID, &repositoryName); err != nil {
		t.Fatal(err)
	}
	if repositoryID != "github.com/ExampleOrg/cctrace" {
		t.Fatalf("repository_id = %q, want github.com/ExampleOrg/cctrace", repositoryID)
	}
	if repositoryName != "cctrace" {
		t.Fatalf("repository_name = %q, want cctrace", repositoryName)
	}
}
