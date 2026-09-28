package omolog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeOmoFile(t *testing.T, lines []string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	var content string
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func headerLine(cwd, title string) string {
	line := `{"type":"session","version":3,"id":"019f0000-0000-7000-8000-000000000101","timestamp":"2026-01-01T00:00:00.000Z","cwd":"` + cwd + `"`
	if title != "" {
		line += `,"title":"` + title + `","titleSource":"auto"`
	}
	return line + `}`
}

func TestParseLine_HeaderCWDAndTitle(t *testing.T) {
	state := &scanState{sessionID: "sess-1"}
	rec := parseLine([]byte(headerLine("/Users/alice/repo", "my title")), state)
	if rec != nil {
		t.Fatalf("header line should not produce a record, got %+v", rec)
	}
	if state.cwd != "/Users/alice/repo" {
		t.Errorf("state.cwd = %q, want /Users/alice/repo", state.cwd)
	}
	if state.title != "my title" {
		t.Errorf("state.title = %q, want %q", state.title, "my title")
	}
}

func TestParseLine_HeaderTitleAbsent(t *testing.T) {
	state := &scanState{sessionID: "sess-1"}
	rec := parseLine([]byte(headerLine("/Users/alice/repo", "")), state)
	if rec != nil {
		t.Fatalf("header line should not produce a record, got %+v", rec)
	}
	if state.title != "" {
		t.Errorf("state.title = %q, want empty when absent from header", state.title)
	}
}

func TestParseLine_RoleMapping(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{
			"user",
			`{"id":"a1","parentId":null,"timestamp":"2026-08-12T16:33:31.269Z","type":"message","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`,
			"user",
		},
		{
			"assistant",
			`{"id":"a2","parentId":"a1","timestamp":"2026-08-12T16:33:43.677Z","type":"message","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"model":"claude-opus-4-8","provider":"claude-sdk-oauth","usage":{"input":2,"output":822,"cost":{"total":0.26}}}}`,
			"assistant",
		},
		{
			"toolResult",
			`{"id":"a3","parentId":"a2","timestamp":"2026-08-12T16:33:43.927Z","type":"message","message":{"role":"toolResult","toolCallId":"toolu_1","toolName":"bash","content":[{"type":"text","text":"output"}],"isError":false}}`,
			"tool_result",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			state := &scanState{sessionID: "sess-1"}
			rec := parseLine([]byte(c.line), state)
			if rec == nil {
				t.Fatal("expected a record, got nil")
			}
			if rec.RecordType != c.want {
				t.Errorf("RecordType = %q, want %q", rec.RecordType, c.want)
			}
		})
	}
}

func TestParseLine_AssistantUsage(t *testing.T) {
	// The parser does not filter by provider - it reports usage exactly as
	// the file records it, regardless of which provider produced the record.
	// Any provider-based accounting decision belongs to the syncer.
	cases := []struct {
		name     string
		provider string
	}{
		{"claude-sdk-oauth", "claude-sdk-oauth"},
		{"openai-codex", "openai-codex"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			line := `{"id":"a2","parentId":"a1","timestamp":"2026-08-12T16:33:43.677Z","type":"message","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"model":"claude-opus-4-8","provider":"` + c.provider + `","stopReason":"toolUse","usage":{"input":2,"output":822,"cacheRead":16075,"cacheWrite":37808,"totalTokens":54707,"cost":{"input":0.00001,"output":0.02055,"cacheRead":0.008,"cacheWrite":0.236,"total":0.2649}}}}`
			state := &scanState{sessionID: "sess-1"}
			rec := parseLine([]byte(line), state)
			if rec == nil {
				t.Fatal("expected a record, got nil")
			}
			if rec.Model != "claude-opus-4-8" {
				t.Errorf("Model = %q, want claude-opus-4-8", rec.Model)
			}
			if rec.Provider != c.provider {
				t.Errorf("Provider = %q, want %q", rec.Provider, c.provider)
			}
			if rec.Content != "hi" {
				t.Errorf("Content = %q, want hi", rec.Content)
			}
			if rec.InputTokens == nil || *rec.InputTokens != 2 {
				t.Errorf("InputTokens = %v, want 2", rec.InputTokens)
			}
			if rec.OutputTokens == nil || *rec.OutputTokens != 822 {
				t.Errorf("OutputTokens = %v, want 822", rec.OutputTokens)
			}
			if rec.CacheReadTokens == nil || *rec.CacheReadTokens != 16075 {
				t.Errorf("CacheReadTokens = %v, want 16075", rec.CacheReadTokens)
			}
			if rec.CacheWriteTokens == nil || *rec.CacheWriteTokens != 37808 {
				t.Errorf("CacheWriteTokens = %v, want 37808", rec.CacheWriteTokens)
			}
			if rec.TotalTokens == nil || *rec.TotalTokens != 54707 {
				t.Errorf("TotalTokens = %v, want 54707", rec.TotalTokens)
			}
			if rec.CostUSD == nil || *rec.CostUSD != 0.2649 {
				t.Errorf("CostUSD = %v, want 0.2649", rec.CostUSD)
			}
			// Raw preserves the usage object verbatim.
			if !strings.Contains(string(rec.Raw), `"usage":{"input":2`) {
				t.Errorf("Raw does not preserve the usage object: %s", rec.Raw)
			}
		})
	}
}

