package omolog

import (
	"encoding/json"
	"strings"
	"time"
)

// Record is a normalized omo (senpi) session record.
//
// The parser is faithful to the file: it reports usage exactly as omo
// recorded it, on every record that carries a `.message.usage`. Whether a
// given record's usage is actually forwarded is a policy decision made
// downstream by the syncer, not by this package - omo mixes providers with
// different accounting status. Its "claude-sdk-oauth" provider spawns the
// `claude` binary, so that usage is already collected through the native
// Claude Code session logs and forwarding it here would double-count it.
// Other providers (e.g. "openai-codex") are not collected anywhere else, so
// dropping their usage at parse time would lose it. Record.Provider is what
// the syncer keys that decision on.
type Record struct {
	SessionID  string
	MessageID  string
	ParentID   string
	Timestamp  time.Time
	RecordType string // "user" | "assistant" | "tool_result"
	CWD        string
	Title      string
	Model      string
	Provider   string
	Content    string
	ToolName   string
	ToolCallID string
	IsError    bool

	// Usage and cost populate only on assistant records that carry a
	// `.message.usage` object.
	InputTokens      *int
	OutputTokens     *int
	CacheReadTokens  *int
	CacheWriteTokens *int
	TotalTokens      *int
	CostUSD          *float64

	Raw json.RawMessage
}

// scanState carries per-file mutable state gathered from the header line.
type scanState struct {
	sessionID string
	cwd       string
	title     string
}

// --- raw JSON structs ---

type rawLine struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	ParentID  string          `json:"parentId"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
	// Header-only fields (type == "session").
	CWD   string `json:"cwd"`
	Title string `json:"title"`
}

type messagePayload struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Model      string          `json:"model"`
	Provider   string          `json:"provider"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	IsError    bool            `json:"isError"`
	Usage      *usagePayload   `json:"usage"`
}

type usagePayload struct {
	Input       int          `json:"input"`
	Output      int          `json:"output"`
	CacheRead   int          `json:"cacheRead"`
	CacheWrite  int          `json:"cacheWrite"`
	TotalTokens int          `json:"totalTokens"`
	Cost        *costPayload `json:"cost"`
}

type costPayload struct {
	Total float64 `json:"total"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// --- parser ---

// parseLine parses a single omo session JSONL line. It returns nil for lines
// that carry no utterance - the header line (type "session", which updates
// state instead), "model_change", "thinking_level_change", "compaction",
// "custom", "custom_message", any unrecognized type, and lines that fail to
// parse as JSON.
func parseLine(line []byte, state *scanState) *Record {
	var rl rawLine
	if err := json.Unmarshal(line, &rl); err != nil {
		return nil
	}

	switch rl.Type {
	case "session":
		if rl.CWD != "" {
			state.cwd = rl.CWD
		}
		if rl.Title != "" {
			state.title = rl.Title
		}
		return nil
	case "message":
		return messageToRecord(rl, state, line)
	default:
		return nil
	}
}

func messageToRecord(rl rawLine, state *scanState, raw []byte) *Record {
	var mp messagePayload
	if err := json.Unmarshal(rl.Message, &mp); err != nil {
		return nil
	}
	recordType := roleToRecordType(mp.Role)
	if recordType == "" {
		return nil
	}
	rec := &Record{
		SessionID:  state.sessionID,
		MessageID:  rl.ID,
		ParentID:   rl.ParentID,
		Timestamp:  parseTimestamp(rl.Timestamp),
		RecordType: recordType,
		CWD:        state.cwd,
		Title:      state.title,
		Model:      mp.Model,
		Provider:   mp.Provider,
		Content:    extractTextContent(mp.Content),
		ToolName:   mp.ToolName,
		ToolCallID: mp.ToolCallID,
		IsError:    mp.IsError,
		Raw:        raw,
	}
	if mp.Usage != nil {
		in, out := mp.Usage.Input, mp.Usage.Output
		cacheRead, cacheWrite, total := mp.Usage.CacheRead, mp.Usage.CacheWrite, mp.Usage.TotalTokens
		rec.InputTokens = &in
		rec.OutputTokens = &out
		rec.CacheReadTokens = &cacheRead
		rec.CacheWriteTokens = &cacheWrite
		rec.TotalTokens = &total
		if mp.Usage.Cost != nil {
			cost := mp.Usage.Cost.Total
			rec.CostUSD = &cost
		}
	}
	return rec
}

// roleToRecordType maps an omo message role to a normalized RecordType.
// Roles outside the three known values return "" so the line is skipped.
func roleToRecordType(role string) string {
	switch role {
	case "user":
		return "user"
	case "assistant":
		return "assistant"
	case "toolResult":
		return "tool_result"
	default:
		return ""
	}
}

func parseTimestamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, _ = time.Parse("2006-01-02T15:04:05.000Z", s)
	}
	return t
}

// extractTextContent extracts plain text from a message content field, which
// may be a plain string, an array of typed blocks (text/thinking/toolCall/
// ...), or null. Only "text" blocks contribute; thinking and tool-call blocks
// carry no "text" field and are excluded from the joined result.
func extractTextContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var items []contentBlock
	if err := json.Unmarshal(raw, &items); err == nil {
		var parts []string
		for _, item := range items {
			if item.Type == "text" && item.Text != "" {
				parts = append(parts, item.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return ""
}
