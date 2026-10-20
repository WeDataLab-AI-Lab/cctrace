// Package sessiontranscript derives role-specific counts from the text already
// stored for a session. Raw records are inspected in memory and are never kept
// in a counter or returned to the caller.
package sessiontranscript

import (
	"encoding/json"
	"strings"
	"time"
)

const redactedMarker = "[redacted by cctrace client]"

// Record is the minimal stored row needed to count saved transcript text.
// Raw is transient input from session_records; it must not leave this package.
type Record struct {
	Agent            string
	RecordType       string
	PromptSource     string
	Entrypoint       string
	CommandName      string
	IsSidechain      bool
	IsMeta           bool
	IsCompactSummary bool
	// SyntheticOutput marks a Claude local/API-error row whose message body is
	// text but was not generated as model output.
	SyntheticOutput      bool
	InterruptedMessageID string
	ObservedAt           time.Time
	Raw                  json.RawMessage
}

// Measurement contains only nullable counters and transcript metadata.
// A nil direction means the saved rows could not establish its text count.
type Measurement struct {
	InputBytes  *int64
	InputLines  *int64
	OutputBytes *int64
	OutputLines *int64
	AsOf        *time.Time
	Partial     bool
}

type direction struct {
	bytes   int64
	lines   int64
	seen    bool
	unknown bool
}

// Counter consumes stored rows one at a time, retaining counters but no text.
type Counter struct {
	input  direction
	output direction
	asOf   *time.Time
}

func NewCounter() *Counter { return &Counter{} }

// Add reads only role and content shapes required to measure saved human and
// model text. Tool, reasoning, notification, and non-human input records do
// not contribute to these transcript-only totals.
func (c *Counter) Add(record Record) {
	if record.RecordType != "user" && record.RecordType != "assistant" {
		return
	}
	if record.RecordType == "assistant" {
		c.addAssistant(record)
		return
	}
	c.addUser(record)
}

func (c *Counter) Snapshot() Measurement {
	result := Measurement{AsOf: c.asOf, Partial: c.input.unknown || c.output.unknown}
	if c.input.seen && !c.input.unknown {
		result.InputBytes, result.InputLines = int64Pointer(c.input.bytes), int64Pointer(c.input.lines)
	}
	if c.output.seen && !c.output.unknown {
		result.OutputBytes, result.OutputLines = int64Pointer(c.output.bytes), int64Pointer(c.output.lines)
	}
	return result
}

func (c *Counter) addAssistant(record Record) {
	if record.Agent == "claude" && record.SyntheticOutput {
		return
	}
	parsed, ok := parseRecord(record)
	if !ok || parsed.role != "assistant" {
		c.output.unknown = true
		c.observe(record.ObservedAt)
		return
	}
	texts, redacted := textBlocks(parsed.content, parsed.family, "assistant")
	if redacted {
		c.output.unknown = true
		c.observe(record.ObservedAt)
		return
	}
	if len(texts) == 0 {
		return
	}
	text := strings.Join(texts, "")
	addText(&c.output, text)
	c.observe(record.ObservedAt)
}

