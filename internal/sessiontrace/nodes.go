package sessiontrace

import "time"

// Node is a source-backed audit item. A request is a native model request, never
// a user turn. Missing timestamps and parents remain null.
type Node struct {
	RequestNo      *VerifiedNumber `json:"request_no,omitempty"`
	ToolCount      *VerifiedNumber `json:"tool_count,omitempty"`
	GroupID        string          `json:"group_id,omitempty"`
	Role           string          `json:"role,omitempty"`
	Semantic       string          `json:"semantic,omitempty"`
	SourceKind     string          `json:"source_kind,omitempty"`
	TimeBasis      string          `json:"time_basis,omitempty"`
	NodeID         string          `json:"node_id"`
	ParentID       *string         `json:"parent_id"`
	ParentRelation string          `json:"parent_relation,omitempty"`
	GroupingBasis  string          `json:"grouping_basis,omitempty"`
	Kind           string          `json:"kind"`
	Title          string          `json:"title"`
	Timestamp      *time.Time      `json:"timestamp"`
	StartedAt      *time.Time      `json:"started_at"`
	EndedAt        *time.Time      `json:"ended_at"`
	Status         string          `json:"status"`
	HasChildren    bool            `json:"has_children"`
	ChildCount     *int            `json:"child_count"`
	SourceRef      string          `json:"source_ref"`
	Evidence       []string        `json:"evidence"`
	Availability   string          `json:"availability"`
}

// AuditValue preserves the difference between absent, withheld and observed.
type AuditValue struct {
	State  string `json:"state"`
	Value  any    `json:"value,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type DigestCheck struct {
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
	State    string `json:"state"`
}

type NativeRequest struct {
	Provenance  []RequestProvenance `json:"provenance,omitempty"`
	InputPage   *RequestInputPage   `json:"input_page,omitempty"`
	Comparison  *RequestComparison  `json:"comparison,omitempty"`
	State       string              `json:"state"`
	Body        map[string]any      `json:"body,omitempty"`
	Params      any                 `json:"params,omitempty"`
	Digest      DigestCheck         `json:"digest"`
	MissingRefs []string            `json:"missing_refs"`
	SourceRefs  []string            `json:"source_refs"`
}

// VerifiedNumber names its counting unit and source scope. Missing facts never
// become zero, and an input/queued count never masquerades as a request total.
type VerifiedNumber struct {
	Value      *int   `json:"value,omitempty"`
	State      string `json:"state"`
	Definition string `json:"definition"`
	Scope      string `json:"scope"`
	Revision   int64  `json:"revision"`
}
