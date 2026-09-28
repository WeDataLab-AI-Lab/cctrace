package omosyncer

import (
	"encoding/json"
	"testing"

	"cctrace/internal/omolog"
)

func TestToStoreRecord_ToolIdentity(t *testing.T) {
	cases := []struct {
		recordType       string
		wantName, wantID string
	}{
		{"tool_result", "bash", "tc1"},
		{"assistant", "", ""},
	}
	for _, c := range cases {
		r := &omolog.Record{RecordType: c.recordType, ToolName: "bash", ToolCallID: "tc1", Raw: json.RawMessage(`{}`)}
		sr := toStoreRecord(r, "user@example.com", "uid-001", "s", "session.jsonl", "cli")
		if sr.ToolName != c.wantName || sr.ToolCallID != c.wantID {
			t.Errorf("%s: ToolName = %q, ToolCallID = %q; want %q, %q", c.recordType, sr.ToolName, sr.ToolCallID, c.wantName, c.wantID)
		}
	}
}