func (c *Counter) addUser(record Record) {
	if record.IsSidechain || record.IsMeta || record.IsCompactSummary || record.InterruptedMessageID != "" {
		return
	}
	parsed, ok := parseRecord(record)
	if !ok || parsed.role != "user" {
		if knownNonHumanInput(record, parsed) {
			return
		}
		c.input.unknown = true
		c.observe(record.ObservedAt)
		return
	}
	if knownNonHumanInput(record, parsed) {
		return
	}
	texts, redacted := textBlocks(parsed.content, parsed.family, "user")
	parsed.hasText = len(texts) > 0
	if redacted {
		if !knownNonHumanInput(record, parsed) {
			c.input.unknown = true
			c.observe(record.ObservedAt)
		}
		return
	}
	text := strings.Join(texts, "")
	if record.Agent == "codex" {
		switch record.Entrypoint {
		case "cli":
			if text != "" && !looksLikeHumanText(text) {
				return
			}
			addText(&c.input, text)
			c.observe(record.ObservedAt)
		case "":
			if !parsed.hasToolResult {
				c.input.unknown = true
				c.observe(record.ObservedAt)
			}
		default:
			return
		}
		return
	}
	if parsed.isMeta || parsed.isCompactSummary ||
		record.InterruptedMessageID != "" || parsed.interruptedMessageID != "" {
		return
	}
	if text != "" && !looksLikeHumanText(text) {
		return
	}
	if knownHumanInput(record, parsed) {
		addText(&c.input, text)
		c.observe(record.ObservedAt)
		return
	}
	if knownNonHumanInput(record, parsed) {
		return
	}
	if looksLikeHumanText(text) || record.CommandName != "" || parsed.commandName != "" {
		addText(&c.input, text)
		c.observe(record.ObservedAt)
		return
	}
	if parsed.hasText || !parsed.hasContent || !parsed.hasToolResult {
		c.input.unknown = true
		c.observe(record.ObservedAt)
	}
}

func (c *Counter) observe(at time.Time) {
	if at.IsZero() || c.asOf != nil && !at.After(*c.asOf) {
		return
	}
	observed := at.UTC()
	c.asOf = &observed
}

type parsedRecord struct {
	family               string
	role                 string
	content              json.RawMessage
	promptSource         string
	originKind           string
	turnOrigin           string
	commandName          string
	isMeta               bool
	isCompactSummary     bool
	interruptedMessageID string
	hasText              bool
	hasToolResult        bool
	hasContent           bool
}

