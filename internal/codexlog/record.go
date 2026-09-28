package codexlog

import (
	"encoding/json"
	"strings"
	"time"
)

// Format version of a Codex JSONL file.
type Format int

const (
	FormatUnknown Format = iota
	FormatOld            // ~2026-02: flat records (message, function_call, etc.)
	FormatNew            // 2026-04+: payload-wrapped (session_meta, turn_context, etc.)
)

// Record is a normalized Codex session record.
type Record struct {
	SessionID       string
	Timestamp       time.Time
	RecordType      string // "user" | "assistant" | "agent_task" | "tool_call" | "tool_output" | "reasoning" | "usage"
	CWD             string
	Model           string
	Content         string
	ToolName        string
	CallID          string // pairs a tool_call with its tool_output
	ToolArgs        string
	ToolOutput      string
	InputTokens     *int
	OutputTokens    *int
	CacheReadTokens *int
	Raw             json.RawMessage
}

// scanState carries per-file mutable state during a scan pass.
type scanState struct {
	sessionID              string
	cwd                    string
	originator             string
	model                  string
	format                 Format
	hasTotalTokenUsage     bool
	totalInputTokens       int
	totalCachedInputTokens int
	totalOutputTokens      int
	hasTokenUsageRecord    bool
	forkMetadataScanned    bool
	isSubagentFork         bool
	forkHistoryCopied      bool
	forkBoundaryReached    bool
	forkHasTriggerTurn     bool

	// rateLimits accumulates account-scoped readings found during the pass.
	// It is not part of Metadata: metadata is persisted into the sync state
	// between passes, and a slice that grows every scan does not belong there.
	rateLimits []RateLimitSample
}

// DetectFormat returns the format based on the type field of a first record.
func DetectFormat(typ string) Format {
	switch typ {
	case "session_meta", "event_msg", "response_item", "turn_context", "compacted",
		"world_state", "inter_agent_communication_metadata", "token_usage_record":
		return FormatNew
	case "message", "function_call", "function_call_output", "reasoning":
		return FormatOld
	default:
		return FormatUnknown
	}
}

// --- raw JSON structs ---

type rawLine struct {
	Type       string `json:"type"`
	RecordType string `json:"record_type"`
	Timestamp  string `json:"timestamp"`
	// New format
	Payload json.RawMessage `json:"payload"`
	// Old format flat fields
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Output    string          `json:"output"`
}

type sessionMetaPayload struct {
	CWD           string `json:"cwd"`
	ModelProvider string `json:"model_provider"`
	ForkedFromID  string `json:"forked_from_id"`
	ThreadSource  string `json:"thread_source"`
	// Originator names what launched the session: "codex-tui" (human TUI),
	// "codex_exec" (headless), "codex_sdk_ts", or a delegating harness such as
	// "Claude Code". Codex emits no prompt_source, so this is the only signal
	// separating human turns from automated ones.
	Originator string `json:"originator"`
}

type turnContextPayload struct {
	CWD   string `json:"cwd"`
	Model string `json:"model"`
}

type responseItemPayload struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Output    json.RawMessage `json:"output"`
	Input     string          `json:"input"`
}

type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type tokenUsage struct {
	InputTokens       int `json:"input_tokens"`
	CachedInputTokens int `json:"cached_input_tokens"`
	OutputTokens      int `json:"output_tokens"`
}

type tokenUsageRecordPayload struct {
	Usage            *tokenUsage `json:"usage"`
	TurnTokenUsage   *tokenUsage `json:"turn_token_usage"`
	ThreadTokenUsage *tokenUsage `json:"thread_token_usage"`
}

// --- parsers ---

