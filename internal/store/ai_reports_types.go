package store

import (
	"encoding/json"
	"errors"
	"time"
)

// Row types for the weekly AI report tables (ai_report_runs,
// ai_report_tool_calls, ai_reports, ai_consents) and the segment reads its tools
// make. Kept apart from store.go because only *PgStore implements the methods;
// the Store interface does not change.

// ErrAIRunAlreadyRunning is returned when the one-running-run-per-user partial
// unique index rejects a new run.
var ErrAIRunAlreadyRunning = errors.New("ai report run already running")

// AIReportRun.Status values.
const (
	AIRunRunning   = "running"
	AIRunCompleted = "completed"
	AIRunFailed    = "failed"
	AIRunCanceled  = "canceled"
)

// Who set a run going. The scheduler asks whether it has already fired for a
// week, and a run the user started by hand must not answer yes -- otherwise one
// manual report would cancel that week's automatic one.
const (
	AIRunStartedByManual   = "manual"
	AIRunStartedBySchedule = "schedule"
)

type AIReportRun struct {
	ID                int64      `json:"id"`
	DashboardUserID   int64      `json:"dashboard_user_id"`
	Week              string     `json:"week"`
	TZ                string     `json:"tz"`
	Since             time.Time  `json:"since"`
	Until             time.Time  `json:"until"`
	ScopeProfileEmail string     `json:"scope_profile_email"`
	ScopeUserID       string     `json:"scope_user_id"`
	StartedBy         string     `json:"started_by"`
	Status            string     `json:"status"`
	ErrorCode         string     `json:"error_code"`
	ErrorMessage      string     `json:"error_message"`
	Runtime           string     `json:"runtime"`
	Model             string     `json:"model"`
	AuthMode          string     `json:"auth_mode"`
	Usage             AIUsage    `json:"usage"`
	ItemsDropped      int        `json:"items_dropped"`
	StartedAt         time.Time  `json:"started_at"`
	FinishedAt        *time.Time `json:"finished_at"`
}

