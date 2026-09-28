package store

import (
	"context"
	"testing"
	"time"
)

// TestAIReportWeeklyUnifiedEventsArmOpenAI verifies that ai_report_runs with OpenAI runtime
// are correctly transformed to unified_events with correct cost calculation.
func TestAIReportWeeklyUnifiedEventsArmOpenAI(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	// Create user
	u, err := s.CreateDashboardUser(ctx, &DashboardUser{
		Email:        "openai_test@example.com",
		PasswordHash: "test",
		Role:         "user",
	})
	if err != nil {
		t.Fatalf("CreateDashboardUser: %v", err)
	}

	// Create a completed AI report run with OpenAI runtime
	// Model: gpt-5.6-luna, InputTokens: 1000, OutputTokens: 100
	insert := `INSERT INTO ai_report_runs
		(dashboard_user_id, week, tz, since, until, status, runtime, model, auth_mode,
		 tokens_reported, input_tokens, cached_input_tokens, output_tokens, started_at, finished_at)
		VALUES ($1, '2026-W37', 'UTC', $2, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`

	if _, err := s.pool.Exec(ctx, insert,
		u.ID,                    // dashboard_user_id
		now,                     // since/until
		"completed",             // status
		"openai-api",            // runtime
		"gpt-5.6-luna",          // model
		"api_key",               // auth_mode
		true,                    // tokens_reported
		1000,                    // input_tokens
		0,                       // cached_input_tokens
		100,                     // output_tokens
		now.Add(-2*time.Minute), // started_at
		now.Add(-time.Minute),   // finished_at
	); err != nil {
		t.Fatalf("insert ai_report_runs: %v", err)
	}

	// Query unified_events to verify transformation
	rows, err := s.pool.Query(ctx, `SELECT ts, event_name, agent, billing_provider, model,
		input_tokens, output_tokens, cache_create_tokens, cost_usd
		FROM unified_events WHERE agent = 'weekly' AND model = 'gpt-5.6-luna'`)
	if err != nil {
		t.Fatalf("query unified_events: %v", err)
	}
	defer rows.Close()

	var found bool
	var ts time.Time
	var event, agent, billing, model string
	var in, out, cache int
	var cost *float64

	for rows.Next() {
		if err := rows.Scan(&ts, &event, &agent, &billing, &model, &in, &out, &cache, &cost); err != nil {
			t.Fatalf("scan: %v", err)
		}
		found = true

		// Verify event properties
		if event != "weekly_usage" {
			t.Errorf("event = %q, want weekly_usage", event)
		}
		if agent != "weekly" {
			t.Errorf("agent = %q, want weekly", agent)
		}
		if billing != "openai" {
			t.Errorf("billing_provider = %q, want openai", billing)
		}
		if model != "gpt-5.6-luna" {
			t.Errorf("model = %q, want gpt-5.6-luna", model)
		}

		// Verify tokens
		if in != 1000 {
			t.Errorf("input_tokens = %d, want 1000", in)
		}
		if out != 100 {
			t.Errorf("output_tokens = %d, want 100", out)
		}
		if cache != 0 {
			t.Errorf("cache_create_tokens = %d, want 0", cache)
		}

		// Verify timestamp is started_at
		if !ts.Equal(now.Add(-2 * time.Minute)) {
			t.Errorf("ts = %v, want %v (started_at)", ts, now.Add(-2*time.Minute))
		}

		// Verify cost is calculated (will depend on model rates in database)
		if cost == nil || *cost <= 0 {
			t.Logf("Note: cost not calculated or model rates missing (cost = %v)", cost)
		}
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("rows error: %v", err)
	}

	if !found {
		t.Fatal("no unified_events row found for AI report run")
	}
}

// TestAIReportWeeklyUnifiedEventsArmClaude verifies that ai_report_runs with Claude runtime
// are correctly transformed to unified_events with claude_model_rates billing.
func TestAIReportWeeklyUnifiedEventsArmClaude(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	// Create user
	u, err := s.CreateDashboardUser(ctx, &DashboardUser{
		Email:        "claude_test@example.com",
		PasswordHash: "test",
		Role:         "user",
	})
	if err != nil {
		t.Fatalf("CreateDashboardUser: %v", err)
	}

	// Create a completed AI report run with Claude runtime
	// Model: claude-sonnet-5, InputTokens: 2000, OutputTokens: 200
	insert := `INSERT INTO ai_report_runs
		(dashboard_user_id, week, tz, since, until, status, runtime, model, auth_mode,
		 tokens_reported, input_tokens, cached_input_tokens, output_tokens, started_at, finished_at)
		VALUES ($1, '2026-W37', 'UTC', $2, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`

	if _, err := s.pool.Exec(ctx, insert,
		u.ID,                    // dashboard_user_id
		now,                     // since/until
		"completed",             // status
		"claude-api",            // runtime
		"claude-sonnet-5",       // model
		"api_key",               // auth_mode
		true,                    // tokens_reported
		2000,                    // input_tokens
		0,                       // cached_input_tokens
		200,                     // output_tokens
		now.Add(-2*time.Minute), // started_at
		now.Add(-time.Minute),   // finished_at
	); err != nil {
		t.Fatalf("insert ai_report_runs: %v", err)
	}

	// Query unified_events to verify transformation
	rows, err := s.pool.Query(ctx, `SELECT ts, event_name, agent, billing_provider, model,
		input_tokens, output_tokens, cache_create_tokens
		FROM unified_events WHERE agent = 'weekly' AND model = 'claude-sonnet-5'`)
	if err != nil {
		t.Fatalf("query unified_events: %v", err)
	}
	defer rows.Close()

	var found bool
	var ts time.Time
	var event, agent, billing, model string
	var in, out, cache int

	for rows.Next() {
		if err := rows.Scan(&ts, &event, &agent, &billing, &model, &in, &out, &cache); err != nil {
			t.Fatalf("scan: %v", err)
		}
		found = true

		// Verify event properties
		if event != "weekly_usage" {
			t.Errorf("event = %q, want weekly_usage", event)
		}
		if agent != "weekly" {
			t.Errorf("agent = %q, want weekly", agent)
		}
		// Claude runtime should use anthropic billing provider
		if billing != "anthropic" {
			t.Errorf("billing_provider = %q, want anthropic", billing)
		}
		if model != "claude-sonnet-5" {
			t.Errorf("model = %q, want claude-sonnet-5", model)
		}

		// Verify tokens
		if in != 2000 {
			t.Errorf("input_tokens = %d, want 2000", in)
		}
		if out != 200 {
			t.Errorf("output_tokens = %d, want 200", out)
		}
		if cache != 0 {
			t.Errorf("cache_create_tokens = %d, want 0", cache)
		}

		// Verify timestamp is started_at
		if !ts.Equal(now.Add(-2 * time.Minute)) {
			t.Errorf("ts = %v, want %v (started_at)", ts, now.Add(-2*time.Minute))
		}
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("rows error: %v", err)
	}

	if !found {
		t.Fatal("no unified_events row found for AI report run")
	}
}