func TestParseLine_AssistantUsageAbsent(t *testing.T) {
	line := `{"id":"a2","parentId":"a1","timestamp":"2026-08-12T16:33:43.677Z","type":"message","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"model":"claude-opus-4-8","provider":"claude-sdk-oauth"}}`
	state := &scanState{sessionID: "sess-1"}
	rec := parseLine([]byte(line), state)
	if rec == nil {
		t.Fatal("expected a record, got nil")
	}
	if rec.InputTokens != nil || rec.OutputTokens != nil || rec.CacheReadTokens != nil ||
		rec.CacheWriteTokens != nil || rec.TotalTokens != nil || rec.CostUSD != nil {
		t.Errorf("expected all usage pointers nil when .message.usage is absent, got %+v", rec)
	}
}

func TestParseLine_ToolResultFields(t *testing.T) {
	line := `{"id":"a3","parentId":"a2","timestamp":"2026-08-12T16:33:43.927Z","type":"message","message":{"role":"toolResult","toolCallId":"toolu_01XWR4fC9MUB921bogHEpyQy","toolName":"bash","content":[{"type":"text","text":"origin\thttp://github.com"}],"details":{"status":"completed"},"isError":true}}`
	state := &scanState{sessionID: "sess-1"}
	rec := parseLine([]byte(line), state)
	if rec == nil {
		t.Fatal("expected a record, got nil")
	}
	if rec.ToolName != "bash" {
		t.Errorf("ToolName = %q, want bash", rec.ToolName)
	}
	if rec.ToolCallID != "toolu_01XWR4fC9MUB921bogHEpyQy" {
		t.Errorf("ToolCallID = %q, want toolu_01XWR4fC9MUB921bogHEpyQy", rec.ToolCallID)
	}
	if !rec.IsError {
		t.Error("IsError = false, want true")
	}
}

func TestParseLine_ContentExtraction(t *testing.T) {
	t.Run("string form", func(t *testing.T) {
		line := `{"id":"a1","parentId":null,"timestamp":"2026-08-12T16:33:31.269Z","type":"message","message":{"role":"user","content":"plain string content"}}`
		state := &scanState{sessionID: "sess-1"}
		rec := parseLine([]byte(line), state)
		if rec == nil || rec.Content != "plain string content" {
			t.Fatalf("Content = %+v, want plain string content", rec)
		}
	})

	t.Run("block array form with text and thinking", func(t *testing.T) {
		line := `{"id":"a2","parentId":"a1","timestamp":"2026-08-12T16:33:43.677Z","type":"message","message":{"role":"assistant","content":[{"type":"thinking","thinking":"pondering..."},{"type":"text","text":"the answer"}]}}`
		state := &scanState{sessionID: "sess-1"}
		rec := parseLine([]byte(line), state)
		if rec == nil {
			t.Fatal("expected a record, got nil")
		}
		if rec.Content != "the answer" {
			t.Errorf("Content = %q, want %q (thinking block excluded)", rec.Content, "the answer")
		}
	})
}

func TestParseLine_SkippedLineTypes(t *testing.T) {
	lines := []string{
		`{"type":"model_change","id":"m1","parentId":null,"timestamp":"2026-08-12T16:31:09.145Z","provider":"claude-sdk-oauth","modelId":"claude-opus-4-8"}`,
		`{"type":"thinking_level_change","id":"m2","parentId":"m1","timestamp":"2026-08-12T16:31:09.145Z","thinkingLevel":"medium"}`,
		`{"type":"compaction","id":"m3","parentId":"m2","timestamp":"2026-08-12T16:31:09.182Z"}`,
		`{"type":"custom","customType":"pi-rules.scan","data":{},"id":"m4","parentId":"m3","timestamp":"2026-08-12T16:31:09.182Z"}`,
		`{"type":"custom_message","customType":"senpi-task.usage","content":"...","display":false,"details":{},"id":"m5","parentId":"m4","timestamp":"2026-08-12T16:33:31.230Z"}`,
	}
	state := &scanState{sessionID: "sess-1"}
	for _, l := range lines {
		if rec := parseLine([]byte(l), state); rec != nil {
			t.Errorf("line %q should be skipped, got record %+v", l, rec)
		}
	}
}

func TestParseLine_MalformedJSON(t *testing.T) {
	state := &scanState{sessionID: "sess-1"}
	rec := parseLine([]byte(`{not valid json`), state)
	if rec != nil {
		t.Fatalf("expected nil for malformed JSON, got %+v", rec)
	}
}
