package sessionlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// record.go tests
// ---------------------------------------------------------------------------

func TestParseAssistantMessage_ValidJSON(t *testing.T) {
	body := `{
		"role": "assistant",
		"content": [{"type":"text","text":"hello"}],
		"model": "claude-3-5-sonnet-20241022",
		"usage": {
			"input_tokens": 100,
			"output_tokens": 50,
			"cache_creation_input_tokens": 10,
			"cache_read_input_tokens": 5
		}
	}`
	r := &Record{
		Type:    "assistant",
		Message: json.RawMessage(body),
	}
	msg := r.ParseAssistantMessage()
	if msg == nil {
		t.Fatal("expected non-nil AssistantMessage, got nil")
	}
	if msg.Model != "claude-3-5-sonnet-20241022" {
		t.Errorf("Model: got %q, want %q", msg.Model, "claude-3-5-sonnet-20241022")
	}
	if msg.Usage.InputTokens != 100 {
		t.Errorf("InputTokens: got %d, want 100", msg.Usage.InputTokens)
	}
	if msg.Usage.OutputTokens != 50 {
		t.Errorf("OutputTokens: got %d, want 50", msg.Usage.OutputTokens)
	}
	if msg.Usage.CacheCreationInputTokens != 10 {
		t.Errorf("CacheCreationInputTokens: got %d, want 10", msg.Usage.CacheCreationInputTokens)
	}
	if msg.Usage.CacheReadInputTokens != 5 {
		t.Errorf("CacheReadInputTokens: got %d, want 5", msg.Usage.CacheReadInputTokens)
	}
}

func TestParseAssistantMessage_NonAssistantType(t *testing.T) {
	body := `{"model":"claude-3","usage":{"input_tokens":1}}`
	r := &Record{
		Type:    "user",
		Message: json.RawMessage(body),
	}
	msg := r.ParseAssistantMessage()
	if msg != nil {
		t.Errorf("expected nil for non-assistant record, got %+v", msg)
	}
}

func TestParseAssistantMessage_MalformedJSON(t *testing.T) {
	r := &Record{
		Type:    "assistant",
		Message: json.RawMessage(`{not valid json`),
	}
	msg := r.ParseAssistantMessage()
	if msg != nil {
		t.Errorf("expected nil for malformed JSON, got %+v", msg)
	}
}

func TestRecord_AttributionSkill(t *testing.T) {
	var r Record
	line := []byte(`{"type":"assistant","sessionId":"sess-1","attributionSkill":"playwright-cli","message":{"model":"claude"}}`)
	if err := json.Unmarshal(line, &r); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if r.AttributionSkill != "playwright-cli" {
		t.Fatalf("AttributionSkill = %q, want playwright-cli", r.AttributionSkill)
	}
}

