package store

import (
	"context"
	"time"

	"cctrace/internal/sessiontrace"
)

type TraceNodeStore interface {
	TraceNodes(context.Context, string, SessionAnalysisAccess, TraceNodesRequest) (TraceNodesResponse, error)
	TraceNodeDetail(context.Context, string, SessionAnalysisAccess, string) (TraceNodeDetail, error)
}
type TraceNodesRequest struct {
	ParentID, Cursor, View string
	GroupID, Query         string
	From, To               *time.Time
	RequestNo              int
	Limit                  int
}
type TraceNodesResponse struct {
	SessionID      string                       `json:"session_id"`
	Nodes          []sessiontrace.Node          `json:"nodes"`
	PrevCursor     *string                      `json:"prev_cursor"`
	Revision       int64                        `json:"revision"`
	Epoch          int64                        `json:"epoch"`
	RequestTotal   *sessiontrace.VerifiedNumber `json:"request_total,omitempty"`
	SearchScope    string                       `json:"search_scope,omitempty"`
	SearchCoverage string                       `json:"search_coverage,omitempty"`
	NextCursor     *string                      `json:"next_cursor"`
	Coverage       TraceCoverage                `json:"coverage"`
}
type TraceNodeDetail struct {
	SessionID       string                        `json:"session_id"`
	Navigation      *TraceNodeNavigation          `json:"navigation,omitempty"`
	SourceLocations []sessiontrace.SourceLocation `json:"source_locations,omitempty"`
	Node            sessiontrace.Node             `json:"node"`
	Input           sessiontrace.AuditValue       `json:"input"`
	Output          sessiontrace.AuditValue       `json:"output"`
	Context         sessiontrace.AuditValue       `json:"context"`
	Usage           map[string]any                `json:"usage"`
	RawRecords      []*SessionRecord              `json:"raw_records"`
	Request         *sessiontrace.NativeRequest   `json:"request,omitempty"`
	Notices         []string                      `json:"notices"`
}

// TraceNodeDetailOptions opts into bounded native input reading. Legacy callers
// retain the original complete-detail contract.
type TraceNodeDetailOptions struct {
	RequestParts, InputCursor, CompareTo, SourceRef string
	InputLimit                                      int
}
type TraceNodeDetailOptionsStore interface {
	TraceNodeDetailWithOptions(context.Context, string, SessionAnalysisAccess, string, TraceNodeDetailOptions) (TraceNodeDetail, error)
}
type TraceNodeNavigation struct {
	ChildCursors map[string]string `json:"child_cursors,omitempty"`
	GroupID      string            `json:"group_id,omitempty"`
	AncestorIDs  []string          `json:"ancestor_ids"`
	RootNodeID   string            `json:"root_node_id"`
	RootCursor   string            `json:"root_cursor,omitempty"`
	Revision     int64             `json:"revision"`
	Epoch        int64             `json:"epoch"`
	State        string            `json:"state"`
	Reason       string            `json:"reason,omitempty"`
}
