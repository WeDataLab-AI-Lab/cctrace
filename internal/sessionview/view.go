// Package sessionview turns one stored session record into a provider-neutral
// shape the dashboard renders without knowing which agent wrote it.
//
// Claude, Codex, omo and gjc each store a different raw layout. Absorbing that
// here, on the server, keeps the difference out of every consumer: the web
// viewer only pairs tool calls with results and lays out lineage, and the Open
// API can report what a record is without exposing its body.
package sessionview

// Kind is what a record contributes to a conversation.
type Kind string

const (
	KindMessage    Kind = "message"     // a user or assistant turn, possibly carrying tool calls/results
	KindToolCall   Kind = "tool_call"   // a record whose only content is one or more tool invocations
	KindToolResult Kind = "tool_result" // a record whose only content is one or more tool outputs
	KindReasoning  Kind = "reasoning"   // model reasoning; text may be empty when the provider encrypts it
	KindHidden     Kind = "hidden"      // bookkeeping or scaffolding the conversation view skips
)

// View is the normalized form of one session record.
type View struct {
	Kind Kind   `json:"kind"`
	Role string `json:"role,omitempty"` // "user" | "assistant"
	Text string `json:"text,omitempty"`
	// Command is a slash command or skill invocation shown as a chip instead of text.
	Command        string       `json:"command,omitempty"`
	CommandArgs    string       `json:"command_args,omitempty"`
	AgentTask      bool         `json:"agent_task,omitempty"`      // instruction a parent agent sent to a subagent
	CompactSummary bool         `json:"compact_summary,omitempty"` // /compact context summary
	ToolCalls      []ToolCall   `json:"tool_calls,omitempty"`
	ToolResults    []ToolResult `json:"tool_results,omitempty"`
	// AnchorID identifies the record across pages and polls: the record uuid when
	// it has one, otherwise a hash of fields that do not change between reads.
	AnchorID string `json:"anchor_id"`
	// DedupeKey is equal for two stored copies of the same source line -- a legacy
	// copy and a later enriched one. It reads only what enrichment never touches
	// (ts, record_type, the message/payload body), unlike the rest of View, which
	// depends on enrichment-only columns such as prompt_source and is_meta.
	DedupeKey string `json:"dedupe_key"`
}

// ToolCall is one tool invocation. ID pairs it with its ToolResult.
type ToolCall struct {
	ID    string `json:"id,omitempty"`
	Name  string `json:"name"`
	Input string `json:"input,omitempty"`
}

// ToolResult is the output of one tool invocation. Content is never truncated.
type ToolResult struct {
	ID     string `json:"id,omitempty"`
	Output string `json:"output,omitempty"`
	// IsError is the recorded outcome. Nil means the provider records none
	// (Codex): not observed, which is not success.
	IsError *bool `json:"is_error,omitempty"`
	// Inferred marks a result recovered from prose an SDK harness re-serialised
	// into a user turn, not a genuine tool_result block. It may belong to a call
	// from an earlier session, so it only fills a pairing no genuine result makes
	// and is never shown on its own.
	Inferred bool `json:"inferred,omitempty"`
}