func parseRecord(record Record) (parsedRecord, bool) {
	if wholeRawRedacted(record.Raw) {
		return parsedRecord{}, false
	}
	if record.Agent == "codex" {
		var line struct {
			Type    string          `json:"type"`
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
			Payload struct {
				Type    string          `json:"type"`
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"payload"`
		}
		if json.Unmarshal(record.Raw, &line) != nil {
			return parsedRecord{}, false
		}
		parsed := parsedRecord{family: "codex"}
		if line.Type == "response_item" && line.Payload.Type == "message" {
			parsed.role, parsed.content = line.Payload.Role, line.Payload.Content
		} else if line.Type == "message" {
			parsed.role, parsed.content = line.Role, line.Content
		} else {
			return parsedRecord{}, false
		}
		parsed.hasContent = len(parsed.content) != 0 && string(parsed.content) != "null"
		parsed.hasToolResult = hasBlockType(parsed.content, "tool_result")
		return parsed, parsed.role == record.RecordType
	}
	var line struct {
		Type      string `json:"type"`
		PromptSrc string `json:"promptSource"`
		Origin    struct {
			Kind string `json:"kind"`
		} `json:"origin"`
		TurnOrigin  string `json:"turnOrigin"`
		Command     string `json:"commandName"`
		IsMeta      bool   `json:"isMeta"`
		Compact     bool   `json:"isCompactSummary"`
		Interrupted string `json:"interruptedMessageId"`
		Message     struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(record.Raw, &line) != nil || (line.Type != "user" && line.Type != "assistant") {
		return parsedRecord{}, false
	}
	parsed := parsedRecord{
		family: "claude", role: line.Message.Role, content: line.Message.Content,
		promptSource: line.PromptSrc, originKind: line.Origin.Kind, turnOrigin: line.TurnOrigin,
		commandName: line.Command, isMeta: line.IsMeta, isCompactSummary: line.Compact,
		interruptedMessageID: line.Interrupted,
		hasContent:           len(line.Message.Content) != 0 && string(line.Message.Content) != "null",
	}
	parsed.hasToolResult = hasBlockType(parsed.content, "tool_result")
	if record.RecordType != line.Type || parsed.role != line.Type {
		return parsed, false
	}
	return parsed, true
}

type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func hasBlockType(content json.RawMessage, wanted string) bool {
	var items []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(content, &items) != nil {
		return false
	}
	for _, item := range items {
		if item.Type == wanted {
			return true
		}
	}
	return false
}

func textBlocks(content json.RawMessage, family, role string) ([]string, bool) {
	if len(content) == 0 || string(content) == "null" {
		return nil, false
	}
	var text string
	if json.Unmarshal(content, &text) == nil {
		if text == redactedMarker {
			return nil, true
		}
		return []string{text}, false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(content, &object) == nil && object["redacted"] != nil {
		return nil, true
	}
	var items []contentItem
	if json.Unmarshal(content, &items) != nil {
		return nil, false
	}
	texts := make([]string, 0, len(items))
	for _, item := range items {
		if item.Text == redactedMarker {
			return nil, true
		}
		if !isTextBlock(family, role, item.Type) {
			continue
		}
		texts = append(texts, item.Text)
	}
	return texts, false
}

func wholeRawRedacted(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return false
	}
	var marker string
	return json.Unmarshal(object["redacted"], &marker) == nil && marker == redactedMarker
}

func isTextBlock(family, role, blockType string) bool {
	if family == "codex" {
		if role == "user" {
			return blockType == "input_text" || blockType == "text"
		}
		return blockType == "output_text" || blockType == "text"
	}
	if role == "user" {
		return blockType == "text" || blockType == "input_text"
	}
	return blockType == "text"
}

func knownHumanInput(record Record, parsed parsedRecord) bool {
	if record.PromptSource == "typed" || record.PromptSource == "paste" || record.PromptSource == "queued" || record.PromptSource == "suggestion_accepted" ||
		parsed.promptSource == "typed" || parsed.promptSource == "paste" || parsed.promptSource == "queued" || parsed.promptSource == "suggestion_accepted" {
		return true
	}
	return parsed.originKind == "human" || parsed.turnOrigin == "human" || record.Agent == "codex" && record.Entrypoint == "cli"
}

func knownNonHumanInput(record Record, parsed parsedRecord) bool {
	if record.Entrypoint == "sdk-cli" || record.Agent == "codex" && record.Entrypoint != "" && record.Entrypoint != "cli" {
		return true
	}
	switch record.PromptSource {
	case "sdk", "system":
		return true
	}
	switch parsed.promptSource {
	case "sdk", "system":
		return true
	}
	switch parsed.originKind {
	case "task-notification", "peer", "channel", "auto-continuation":
		return true
	}
	switch parsed.turnOrigin {
	case "task_notification", "peer", "scheduled", "sdk", "system":
		return true
	}
	return false
}

func looksLikeHumanText(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" || text == redactedMarker {
		return false
	}
	return !strings.HasPrefix(text, "<") && !strings.HasPrefix(text, "# AGENTS.md") &&
		!strings.HasPrefix(text, "[Request interrupted")
}

func addText(direction *direction, text string) {
	direction.seen = true
	direction.bytes = saturatingAdd(direction.bytes, int64(len(text)))
	direction.lines = saturatingAdd(direction.lines, countLines(text))
}

func countLines(text string) int64 {
	if text == "" {
		return 0
	}
	var lines int64
	content := false
	pendingCR := false
	for i := 0; i < len(text); i++ {
		b := text[i]
		if pendingCR {
			pendingCR = false
			if b == '\n' {
				continue
			}
		}
		switch b {
		case '\r':
			lines = saturatingAdd(lines, 1)
			content = false
			pendingCR = true
		case '\n':
			lines = saturatingAdd(lines, 1)
			content = false
		default:
			content = true
		}
	}
	if content {
		lines = saturatingAdd(lines, 1)
	}
	return lines
}

func int64Pointer(value int64) *int64 { return &value }

func saturatingAdd(a, b int64) int64 {
	const maxInt64 = int64(^uint64(0) >> 1)
	if b > 0 && a > maxInt64-b {
		return maxInt64
	}
	return a + b
}
