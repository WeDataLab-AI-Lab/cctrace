package codexlog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAuditRetainsUsageWithOnlyDetailedCounters(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"cache_write_input_tokens", "reasoning_output_tokens"} {
		r := parseNewFormatLine([]byte(`{"type":"token_usage_record","payload":{"response_id":"response-one","turn_id":"turn-one","root_turn_id":"root-one","usage":{"`+field+`":7}}}`), &scanState{sessionID: "s"})
		if r == nil || r.RecordType != "usage" || !strings.Contains(string(r.Raw), field) {
			t.Fatalf("detailed usage %s was discarded: %+v", field, r)
		}
		if r.TurnID != "turn-one" || r.RootTurnID != "root-one" {
			t.Fatalf("explicit usage membership was lost: %+v", r)
		}
	}
}

func TestAuditPreservesDeclaredHistoryWithoutInstructions(t *testing.T) {
	t.Parallel()
	r := parseNewFormatLine([]byte(`{"type":"session_meta","payload":{"id":"child","parent_thread_id":"parent","thread_source":"subagent","history_mode":"paginated","history_base":{"thread_id":"parent","end_byte_offset":123,"end_ordinal_exclusive":4},"agent_path":"/root/worker","unrelated_secret":"do-not-send"}}`), &scanState{sessionID: "child"})
	if r == nil || r.RecordType != "context" {
		t.Fatalf("declared history was dropped: %+v", r)
	}
	var raw struct {
		Payload map[string]json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(r.Raw, &raw); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"parent_thread_id", "history_mode", "history_base", "agent_path"} {
		if len(raw.Payload[field]) == 0 {
			t.Errorf("lost %s", field)
		}
	}
	if strings.Contains(string(r.Raw), "do-not-send") {
		t.Fatal("unrelated metadata collected")
	}
}

func TestAuditPreservesTurnPermissionsWithoutInstructions(t *testing.T) {
	t.Parallel()
	r := parseNewFormatLine([]byte(`{"type":"turn_context","payload":{"turn_id":"t","root_turn_id":"root","model":"model","effort":"high","approval_policy":"never","sandbox_policy":{"type":"workspace-write"},"unrelated_secret":"do-not-send"}}`), &scanState{sessionID: "s", turnMetadataScanned: true, format: FormatNew})
	if r == nil || !strings.Contains(string(r.Raw), "approval_policy") || !strings.Contains(string(r.Raw), "sandbox_policy") || strings.Contains(string(r.Raw), "do-not-send") {
		t.Fatalf("incorrect permission provenance: %+v", r)
	}
}

// Lineage is a separate audit context; transcript gate tests still assert that
// inherited messages and usage never become the child's fresh work.
func transcriptAndUsage(records []*Record) []*Record {
	var result []*Record
	for _, r := range records {
		if r.RecordType != "context" {
			result = append(result, r)
		}
	}
	return result
}
