package store

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

const TraceChunkRows = 2000
const traceDefaultChunkRows = 500
const TraceOverviewRows = 200000
const TraceLeaseTTL = 15 * time.Second
const TraceHeartbeatInterval = 5 * time.Second
const TraceRulesVersion = 2

var ErrTraceDependencyLimit = errors.New("trace metadata dependency limit")
var ErrTraceBusy = errors.New("trace worker busy")
var ErrTraceInvalid = errors.New("invalid trace request")
var ErrTraceSnapshot = errors.New("expired trace snapshot")

type TraceStore interface {
	TraceDemand(context.Context, string, SessionAnalysisAccess, TraceDemandRequest) (TraceDemandResponse, error)
	TraceRelease(context.Context, string, SessionAnalysisAccess, string) error
	TraceOverview(context.Context, string, SessionAnalysisAccess, string) (TraceOverviewResponse, error)
	TraceEvents(context.Context, string, SessionAnalysisAccess, TraceEventsRequest) (TraceEventsResponse, error)
	TraceContext(context.Context, string, SessionAnalysisAccess, TraceContextRequest) (TraceContextResponse, error)
}
type TraceDemandRequest struct {
	Mode    string     `json:"mode"`
	From    *time.Time `json:"from,omitempty"`
	To      *time.Time `json:"to,omitempty"`
	LeaseID string     `json:"lease_id,omitempty"`
}

func (d TraceDemandRequest) Valid() bool {
	return (d.Mode == "overview" && d.From == nil && d.To == nil) || (d.Mode == "range" && validTraceRange(d.From, d.To))
}
func validTraceRange(from, to *time.Time) bool { return from != nil && to != nil && from.Before(*to) }

type TraceDemandResponse struct {
	SessionID  string    `json:"session_id"`
	LeaseID    string    `json:"lease_id"`
	ExpiresAt  time.Time `json:"expires_at"`
	SnapshotID string    `json:"snapshot_id"`
	State      string    `json:"state"`
}
type TraceCoverage struct {
	State         string     `json:"state"`
	From          *time.Time `json:"from"`
	To            *time.Time `json:"to"`
	ProcessedRows int64      `json:"processed_rows"`
	RowLimit      int        `json:"row_limit"`
	Reason        *string    `json:"reason"`
}
type TraceBin struct {
	Kind    string    `json:"kind"`
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	FirstTS time.Time `json:"first_ts"`
	LastTS  time.Time `json:"last_ts"`
	Count   int       `json:"count"`
}
type TraceOverviewResponse struct {
	SessionID      string        `json:"session_id"`
	SnapshotID     string        `json:"snapshot_id"`
	SourceRevision string        `json:"source_revision"`
	State          string        `json:"state"`
	Coverage       TraceCoverage `json:"coverage"`
	From           *time.Time    `json:"from"`
	To             *time.Time    `json:"to"`
	Bins           []TraceBin    `json:"bins"`
	EventsCount    int64         `json:"events_count"`
}
type TraceEventsRequest struct {
	SnapshotID string
	From, To   *time.Time
	Cursor     string
	Limit      int
}
type TraceEvent struct {
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
}
type TraceEventsResponse struct {
	SessionID      string        `json:"session_id"`
	SnapshotID     string        `json:"snapshot_id"`
	SourceRevision string        `json:"source_revision"`
	State          string        `json:"state"`
	Coverage       TraceCoverage `json:"coverage"`
	Events         []TraceEvent  `json:"events"`
	NextCursor     *string       `json:"next_cursor"`
}
type TraceContextRequest struct{ SnapshotID, EventID, TurnKey, Cursor string }
type TraceContextResponse struct {
	SessionID  string           `json:"session_id"`
	SnapshotID string           `json:"snapshot_id"`
	EventID    *string          `json:"event_id"`
	Records    []*SessionRecord `json:"records"`
	NextCursor *string          `json:"next_cursor"`
}

type traceCursor struct {
	Snapshot, Scope, Selection string
	Generation                 int64
	TS                         time.Time
	Key                        string
}

func encodeTraceCursor(c traceCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}
func decodeTraceCursor(raw, snapshot, scope, selection string) (traceCursor, error) {
	var c traceCursor
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || json.Unmarshal(b, &c) != nil || c.Snapshot != snapshot || c.Scope != scope || c.Selection != selection || c.Key == "" || c.TS.IsZero() {
		return c, ErrTraceInvalid
	}
	return c, nil
}
func traceScope(a SessionAnalysisAccess) string {
	b, _ := json.Marshal(struct {
		Dashboard                                   int64
		User                                        bool
		UID, Profile, Login, OwnerUID, OwnerProfile string
	}{a.DashboardUserID, a.User, a.UserID, a.ProfileEmail, a.LoginEmail, a.OwnerUserID, a.OwnerProfileEmail})
	h := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(h[:])
}
func traceSelection(from, to *time.Time) string {
	if from == nil || to == nil {
		return ""
	}
	return from.UTC().Format(time.RFC3339Nano) + "/" + to.UTC().Format(time.RFC3339Nano)
}
