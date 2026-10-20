// Package sessiontrace projects observed trace metadata without storing bodies,
// consulting a database, or inferring relationships from nearby timestamps.
package sessiontrace

import (
	"encoding/json"
	"time"
)

// Record is a visible source row. SourceScope identifies the exact account/copy
// scope; it must not combine unrelated sources. Blocks is the body-free SQL
// projection; nil enables Raw fixture parsing, while an empty slice is explicit.
type Record struct {
	ID               int64
	SessionID        string
	Agent            string
	TS               time.Time
	UUID             string
	RecordType       string
	SourceFile       string
	AgentID          string
	Branch           string
	SourceScope      string
	ToolUseID        string
	CodexTurnID      string
	CodexRootTurnID  string
	TurnKey          string // exact caller-supplied key only; never timestamp attribution
	Role             string
	PayloadType      string
	ToolName         string
	ToolCallID       string
	ToolStatus       string
	HasText          bool
	IsMeta           bool
	IsCompactSummary bool
	Redacted         bool
	ParentToolUseID  string
	ParentAgentID    string
	ParentSourceFile string
	ChildAgentID     string
	Blocks           []Block
	Raw              json.RawMessage // optional minimal projection, never retained
}

// Block keeps original content ordinality even when SQL filters other blocks.
// A nil Index uses its position in Blocks. IsError nil means unobserved outcome.
type Block struct {
	Index        *int   `json:"index,omitempty"`
	Type         string `json:"type"`
	ID           string `json:"id,omitempty"`
	Name         string `json:"name,omitempty"`
	ToolUseID    string `json:"tool_use_id,omitempty"`
	IsError      *bool  `json:"is_error,omitempty"`
	Status       string `json:"status,omitempty"`
	HasText      bool   `json:"has_text,omitempty"`
	ChildAgentID string `json:"child_agent_id,omitempty"`
}

// Event is the fixed trace-api-contract.md wire shape. Private observations
// contain metadata only and allow Merge before serialization; serialized wire
// events alone cannot reconstruct exact pairing or source references.
type Event struct {
	EventID       string     `json:"event_id"`
	Kind          string     `json:"kind"`
	Timestamp     time.Time  `json:"timestamp"`
	StartedAt     *time.Time `json:"started_at"`
	EndedAt       *time.Time `json:"ended_at"`
	TurnKey       *string    `json:"turn_key"`
	ParentEventID *string    `json:"parent_event_id"`
	AgentID       *string    `json:"agent_id"`
	Label         string     `json:"label"`
	Status        string     `json:"status"`
	Evidence      []string   `json:"evidence"`
	SourceRef     string     `json:"source_ref"`
	observations  []observation
}

// Source identifies a source tuple for an authorized context lookup. It carries
// no text, tool inputs, outputs, or account names in the public wire format.
type Source struct {
	ID          int64
	SessionID   string
	Agent       string
	UUID        string
	TS          time.Time
	RecordType  string
	SourceFile  string
	AgentID     string
	Branch      string
	SourceScope string
}

// Sources returns both observed endpoints when a call has an exact result.
func Sources(event Event) []Source {
	out := make([]Source, 0, len(event.observations))
	seen := make(map[Source]bool)
	for _, o := range event.observations {
		if !seen[o.source] {
			out = append(out, o.source)
			seen[o.source] = true
		}
	}
	return out
}

type observation struct {
	event       Event
	source      Source
	index       int
	callID      string
	phase       string
	scope       string
	parentID    string
	parentScope string
}