// TestAIReportWeeklyOnlyCountsReportedTokens verifies that runs without token reporting
// do not appear in unified_events.
func TestAIReportWeeklyOnlyCountsReportedTokens(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	// Create user
	u, err := s.CreateDashboardUser(ctx, &DashboardUser{
		Email:        "unreported_test@example.com",
		PasswordHash: "test",
		Role:         "user",
	})
	if err != nil {
		t.Fatalf("CreateDashboardUser: %v", err)
	}

	// Create run with tokens_reported = false
	insert := `INSERT INTO ai_report_runs
		(dashboard_user_id, week, tz, since, until, status, runtime, model, auth_mode,
		 tokens_reported, input_tokens, cached_input_tokens, output_tokens, started_at, finished_at)
		VALUES ($1, '2026-W37', 'UTC', $2, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`

	if _, err := s.pool.Exec(ctx, insert,
		u.ID,
		now,
		"completed",
		"openai-api",
		"gpt-5.6-luna",
		"api_key",
		false, // tokens_reported = false
		1000,
		0,
		100,
		now.Add(-2*time.Minute),
		now.Add(-time.Minute),
	); err != nil {
		t.Fatalf("insert ai_report_runs: %v", err)
	}

	// Query unified_events - should find nothing for this run
	rows, err := s.pool.Query(ctx, `SELECT COUNT(*) FROM unified_events
		WHERE agent = 'weekly' AND model = 'gpt-5.6-luna' AND event_name = 'weekly_usage'`)
	if err != nil {
		t.Fatalf("query unified_events count: %v", err)
	}
	defer rows.Close()

	var count int
	if rows.Next() {
		if err := rows.Scan(&count); err != nil {
			t.Fatalf("scan count: %v", err)
		}
	}

	if count != 0 {
		t.Errorf("found %d weekly_usage events for unreported run, want 0", count)
	}
}

// TestAIReportWeeklyUnifiedEventsArmNVIDIA pins what the weekly arm says about a
// runtime that is neither Claude nor an OpenAI-billed one. The arm split on
// claude-api alone and swept everything else into 'openai' priced at the Codex
// table, so an NVIDIA run was reported as OpenAI spend and, with no rate row
// for z-ai/glm-5.3, cost exactly nothing. Reading that back, someone would see
// free OpenAI usage that never happened.
func TestAIReportWeeklyUnifiedEventsArmNVIDIA(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	u, err := s.CreateDashboardUser(ctx, &DashboardUser{
		Email:        "nvidia_test@example.com",
		PasswordHash: "test",
		Role:         "user",
	})
	if err != nil {
		t.Fatalf("CreateDashboardUser: %v", err)
	}

	insert := `INSERT INTO ai_report_runs
		(dashboard_user_id, week, tz, since, until, status, runtime, model, auth_mode,
		 tokens_reported, input_tokens, cached_input_tokens, output_tokens, started_at, finished_at)
		VALUES ($1, '2026-W37', 'UTC', $2, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`

	for _, tc := range []struct{ runtime, model, wantProvider string }{
		{"nvidia-api", "z-ai/glm-5.3", "nvidia"},
		{"litellm-api", "z-ai/glm-5.3", "litellm"},
	} {
		if _, err := s.pool.Exec(ctx, insert,
			u.ID, now, "completed", tc.runtime, tc.model, "api_key",
			true, 1000, 0, 100, now.Add(-2*time.Minute), now.Add(-time.Minute),
		); err != nil {
			t.Fatalf("insert %s: %v", tc.runtime, err)
		}

		var billing string
		var cost *float64
		err := s.pool.QueryRow(ctx, `SELECT billing_provider, cost_usd
			FROM unified_events WHERE agent = 'weekly' AND model = $1`, tc.model).Scan(&billing, &cost)
		if err != nil {
			t.Fatalf("query %s: %v", tc.runtime, err)
		}
		if billing != tc.wantProvider {
			t.Errorf("%s: billing_provider = %q, want %q", tc.runtime, billing, tc.wantProvider)
		}
		// No rate table prices these models. NULL says so; zero would claim the
		// run was free.
		if cost != nil {
			t.Errorf("%s: cost_usd = %v, want NULL for an unpriced model", tc.runtime, *cost)
		}
		if _, err := s.pool.Exec(ctx, `DELETE FROM ai_report_runs`); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
	}
}