func TestIsToolResultUser(t *testing.T) {
	cases := []struct {
		name string
		rec  *Record
		want bool
	}{
		{
			name: "tool result array",
			rec: &Record{
				Type:    "user",
				Message: json.RawMessage(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}`),
			},
			want: true,
		},
		{
			name: "human string prompt",
			rec: &Record{
				Type:    "user",
				Message: json.RawMessage(`{"role":"user","content":"hello"}`),
			},
			want: false,
		},
		{
			name: "assistant",
			rec: &Record{
				Type:    "assistant",
				Message: json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"hi"}]}`),
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rec.IsToolResultUser(); got != tc.want {
				t.Fatalf("IsToolResultUser() = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// scanner.go tests
// ---------------------------------------------------------------------------

// writeFile is a helper that writes content to a temp file and returns its path.
func writeFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "sessionlog_*.jsonl")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return f.Name()
}

// sampleLine returns a minimal valid Record JSON line (with or without trailing newline).
func sampleLine(sessionID string, withNewline bool) string {
	line := `{"type":"assistant","sessionId":"` + sessionID + `","message":{"model":"m","usage":{"input_tokens":1,"output_tokens":2}}}`
	if withNewline {
		line += "\n"
	}
	return line
}

func TestScanFile_Empty(t *testing.T) {
	path := writeFile(t, "")
	records, newOffset, err := ScanFile(path, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("expected 0 records, got %d", len(records))
	}
	if newOffset != 0 {
		t.Errorf("expected offset 0, got %d", newOffset)
	}
}

func TestScanFile_TwoCompleteRecords(t *testing.T) {
	line1 := sampleLine("session1", true)
	line2 := sampleLine("session2", true)
	content := line1 + line2
	path := writeFile(t, content)

	records, newOffset, err := ScanFile(path, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
	if records[0].SessionID != "session1" {
		t.Errorf("record[0].SessionID: got %q, want %q", records[0].SessionID, "session1")
	}
	if records[1].SessionID != "session2" {
		t.Errorf("record[1].SessionID: got %q, want %q", records[1].SessionID, "session2")
	}
	expectedOffset := int64(len(content))
	if newOffset != expectedOffset {
		t.Errorf("newOffset: got %d, want %d", newOffset, expectedOffset)
	}
}

func TestScanFile_PartialLastLine(t *testing.T) {
	line1 := sampleLine("session1", true)
	partial := sampleLine("session2", false) // no trailing newline
	content := line1 + partial
	path := writeFile(t, content)

	records, newOffset, err := ScanFile(path, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected only the complete record, got %d", len(records))
	}
	if records[0].SessionID != "session1" {
		t.Errorf("record[0].SessionID: got %q, want %q", records[0].SessionID, "session1")
	}
	// Offset must stop before the partial line (only complete lines advance it).
	expectedOffset := int64(len(line1))
	if newOffset != expectedOffset {
		t.Errorf("newOffset: got %d, want %d (should not include partial line)", newOffset, expectedOffset)
	}
}

func TestScanFile_IncrementalRead(t *testing.T) {
	line1 := sampleLine("session1", true)
	path := writeFile(t, line1)

	// First scan: get the first record.
	records1, offset1, err := ScanFile(path, 0)
	if err != nil {
		t.Fatalf("first scan error: %v", err)
	}
	if len(records1) != 1 {
		t.Fatalf("first scan: expected 1 record, got %d", len(records1))
	}

	// Append a second line to the file.
	line2 := sampleLine("session2", true)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatalf("OpenFile for append: %v", err)
	}
	if _, err := f.WriteString(line2); err != nil {
		t.Fatalf("append write: %v", err)
	}
	f.Close()

	// Second scan starting at offset1 should return only the new record.
	records2, _, err := ScanFile(path, offset1)
	if err != nil {
		t.Fatalf("second scan error: %v", err)
	}
	if len(records2) != 1 {
		t.Fatalf("second scan: expected 1 new record, got %d", len(records2))
	}
	if records2[0].SessionID != "session2" {
		t.Errorf("second scan record SessionID: got %q, want %q", records2[0].SessionID, "session2")
	}
}

// ---------------------------------------------------------------------------
// finder.go tests
// ---------------------------------------------------------------------------

func TestDefaultClaudeDir_EnvVar(t *testing.T) {
	want := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", want)

	got := DefaultClaudeDir()
	if got != want {
		t.Errorf("DefaultClaudeDir: got %q, want %q", got, want)
	}
}

func TestDefaultClaudeDir_Fallback(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("cannot determine home dir: %v", err)
	}
	want := filepath.Join(home, ".claude")
	got := DefaultClaudeDir()
	if got != want {
		t.Errorf("DefaultClaudeDir fallback: got %q, want %q", got, want)
	}
}

func TestProjectHash_FirstSegment(t *testing.T) {
	claudeDir := "/home/user/.claude"
	filePath := "/home/user/.claude/projects/-home-user-myapp/session.jsonl"
	got := ProjectHash(claudeDir, filePath)
	want := "-home-user-myapp"
	if got != want {
		t.Errorf("ProjectHash: got %q, want %q", got, want)
	}
}

func TestProjectHash_NestedSubagentPath(t *testing.T) {
	claudeDir := "/home/user/.claude"
	filePath := "/home/user/.claude/projects/-home-user-myapp/subagents/sub.jsonl"
	got := ProjectHash(claudeDir, filePath)
	want := "-home-user-myapp"
	if got != want {
		t.Errorf("ProjectHash nested: got %q, want %q", got, want)
	}
}

func TestProjectHash_UnrelatedPath(t *testing.T) {
	claudeDir := "/home/user/.claude"
	filePath := "/some/other/path/session.jsonl"
	// filepath.Rel will succeed but produce a relative path starting with "../...";
	// the first segment will not be the project hash but we should not panic.
	got := ProjectHash(claudeDir, filePath)
	// Just ensure it doesn't panic and returns a string (content varies by OS).
	_ = got
}