// parseNewFormatLine parses a single line from a new-format file.
// Returns nil for records that update state only (session_meta, turn_context, event_msg).
func parseNewFormatLine(line []byte, state *scanState) *Record {
	var rl rawLine
	if err := json.Unmarshal(line, &rl); err != nil {
		return nil
	}

	// Older fork files start the child's own records immediately after the
	// child session_meta. Newer files insert at least one inherited session_meta
	// before copied history, so only those files need the trigger_turn gate.
	if rl.Type != "session_meta" &&
		state.isSubagentFork &&
		!state.forkHistoryCopied &&
		!state.forkBoundaryReached {
		state.forkBoundaryReached = true
	}

	ts := parseTimestamp(rl.Timestamp)

	switch rl.Type {
	case "session_meta":
		var p sessionMetaPayload
		if err := json.Unmarshal(rl.Payload, &p); err == nil {
			if state.isSubagentFork {
				state.forkHistoryCopied = true
			}
			state.forkMetadataScanned = true
			if p.CWD != "" {
				state.cwd = p.CWD
			}
			// The child's session_meta comes first in a fork file, so the first
			// non-empty originator is the session's own.
			if state.originator == "" && p.Originator != "" {
				state.originator = p.Originator
			}
			// A fork file embeds its parent session_meta immediately after the child
			// metadata. Keep this sticky so the embedded non-fork metadata cannot
			// re-enable inherited history emission.
			if p.ForkedFromID != "" && p.ThreadSource == "subagent" {
				state.isSubagentFork = true
			}
		}
		return nil

	case "turn_context":
		var p turnContextPayload
		if err := json.Unmarshal(rl.Payload, &p); err == nil {
			if p.CWD != "" {
				state.cwd = p.CWD
			}
			if p.Model != "" {
				state.model = p.Model
			}
		}
		return nil

	case "event_msg":
		// Extract token usage from token_count events.
		//
		// rate_limits is a sibling of info, not a child of it, and info is null
		// on some readings — so the limits are collected before this branch
		// tests info. Reading them inside it would drop those readings with no
		// sign that anything was missed.
		var p struct {
			Type string `json:"type"`
			Info *struct {
				TotalTokenUsage *tokenUsage `json:"total_token_usage"`
				LastTokenUsage  *tokenUsage `json:"last_token_usage"`
			} `json:"info"`
			RateLimits *rawRateLimits `json:"rate_limits"`
		}
		if err := json.Unmarshal(rl.Payload, &p); err != nil {
			return nil
		}
		if p.Type == "token_count" && p.RateLimits != nil {
			if s, ok := p.RateLimits.sample(ts); ok {
				state.rateLimits = append(state.rateLimits, s)
			}
		}
		if p.Type == "token_count" && p.Info != nil {
			in, cache, out, ok := usageDeltaFromTokenEvent(state, p.Info.TotalTokenUsage, p.Info.LastTokenUsage)
			if !ok {
				return nil
			}
			// Prime the cumulative-token baseline from inherited events, but do not
			// report them as fresh usage in the child session. Only gate when the
			// file actually contains a trigger_turn marker: a no-copy fork embeds the
			// parent session_meta but copies no history and emits no trigger_turn, so
			// gating it would swallow the child's own usage entirely.
			if state.isSubagentFork && state.forkHasTriggerTurn && !state.forkBoundaryReached {
				return nil
			}
			return &Record{
				SessionID:       state.sessionID,
				Timestamp:       ts,
				RecordType:      "usage",
				CWD:             state.cwd,
				Model:           state.model,
				InputTokens:     &in,
				OutputTokens:    &out,
				CacheReadTokens: &cache,
				Raw:             line,
			}
		}
		return nil

	case "token_usage_record":
		var p tokenUsageRecordPayload
		if err := json.Unmarshal(rl.Payload, &p); err != nil || p.Usage == nil {
			return nil
		}
		state.hasTokenUsageRecord = true
		// thread_token_usage is cumulative in the new format and includes
		// compaction usage even when the following event_msg keeps its previous
		// total_token_usage value. Older new-format writers may omit it; in that
		// case advance the last known cumulative total by this raw usage.
		if p.ThreadTokenUsage != nil {
			updateTotalTokenUsage(state, p.ThreadTokenUsage)
		} else if state.hasTotalTokenUsage {
			state.totalInputTokens += p.Usage.InputTokens
			state.totalCachedInputTokens += p.Usage.CachedInputTokens
			state.totalOutputTokens += p.Usage.OutputTokens
		} else {
			updateTotalTokenUsage(state, p.Usage)
		}
		in, cache, out := tokenUsageAmounts(p.Usage)
		if !hasTokenUsage(in, cache, out) {
			return nil
		}
		if state.isSubagentFork && state.forkHasTriggerTurn && !state.forkBoundaryReached {
			return nil
		}
		return &Record{
			SessionID:       state.sessionID,
			Timestamp:       ts,
			RecordType:      "usage",
			CWD:             state.cwd,
			Model:           state.model,
			InputTokens:     &in,
			OutputTokens:    &out,
			CacheReadTokens: &cache,
			Raw:             line,
		}

	case "compacted":
		return nil

	case "inter_agent_communication_metadata":
		var p struct {
			TriggerTurn bool `json:"trigger_turn"`
		}
		// trigger_turn is the first stable marker after Codex finishes copying the
		// parent transcript and starts the subagent's own work. Record its presence
		// so a metadata rescan of the consumed prefix restores the gate.
		if err := json.Unmarshal(rl.Payload, &p); err == nil && p.TriggerTurn {
			state.forkHasTriggerTurn = true
			if state.isSubagentFork {
				state.forkBoundaryReached = true
			}
		}
		return nil

	case "response_item":
		var p responseItemPayload
		if err := json.Unmarshal(rl.Payload, &p); err != nil {
			return nil
		}
		if state.isSubagentFork && state.forkHasTriggerTurn && !state.forkBoundaryReached {
			return nil
		}
		return responseItemToRecord(p, ts, state, line)
	}

	return nil
}

