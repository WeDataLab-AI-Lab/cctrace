package localapi

import (
	"encoding/json"
	"time"

	"cctrace/internal/sessionview"
)

type Options struct {
	ClaudeDirs         []string
	CodexDirs          []string
	ProfileFingerprint string
	TokenHash          string
	Token              string
	Address            string
	AllowedOrigins     []string
	Profiles           []ProfileIdentity
	SelectionPath      string
}

// ProfileIdentity is the only profile metadata exposed through the local
// bridge. Paths, emails and authentication contents are intentionally absent.
type ProfileIdentity struct {
	Agent           string `json:"agent"`
	ProfileName     string `json:"profile_name"`
	BillingProvider string `json:"billing_provider"`
	AccountID       string `json:"account_id,omitempty"`
	Selected        bool   `json:"selected"`
}

type ProfileCatalogResponse struct {
	Revision uint64            `json:"revision"`
	Profiles []ProfileIdentity `json:"profiles"`
}

type Session struct {
	LocalID            string     `json:"local_id"`
	CanonicalID        string     `json:"canonical_id"`
	NativeSessionID    *string    `json:"native_session_id"`
	SessionID          string     `json:"session_id"`
	Agent              string     `json:"agent"`
	ProfileFingerprint string     `json:"profile_fingerprint"`
	Revision           string     `json:"revision"`
	ServerMatch        *string    `json:"server_match"`
	HasSync            bool       `json:"has_sync"`
	ProjectHash        string     `json:"project_hash"`
	ProjectName        *string    `json:"project_name"`
	Entrypoint         *string    `json:"entrypoint"`
	Model              *string    `json:"model"`
	Title              *string    `json:"title"`
	Name               *string    `json:"name"`
	StartTime          *time.Time `json:"start_time"`
	EndTime            *time.Time `json:"end_time"`
	InputTokens        *int64     `json:"input_tokens"`
	OutputTokens       *int64     `json:"output_tokens"`
	EventCount         *int       `json:"event_count"`
	CostUSD            *float64   `json:"cost_usd"`
}

type Record struct {
	Ts           time.Time        `json:"ts"`
	SessionID    string           `json:"session_id"`
	ProjectHash  string           `json:"project_hash"`
	RecordType   string           `json:"record_type"`
	ProfileEmail string           `json:"profile_email"`
	Model        string           `json:"model"`
	Agent        string           `json:"agent"`
	SourceFile   string           `json:"source_file"`
	UUID         string           `json:"uuid,omitempty"`
	ParentUUID   string           `json:"parent_uuid,omitempty"`
	Raw          json.RawMessage  `json:"raw"`
	View         sessionview.View `json:"view"`
}

type ListResponse struct {
	Sessions   []Session `json:"sessions"`
	Revision   string    `json:"revision"`
	NextCursor *string   `json:"next_cursor"`
}
type RecordsResponse struct {
	Records    []Record `json:"records"`
	Revision   string   `json:"revision"`
	NextCursor *string  `json:"next_cursor"`
}
type AnalysisResponse struct {
	LocalID      string           `json:"local_id"`
	Revision     string           `json:"revision"`
	RulesVersion int              `json:"rules_version"`
	Summary      AnalysisSummary  `json:"summary"`
	Turns        []AnalysisTurn   `json:"turns"`
	Timeline     AnalysisTimeline `json:"timeline"`
}
type AnalysisSummary struct {
	Agent                string     `json:"agent"`
	Model                *string    `json:"model"`
	Title                *string    `json:"title"`
	TitleState           string     `json:"title_state"`
	StartTime            *time.Time `json:"start_time"`
	EndTime              *time.Time `json:"end_time"`
	ActiveSeconds        int        `json:"active_seconds"`
	InputTokens          *int64     `json:"input_tokens"`
	OutputTokens         *int64     `json:"output_tokens"`
	CacheReadTokens      *int64     `json:"cache_read_tokens"`
	CacheCreateTokens    *int64     `json:"cache_create_tokens"`
	CostUSD              *float64   `json:"cost_usd"`
	CostSource           string     `json:"cost_source"`
	TurnBoundaries       string     `json:"turn_boundaries"`
	TurnBoundariesReason string     `json:"turn_boundaries_reason"`
	TurnCount            *int       `json:"turn_count"`
	RequestCount         *int       `json:"request_count"`
	InputCount           *int       `json:"input_count"`
	ToolCalls            int        `json:"tool_calls"`
}
type AnalysisTurn struct {
	TurnKey           string     `json:"turn_key"`
	Ordinal           int        `json:"ordinal"`
	RequestNo         *int       `json:"request_no"`
	Status            string     `json:"status"`
	StatusReason      string     `json:"status_reason"`
	Branch            string     `json:"branch"`
	InputKind         string     `json:"input_kind"`
	Preview           *string    `json:"preview"`
	PreviewState      string     `json:"preview_state"`
	StartedAt         time.Time  `json:"started_at"`
	EndedAt           *time.Time `json:"ended_at"`
	InputTokens       *int64     `json:"input_tokens"`
	OutputTokens      *int64     `json:"output_tokens"`
	CacheReadTokens   *int64     `json:"cache_read_tokens"`
	CacheCreateTokens *int64     `json:"cache_create_tokens"`
	CostUSD           *float64   `json:"cost_usd"`
	CostSource        string     `json:"cost_source"`
	ToolCalls         int        `json:"tool_calls"`
	Subagents         int        `json:"subagents"`
}
type AnalysisTimeline struct {
	State  string        `json:"state"`
	Reason string        `json:"reason"`
	Rows   []TimelineRow `json:"rows"`
}
type TimelineRow struct {
	Ts         time.Time `json:"ts"`
	UUID       string    `json:"uuid"`
	RecordType string    `json:"record_type"`
	TurnKey    string    `json:"turn_key"`
	Roles      []string  `json:"roles"`
	Unmapped   string    `json:"unmapped"`
}
