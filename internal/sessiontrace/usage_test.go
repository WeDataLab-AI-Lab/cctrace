package sessiontrace

import (
	"encoding/json"
	"testing"
)

func TestMergeResponseUsageUsesMaximumWithoutDoubleCounting(t *testing.T) {
	t.Parallel()
	values := []any{map[string]any{"input_tokens": json.Number("10"), "output_tokens": json.Number("1"), "cache_creation": map[string]any{"ephemeral_1h_input_tokens": json.Number("0")}}, map[string]any{"input_tokens": json.Number("10"), "output_tokens": json.Number("23"), "cache_creation": map[string]any{"ephemeral_1h_input_tokens": json.Number("4")}}}
	got := MergeResponseUsage(values)
	if got["input_tokens"] != json.Number("10") || got["output_tokens"] != json.Number("23") || got["cache_creation"].(map[string]any)["ephemeral_1h_input_tokens"] != json.Number("4") {
		t.Fatalf("usage=%+v", got)
	}
	if _, exists := got["cache_write_input_tokens"]; exists {
		t.Fatal("unobserved field invented")
	}
}