func responseItemToRecord(p responseItemPayload, ts time.Time, state *scanState, raw []byte) *Record {
	rec := &Record{
		SessionID: state.sessionID,
		Timestamp: ts,
		CWD:       state.cwd,
		Model:     state.model,
		Raw:       raw,
	}
	switch p.Type {
	case "message":
		rec.RecordType = roleToRecordType(p.Role)
		rec.Content = extractTextContent(p.Content)
	case "function_call":
		rec.RecordType = "tool_call"
		rec.ToolName = p.Name
		rec.CallID = p.CallID
		if p.Arguments != nil {
			rec.ToolArgs = string(p.Arguments)
		}
	case "function_call_output":
		rec.RecordType = "tool_output"
		rec.CallID = p.CallID
		rec.ToolOutput = extractTextContent(p.Output)
	case "reasoning":
		rec.RecordType = "reasoning"
		rec.Content = extractTextContent(p.Content)
	case "custom_tool_call":
		rec.RecordType = "tool_call"
		rec.ToolName = p.Name
		rec.CallID = p.CallID
		rec.ToolArgs = p.Input
	case "custom_tool_call_output":
		rec.RecordType = "tool_output"
		rec.CallID = p.CallID
		rec.ToolOutput = extractTextContent(p.Output)
	case "agent_message":
		rec.RecordType = "agent_task"
		rec.Content = extractTextContent(p.Content)
	default:
		return nil
	}
	return rec
}

func usageDeltaFromTokenEvent(state *scanState, total, last *tokenUsage) (int, int, int, bool) {
	if state.hasTokenUsageRecord {
		// Once a token_usage_record has been observed, it is the canonical
		// source for this file and every token_count event is its mirror. The
		// event's total cannot even be diffed against the record's baseline:
		// thread_token_usage restarts when a session is resumed while
		// total_token_usage keeps counting the whole file, so the gap came out
		// as a phantom billion-token usage on every turn. After a compaction the
		// event can also lag behind the record. Leave the cumulative baseline to
		// the records.
		return 0, 0, 0, false
	}
	if total != nil {
		in, cache, out := tokenUsageAmounts(total)
		if state.hasTotalTokenUsage {
			in = total.InputTokens - state.totalInputTokens
			cache = total.CachedInputTokens - state.totalCachedInputTokens
			out = total.OutputTokens - state.totalOutputTokens

			if in == 0 && cache == 0 && out == 0 {
				updateTotalTokenUsage(state, total)
				return 0, 0, 0, false
			}
			if in < 0 || cache < 0 || out < 0 {
				if last != nil {
					in, cache, out = tokenUsageAmounts(last)
				} else {
					in, cache, out = tokenUsageAmounts(total)
				}
			}
		} else if last != nil {
			in, cache, out = tokenUsageAmounts(last)
		}
		updateTotalTokenUsage(state, total)
		return in, cache, out, hasTokenUsage(in, cache, out)
	}

	if last == nil {
		return 0, 0, 0, false
	}
	in, cache, out := tokenUsageAmounts(last)
	state.hasTotalTokenUsage = false
	return in, cache, out, hasTokenUsage(in, cache, out)
}

