package store

// TaskClassifier derives a task_type label from a user turn's raw JSON payload.
// Classification is server-only by design: the concrete implementation
// (internal/insights.KeywordClassifier today, and whatever dictionary or model
// it grows to use for mecab-ko tokenization later) is injected only by
// cmd/cctraced -- see PgStore.SetTaskClassifier. cmd/cctrace never references
// this interface's only implementation, so internal/insights (and anything it
// comes to depend on) is absent from the client binary's dependency graph
// entirely, not merely dead code the linker happens to strip.
type TaskClassifier interface {
	// PromptFromRaw extracts the typed human text from a user turn's raw JSON,
	// or "" if raw carries no typed text (a tool_result block, a blank message,
	// or a shape the extractor does not recognize -- e.g. Codex's payload,
	// which callers must gate separately; see IsTypedTurn below).
	PromptFromRaw(raw []byte) string
	// Classify returns a stable task label for prompt. It never returns "" --
	// "unknown" is the label for "no rule matched", a real answer distinct from
	// "not yet classified" (which callers represent as "").
	Classify(prompt string) string
	// Version tags every row this classifier labels, so a future rule-set
	// change can tell which rows still reflect an older version.
	Version() string
}

// IsTypedTurn reports whether raw is a genuine classification candidate. Codex is
// accepted unconditionally: its typed text lives under payload.content rather than
// message.content, and this package holds no parser of its own.
//
// The comment here used to say PromptFromRaw could not read that shape and that
// Codex therefore classified against an empty prompt. That is not true and was
// not true when it was written -- PromptFromRaw falls back to payload, and a real
// Codex user turn
// ({"payload":{"role":"user","content":[{"type":"input_text","text":"테스트를 추가해줘"}]}})
// comes back parsed and classifies as "testing" (#429). The unconditional accept
// still stands, because "is this a candidate" is a cheaper question than parsing
// and the classifier answers the parse question next anyway.
func IsTypedTurn(c TaskClassifier, agent string, raw []byte) bool {
	if agent == "codex" {
		return true
	}
	return c.PromptFromRaw(raw) != ""
}
