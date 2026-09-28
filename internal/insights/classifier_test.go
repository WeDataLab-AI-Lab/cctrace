package insights

import "testing"

func TestClassifyPrompt(t *testing.T) {
	tests := []struct {
		name, prompt, want string
	}{
		{"planning", "먼저 구현 계획을 세우고 단계별로 설명해줘", "planning"},
		{"drafting", "Draft a release note for this feature", "drafting"},
		{"debugging", "fix this regression and investigate the stack trace", "debugging"},
		{"testing", "테스트를 추가하고 실패 원인을 확인해줘", "testing"},
		{"documentation", "API 문서를 업데이트해줘", "documentation"},
		{"implementation", "implement the new endpoint", "implementation"},
		{"unknown", "안녕하세요", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyPrompt(tt.prompt); got != tt.want {
				t.Fatalf("ClassifyPrompt(%q) = %q, want %q", tt.prompt, got, tt.want)
			}
		})
	}
}

func TestPromptFromRawSupportsClaudeAndCodexAndRejectsTools(t *testing.T) {
	tests := []struct {
		name, raw, want string
	}{
		{"claude", `{"message":{"role":"user","content":[{"type":"text","text":"write tests"}]}}`, "write tests"},
		{"codex", `{"payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the bug"}]}}`, "fix the bug"},
		{"tool result", `{"message":{"role":"user","content":[{"type":"tool_result","content":"failed"}]}}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PromptFromRaw([]byte(tt.raw)); got != tt.want {
				t.Fatalf("PromptFromRaw() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsTypedTurn(t *testing.T) {
	tests := []struct {
		name, agent, raw string
		want             bool
	}{
		{"claude plain text", "claude", `{"message":{"role":"user","content":"hi"}}`, true},
		{"claude text block (array-shaped, common case)", "claude", `{"message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`, true},
		{"claude tool result", "claude", `{"message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`, false},
		{"claude no message", "claude", `{}`, false},
		{"codex payload-shaped, no message.content at all", "codex", `{"payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the bug"}]}}`, true},
		{"agent defaults to claude when empty", "", `{"message":{"role":"user","content":"hi"}}`, true},
		{"malformed json", "claude", `not json`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTypedTurn(tt.agent, []byte(tt.raw)); got != tt.want {
				t.Fatalf("IsTypedTurn(%q, %q) = %v, want %v", tt.agent, tt.raw, got, tt.want)
			}
		})
	}
}

// A Codex user turn keeps its text under payload.content, not message.content.
// The isolation note in internal/store said PromptFromRaw could not read that and
// that Codex therefore always classified as "unknown"; it reads it fine, and the
// comment has been corrected. This pins the behaviour so the two cannot drift
// apart again (#429).
func TestCodexPayloadShapeClassifies(t *testing.T) {
	raw := []byte(`{"timestamp":"2026-09-11T05:19:52Z","type":"response_item",` +
		`"payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"테스트를 추가해줘"}]}}`)

	if got := PromptFromRaw(raw); got != "테스트를 추가해줘" {
		t.Fatalf("PromptFromRaw = %q, want the typed text", got)
	}
	if got := ClassifyPrompt(PromptFromRaw(raw)); got != "testing" {
		t.Errorf("ClassifyPrompt = %q, want testing", got)
	}
}
