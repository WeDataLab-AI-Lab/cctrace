package sessionlog

import (
	"encoding/json"
	"strings"
	"time"
)

// Record is a single line from a Claude Code session JSONL file.
type Record struct {
	Type             string          `json:"type"`
	Timestamp        time.Time       `json:"timestamp"`
	UUID             string          `json:"uuid"`
	ParentUUID       string          `json:"parentUuid,omitempty"`
	SessionID        string          `json:"sessionId"`
	CWD              string          `json:"cwd"`
	Version          string          `json:"version"`
	GitBranch        string          `json:"gitBranch"`
	IsSidechain      bool            `json:"isSidechain,omitempty"`
	AgentID          string          `json:"agentId,omitempty"`
	ForkedFrom       *ForkRef        `json:"forkedFrom,omitempty"`
	IsCompactSummary bool            `json:"isCompactSummary,omitempty"`
	IsMeta           bool            `json:"isMeta,omitempty"`
	PromptSource     string          `json:"promptSource,omitempty"`
	Entrypoint       string          `json:"entrypoint,omitempty"`
	AttributionSkill string          `json:"attributionSkill,omitempty"`
	Message          json.RawMessage `json:"message"`

	// RawLine is the line this record was parsed from, kept for identity only.
	// The struct has no catch-all, so metadata lines lose their payload on the way
	// in: two different last-prompt lines marshal back to identical bytes. Anything
	// that needs to tell such records apart has to look at what was actually read.
	RawLine []byte `json:"-"`
}

// ForkRef links a branched session back to its origin. Present on the first
// records of a session created via /branch: forkedFrom.sessionId is the origin
// session and forkedFrom.messageUuid is the message the branch diverged at.
type ForkRef struct {
	SessionID   string `json:"sessionId"`
	MessageUUID string `json:"messageUuid"`
}

// AssistantMessage is the parsed message for type="assistant".
type AssistantMessage struct {
	Model string     `json:"model"`
	Usage TokenUsage `json:"usage"`
}

// TokenUsage holds token counts from an assistant message.
type TokenUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// ParseCommandName extracts the slash command name from a user message.
// Returns empty string if the message is not a slash command invocation.
// Handles content like: <command-name>/oh-my-claudecode:ralph</command-name>
func (r *Record) ParseCommandName() string {
	if r.Type != "user" || len(r.Message) == 0 {
		return ""
	}
	// message.content is a string (not array) for command records
	var msg struct {
		Content interface{} `json:"content"`
	}
	if err := json.Unmarshal(r.Message, &msg); err != nil {
		return ""
	}
	content, ok := msg.Content.(string)
	if !ok {
		return ""
	}
	// Extract <command-name>...</command-name>
	start := strings.Index(content, "<command-name>")
	end := strings.Index(content, "</command-name>")
	if start == -1 || end == -1 || end <= start {
		return ""
	}
	name := content[start+len("<command-name>") : end]
	// Strip leading slash
	return strings.TrimPrefix(name, "/")
}

// IsToolResultUser reports whether a Claude user record is a tool result rather
// than a new human prompt. Claude stores tool results as role=user messages.
func (r *Record) IsToolResultUser() bool {
	if r.Type != "user" || len(r.Message) == 0 {
		return false
	}
	var msg struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(r.Message, &msg); err != nil || len(msg.Content) == 0 {
		return false
	}
	var items []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(msg.Content, &items); err != nil {
		return false
	}
	for _, item := range items {
		if item.Type == "tool_result" {
			return true
		}
	}
	return false
}

// ParseAssistantMessage parses the Message field for type="assistant".
// Returns nil if parsing fails.
func (r *Record) ParseAssistantMessage() *AssistantMessage {
	if r.Type != "assistant" || len(r.Message) == 0 {
		return nil
	}
	var m AssistantMessage
	if err := json.Unmarshal(r.Message, &m); err != nil {
		return nil
	}
	return &m
}