// AIUsage maps tokens_reported plus the nullable token columns. Reported false
// is stored as NULL tokens and must not be read back as zero usage.
type AIUsage struct {
	Reported          bool  `json:"reported"`
	InputTokens       int64 `json:"input_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
}

// AIToolCall.Status values.
const (
	AIToolCallRunning = "running"
	AIToolCallOK      = "ok"
	AIToolCallFailed  = "failed"
	AIToolCallTimeout = "timeout"
)

// AIToolCall is one row of the tool-call log. Args is the parsed summary, never
// the raw payload. Result fields are nil until the call finishes.
type AIToolCall struct {
	RunID       int64           `json:"run_id"`
	Seq         int             `json:"seq"`
	Tool        string          `json:"tool"`
	Args        json.RawMessage `json:"args"`
	Status      string          `json:"status"`
	ResultRows  *int            `json:"result_rows"`
	ResultBytes *int            `json:"result_bytes"`
	DurationMs  *int            `json:"duration_ms"`
	StartedAt   time.Time       `json:"started_at"`
}

// AIReport is the latest validated report for (user, ISO week).
type AIReport struct {
	DashboardUserID int64          `json:"dashboard_user_id"`
	Week            string         `json:"week"`
	RunID           int64          `json:"run_id"`
	TZ              string         `json:"tz"`
	Since           time.Time      `json:"since"`
	Until           time.Time      `json:"until"`
	Summary         string         `json:"summary"`
	Items           []AIReportItem `json:"items"`
	GeneratedAt     time.Time      `json:"generated_at"`
}

// AIReportItem is stored in ai_reports.items. Segment metadata is not stored;
// it is re-read through AISegmentsByIDs so visibility changes apply.
type AIReportItem struct {
	SegmentID string `json:"segment_id"`
	Title     string `json:"title"`
	Reason    string `json:"reason"`
}

type AIConsent struct {
	DashboardUserID   int64     `json:"dashboard_user_id"`
	RuntimeKey        string    `json:"runtime_key"`
	DisclosureVersion string    `json:"disclosure_version"`
	ConsentedAt       time.Time `json:"consented_at"`
}

// AISettings is the admin's choice of model, reasoning effort, and endpoint for
// report runs on one runtime. An empty field is no override: the environment, then
// the built-in default, decides that one.
type AISettings struct {
	Runtime         string    `json:"runtime"`
	Model           string    `json:"model"`
	ReasoningEffort string    `json:"reasoning_effort"`
	BaseURL         string    `json:"base_url"`
	UpdatedBy       string    `json:"updated_by"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// AIRuntimeChoice is the runtime an admin chose for report runs.
type AIRuntimeChoice struct {
	Runtime   string    `json:"runtime"`
	UpdatedBy string    `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AIEnabledChoice is an admin's word on whether AI reports run at all.
type AIEnabledChoice struct {
	Enabled   bool      `json:"enabled"`
	UpdatedBy string    `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AIAutoSchedule is the admin's default for the weekly automatic run. Each
// field stands on its own: a nil is no admin word, so choosing a weekday never
// implies an opinion about the switch, and the built-in default decides the
// rest.
type AIAutoSchedule struct {
	Enabled   *bool     `json:"enabled"`
	Weekday   *int      `json:"weekday"`
	Hour      *int      `json:"hour"`
	Minute    *int      `json:"minute"`
	UpdatedBy string    `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AIScheduleCandidate is one user the scheduler weighs on a tick: who they are,
// the override row they may never have saved, and the zone to read their
// weekday and time in when that row carries none.
//
// FallbackTZ is the zone of their latest report, which is the only record the
// server keeps of a user's zone until they save a schedule. Empty when they
// have no report yet, and an empty zone reads as UTC.
type AIScheduleCandidate struct {
	DashboardUserID int64           `json:"dashboard_user_id"`
	Email           string          `json:"email"`
	CctraceUserID   string          `json:"cctrace_user_id"`
	Schedule        *AIUserSchedule `json:"schedule"`
	FallbackTZ      string          `json:"fallback_tz"`
}

// AIUserSchedule is one user's override of that default, field by field. A nil
// keeps following the admin's value -- it does not mean "off" or "midnight".
//
// TZ is the zone the weekday and time are read in. The server has no other
// record of a user's zone: the screen sends it with every request, so the row
// captures the one in force when the user saved.
type AIUserSchedule struct {
	DashboardUserID int64     `json:"dashboard_user_id"`
	Enabled         *bool     `json:"enabled"`
	Weekday         *int      `json:"weekday"`
	Hour            *int      `json:"hour"`
	Minute          *int      `json:"minute"`
	TZ              string    `json:"tz"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// AIProviderCredential is an API key an admin registered, sealed by the
// caller. The store never sees the key itself; KeyHint is its last four
// characters.
type AIProviderCredential struct {
	Provider   string
	Ciphertext []byte
	Nonce      []byte
	KeyHint    string
	// KeySource names the secret that sealed the key; "" when unknown.
	KeySource string
	UpdatedBy string
	UpdatedAt time.Time
}

// AISegmentScope fixes whose segments a tool may see and the [Since, Until)
// window, so neither comes from model-supplied arguments.
type AISegmentScope struct {
	ProfileEmail string
	UserID       string
	Since        time.Time
	Until        time.Time
}

// AISegmentFilter.OrderBy values.
const (
	AISegmentOrderStartTs        = "start_ts"
	AISegmentOrderTypedTurnCount = "typed_turn_count"
	AISegmentOrderToolFailCount  = "tool_fail_count"
)

// AISegmentFilter is the parsed query_segments input. Zero values mean no
// filter; HadCompact nil means either.
type AISegmentFilter struct {
	HadCompact    *bool
	MinToolFail   int
	MinTypedTurns int
	Agent         string
	OrderBy       string
	Limit         int
}

// AISegment is one task_segment_facts row as the AI tools see it. ID is
// boundary_record_id. No token counts and no text.
type AISegment struct {
	ID             int64      `json:"id"`
	SessionID      string     `json:"session_id"`
	StartTs        time.Time  `json:"start_ts"`
	EndTs          *time.Time `json:"end_ts,omitempty"`
	ProjectHash    string     `json:"project_hash,omitempty"`
	ProjectName    string     `json:"project_name,omitempty"`
	Agent          string     `json:"agent"`
	TypedTurnCount int64      `json:"typed_turn_count"`
	ToolCallCount  int64      `json:"tool_call_count"`
	ToolFailCount  int64      `json:"tool_fail_count"`
	CommandCount   int64      `json:"command_count"`
	HadCompact     bool       `json:"had_compact"`
	// ToolEvidence has TaskSegment.ToolEvidence's meaning: zero tool counts
	// without it are "not measured", not "no tools".
	ToolEvidence bool `json:"tool_evidence"`
	// ToolOutcomeEvidence has TaskSegment.ToolOutcomeEvidence's meaning.
	ToolOutcomeEvidence bool `json:"tool_outcome_evidence"`
}

// AIWeekAggregate is the compare_week summary over one scope window.
type AIWeekAggregate struct {
	SegmentCount   int64 `json:"segment_count"`
	SessionCount   int64 `json:"session_count"`
	TypedTurnCount int64 `json:"typed_turn_count"`
	ToolCallCount  int64 `json:"tool_call_count"`
	// ToolOutcomeObservedCount is the part of ToolCallCount whose outcome was
	// recorded, and so the only denominator a failure rate may use: ToolFailCount
	// counts failures over these calls alone (AISegment.ToolOutcomeEvidence).
	ToolOutcomeObservedCount int64 `json:"tool_outcome_observed_count"`
	ToolFailCount            int64 `json:"tool_fail_count"`
	CommandCount             int64 `json:"command_count"`
	CompactedSegmentCount    int64 `json:"compacted_segment_count"`
}

// AIUsageSummary is the admin screen's usage_this_week. Token sums cover only
// runs that reported tokens.
type AIUsageSummary struct {
	Runs         int64 `json:"runs"`
	Failed       int64 `json:"failed"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}