func tokenUsageAmounts(u *tokenUsage) (int, int, int) {
	return u.InputTokens, u.CachedInputTokens, u.OutputTokens
}

func updateTotalTokenUsage(state *scanState, total *tokenUsage) {
	state.totalInputTokens = total.InputTokens
	state.totalCachedInputTokens = total.CachedInputTokens
	state.totalOutputTokens = total.OutputTokens
	state.hasTotalTokenUsage = true
}

func hasTokenUsage(in, cache, out int) bool {
	return in > 0 || cache > 0 || out > 0
}

// parseOldFormatLine parses a single line from an old-format file.
func parseOldFormatLine(line []byte, state *scanState) *Record {
	var rl rawLine
	if err := json.Unmarshal(line, &rl); err != nil {
		return nil
	}

	// Skip markers
	if rl.RecordType == "state" {
		return nil
	}

	ts := parseTimestamp(rl.Timestamp)

	switch rl.Type {
	case "message":
		return &Record{
			SessionID:  state.sessionID,
			Timestamp:  ts,
			CWD:        state.cwd,
			Model:      state.model,
			RecordType: roleToRecordType(rl.Role),
			Content:    extractTextContent(rl.Content),
			Raw:        line,
		}
	case "function_call":
		args := ""
		if rl.Arguments != nil {
			args = string(rl.Arguments)
		}
		return &Record{
			SessionID:  state.sessionID,
			Timestamp:  ts,
			CWD:        state.cwd,
			Model:      state.model,
			RecordType: "tool_call",
			ToolName:   rl.Name,
			CallID:     rl.CallID,
			ToolArgs:   args,
			Raw:        line,
		}
	case "function_call_output":
		return &Record{
			SessionID:  state.sessionID,
			Timestamp:  ts,
			CWD:        state.cwd,
			Model:      state.model,
			RecordType: "tool_output",
			CallID:     rl.CallID,
			ToolOutput: rl.Output,
			Raw:        line,
		}
	case "reasoning":
		return &Record{
			SessionID:  state.sessionID,
			Timestamp:  ts,
			CWD:        state.cwd,
			Model:      state.model,
			RecordType: "reasoning",
			Raw:        line,
		}
	}

	// Old format init record (no type, has id+timestamp) — skip
	return nil
}

// --- helpers ---

// roleToRecordType keeps developer messages apart from user turns: Codex writes its
// injected skill, permission and environment instructions under role=developer, and
// every record_type='user' consumer (task segments, request classification) treats a
// user row as something a person typed.
func roleToRecordType(role string) string {
	switch role {
	case "assistant", "developer":
		return role
	}
	return "user"
}

// parseTimestamp reads the line's own timestamp and never substitutes a clock
// reading for a missing one -- the caller pins those to the epoch instead
// (syncer.go, #57), so a rescan lands on the same storage key.
//
// It follows that a rollout file whose lines all carry one instant produces
// records that all carry that instant. That is not a defect here: it is what the
// file says. Bulk conversions do it -- prod holds 42 segments starting inside one
// second on 2026-09-09, one of them with 3,274 turns -- and the surfaces that
// read time have to say so rather than draw it as work that happened then (#686).
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

// extractTextContent extracts plain text from a content field.
// Handles: array of {type, text} objects, plain string, or null.
func extractTextContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Try array of content items
	var items []contentItem
	if err := json.Unmarshal(raw, &items); err == nil {
		var parts []string
		for _, item := range items {
			if item.Text != "" {
				parts = append(parts, item.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	// Try plain string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return ""
}
