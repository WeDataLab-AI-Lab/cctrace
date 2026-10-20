package store

import (
	"strings"
	"testing"
)

func TestSessionAIRecordMigrationDefinesSevenTablesAndControls(t *testing.T) {
	// PgStore.Migrate executes dependentViews after migrations; the P5 DDL is
	// appended there so the timeline-related schema batch remains intact.
	ddl := strings.Join(append(append([]string{}, migrations...), dependentViews...), "\n")
	for _, table := range []string{
		"session_ai_records",
		"session_ai_record_jobs",
		"session_ai_record_budget_reservations",
		"session_ai_record_usage",
		"session_ai_record_consents",
		"session_ai_record_range_requests",
		"session_ai_record_vocab_pins",
	} {
		if !strings.Contains(ddl, "CREATE TABLE IF NOT EXISTS "+table) {
			t.Errorf("missing table migration for %s", table)
		}
	}
	for _, column := range []string{
		"source_input_hash", "basis_hash", "facts_hash", "covered_requests",
		"uncovered_requests", "search_text", "record_enabled", "record_runtime",
		"record_daily_token_cap", "record_model", "record_reasoning_effort",
	} {
		if !strings.Contains(ddl, column) {
			t.Errorf("missing session record persistence field %s", column)
		}
	}
}

func TestSessionAIRecordTablesAreSessionDeletionTargets(t *testing.T) {
	want := map[string]bool{
		"session_ai_records":     false,
		"session_ai_record_jobs": false,
	}
	for _, target := range sessionDeletionTargets {
		if _, ok := want[target.table]; ok {
			want[target.table] = true
		}
	}
	for table, found := range want {
		if !found {
			t.Errorf("%s is not part of the session deletion contract", table)
		}
	}
}
