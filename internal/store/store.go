package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ErrSetupAlreadyCompleted means an initial dashboard account already exists.
var ErrSetupAlreadyCompleted = errors.New("setup already completed")

// ErrProjectRuleAccessDenied is returned when a project rule ingest or read
// targets a repository the caller has no prior session history for. It guards
// against an API-key holder writing or reading rules for another team's repo.
var ErrProjectRuleAccessDenied = errors.New("project rule access denied")

// OtelEvent represents a parsed OTEL log event (user_prompt, api_request, tool_decision, tool_result, api_error).
type OtelEvent struct {
	Ts                time.Time              `json:"ts"`
	EventName         string                 `json:"event_name"`
	SessionID         string                 `json:"session_id,omitempty"`
	PromptID          string                 `json:"prompt_id,omitempty"`
	UserID            string                 `json:"user_id,omitempty"`
	ProfileEmail      string                 `json:"profile_email,omitempty"`
	LoginEmail        string                 `json:"login_email,omitempty"`
	UserName          string                 `json:"user_name,omitempty"`
	UserTeam          string                 `json:"user_team,omitempty"`
	OrgID             string                 `json:"org_id,omitempty"`
	Model             string                 `json:"model,omitempty"`
	CostUSD           *float64               `json:"cost_usd,omitempty"`
	InputTokens       *int                   `json:"input_tokens,omitempty"`
	OutputTokens      *int                   `json:"output_tokens,omitempty"`
	CacheReadTokens   *int                   `json:"cache_read_tokens,omitempty"`
	CacheCreateTokens *int                   `json:"cache_create_tokens,omitempty"`
	DurationMs        *int                   `json:"duration_ms,omitempty"`
	ToolName          string                 `json:"tool_name,omitempty"`
	ToolDecision      string                 `json:"tool_decision,omitempty"`
	ToolSuccess       *bool                  `json:"tool_success,omitempty"`
	Speed             string                 `json:"speed,omitempty"`
	ServiceVersion    string                 `json:"service_version,omitempty"`
	Attrs             map[string]interface{} `json:"attrs,omitempty"`
	Agent             string                 `json:"agent,omitempty"`            // "claude" | "codex" | "gjc" | "omo"
	BillingProvider   string                 `json:"billing_provider,omitempty"` // "anthropic" | "openai"
}

// OtelMetric represents a parsed OTEL metric data point.
type OtelMetric struct {
	Ts           time.Time `json:"ts"`
	MetricName   string    `json:"metric_name"`
	SessionID    string    `json:"session_id,omitempty"`
	UserID       string    `json:"user_id,omitempty"`
	ProfileEmail string    `json:"profile_email,omitempty"`
	LoginEmail   string    `json:"login_email,omitempty"`
	UserTeam     string    `json:"user_team,omitempty"`
	Model        string    `json:"model,omitempty"`
	// Sum/Gauge set at most one of ValueDouble/ValueInt (an unset OTLP oneof
	// leaves both nil). Histogram always sets ValueInt to the observation COUNT
	// (e.g. turns) and sets ValueDouble to the observed SUM (e.g. tokens) only
	// when the datapoint carries a sum, which OTLP makes optional. So the two
	// fields mean different things per metric type: summing ValueInt to chart
	// tokens silently plots the number of observations instead.
	ValueDouble     *float64               `json:"value_double,omitempty"`
	ValueInt        *int64                 `json:"value_int,omitempty"`
	Dimensions      map[string]interface{} `json:"dimensions,omitempty"`
	Agent           string                 `json:"agent,omitempty"`            // "claude" | "codex" | "gjc" | "omo"
	BillingProvider string                 `json:"billing_provider,omitempty"` // "anthropic" | "openai"
	// AccountID is the Codex billing account the exporter's
	// X-Cctrace-Codex-Account header named (#715); empty when it sent none.
	AccountID string `json:"account_id,omitempty"`
}

// SessionRecord represents a parsed record from a Claude Code session JSONL file.
type SessionRecord struct {
	Ts                 time.Time       `json:"ts"`
	SessionID          string          `json:"session_id,omitempty"`
	ProjectHash        string          `json:"project_hash,omitempty"`
	RepositoryID       string          `json:"repository_id,omitempty"`
	RepositoryIDSource string          `json:"repository_id_source,omitempty"`
	RepositoryName     string          `json:"repository_name,omitempty"`
	RepoSubpath        string          `json:"repo_subpath,omitempty"`
	RepoSubpathPresent bool            `json:"repo_subpath_present,omitempty"`
	CommitSHA          string          `json:"commit_sha,omitempty"`
	Branch             string          `json:"branch,omitempty"`
	RecordType         string          `json:"record_type"` // user, assistant, system, progress
	ProfileEmail       string          `json:"profile_email,omitempty"`
	LoginEmail         string          `json:"login_email,omitempty"`
	UserID             string          `json:"user_id,omitempty"`
	Model              string          `json:"model,omitempty"`
	InputTokens        *int            `json:"input_tokens,omitempty"`
	OutputTokens       *int            `json:"output_tokens,omitempty"`
	CacheReadTokens    *int            `json:"cache_read_tokens,omitempty"`
	CacheCreateTokens  *int            `json:"cache_create_tokens,omitempty"`
	Raw                json.RawMessage `json:"raw"`
	CommandName        string          `json:"command_name,omitempty"`
	// CommandSource and CommandKind are decided on the machine that ran the session:
	// the log records only a name, and the directories that answer what it was are
	// local and change over time. Empty means the client did not classify (older
	// clients), which is not the same as "unknown" (it looked and could not tell).
	CommandSource string `json:"command_source,omitempty"`
	CommandKind   string `json:"command_kind,omitempty"`
	// CommandInvoke separates a skill the user typed from one the model chose to
	// run. Codex reports this on its metric; Claude only had the typed half.
	CommandInvoke   string `json:"command_invoke,omitempty"`
	Agent           string `json:"agent,omitempty"`            // "claude" | "codex" | "gjc" | "omo"
	BillingProvider string `json:"billing_provider,omitempty"` // "anthropic" | "openai"
	// Lineage/source fields for individual-vs-assembled session views (forward-only).
	UUID              string `json:"uuid,omitempty"`
	ParentUUID        string `json:"parent_uuid,omitempty"`
	IsSidechain       bool   `json:"is_sidechain,omitempty"`
	AgentID           string `json:"agent_id,omitempty"` // subagent id; empty for main session file
	ForkedFromSession string `json:"forked_from_session,omitempty"`
	ForkedFromUUID    string `json:"forked_from_uuid,omitempty"`
	SourceFile        string `json:"source_file,omitempty"` // basename of the jsonl file this record came from
	ToolUseID         string `json:"tool_use_id,omitempty"` // subagent records: the main-thread Task/Agent tool_use id that spawned this sidechain
	// ToolName and ToolCallID identify the single tool invocation or output a record
	// carries (Codex tool_call/tool_output, gjc tool_call, omo tool_result). Empty for
	// Claude, whose one record can hold several tool_use blocks; arguments and output
	// stay in Raw so client-side redaction still covers them.
	ToolName         string `json:"tool_name,omitempty"`
	ToolCallID       string `json:"tool_call_id,omitempty"`
	IsCompactSummary bool   `json:"is_compact_summary,omitempty"` // first record of a /compact continuation session
	IsMeta           bool   `json:"is_meta,omitempty"`            // local-command scaffolding (caveat etc.)
	PromptSource     string `json:"prompt_source,omitempty"`      // typed/paste/queued/sdk/system — genuine turns carry this
	Entrypoint       string `json:"entrypoint,omitempty"`         // cli | sdk-cli (headless) | claude-desktop
	CctraceVersion   string `json:"cctrace_version,omitempty"`    // cctrace collector version that produced this record
	// AccountID is the provider billing account this record is attributed to
	// (Codex: chatgpt_account_id, read locally from auth.json). Empty means the
	// account in use at that time was never observed — not "the current one".
	AccountID string `json:"account_id,omitempty"`
	// TaskType is a server-derived label from a human user prompt. It never contains
	// the prompt itself; empty means the record did not carry classifiable input.
	TaskType string `json:"task_type,omitempty"`
}

// RepositoryID source values carried by the sync envelope. An empty source is
// the legacy wire shape and remains accepted for backward compatibility.
const (
	RepositoryIDSourceResolved = "resolved"
	RepositoryIDSourceFallback = "fallback"
	RepositoryIDSourceUnknown  = "unknown"
)

// ProjectIdentityMetadata is the identity portion of a sync envelope. The
// source and presence marker are deliberately separate: a local fallback may
// have a non-empty subpath, while a resolved repository root has an empty but
// authoritative subpath.
type ProjectIdentityMetadata struct {
	GitRemoteURL       string
	RepositoryID       string
	RepositoryName     string
	RepoSubpath        string
	RepositoryIDSource string
	RepoSubpathPresent bool
}

// CctraceVersionSince is the cctrace release that introduced session_records.cctrace_version.
// Records collected by clients at or above this version are guaranteed to carry a value; an
// empty CctraceVersion on such records is impossible, and on older records it means "collected
// before this release" rather than "unknown". This is not a value to bump on every release —
// it is fixed at the release that introduced the column and defines what "empty" means.
const CctraceVersionSince = "v0.7.8"

// ClientVersionHeaderSince is the cctrace release that introduced the
// X-Cctrace-Version sync header. An empty client_version means the request
// carried no header, which is mostly a client older than this release but can
// also be a build whose version string is empty (internal/syncer/client.go
// omits the header then). What it never means is "did not sync": since #455
// every sync writes a row, so absence of a row — not an empty value — is what
// says an account never synced. ListClientVersions relies on this to tell
// "reported below v0.6.1" apart from "unknown".
const ClientVersionHeaderSince = "v0.6.1"

// EventFilter is used to query events.
type EventFilter struct {
	SessionID     string
	ProfileEmail  string
	LoginEmail    string
	UserID        string
	ProjectHash   string
	ProjectHashes []string
	// ProjectHashesPresent distinguishes an explicit empty member set from an
	// absent project_hashes filter. Explicit empty means no matches; absent keeps
	// legacy scalar or intentionally unfiltered behavior.
	ProjectHashesPresent bool
	UserTeam             string
	Agent                string // "claude" | "codex" | "gjc" | "omo" | ""
	// ModelCategory buckets by billing_provider, not by agent: "anthropic" -> billing_provider
	// = 'anthropic', "codex" -> billing_provider = 'openai', "compatible" -> anything else
	// (e.g. third-party models via the Anthropic-compatible API surface, or non-subscription
	// providers). Names are kept stable even though the axis changed, to avoid breaking saved
	// filter state and API query params. This matters for gjc/omo: each mixes
	// subscription-billed and non-subscription traffic within a single agent, so an
	// agent-based bucket would misclassify them; billing_provider is the correct axis.
	ModelCategory string // "anthropic" | "codex" | "compatible" | ""
	Model         string // partial match, e.g. "sonnet-4-6"; see MetricFilter.Model
	EventName     string
	Timezone      string
	Since         *time.Time
	Until         *time.Time
	Limit         int
	Offset        int
}

// MetricFilter is used to query metrics.
type MetricFilter struct {
	MetricName   string
	UserID       string
	ProfileEmail string
	LoginEmail   string
	UserTeam     string
	Agent        string
	Model        string // partial match, same as EventFilter.Model above
	Since        *time.Time
	Until        *time.Time
	Limit        int
	Offset       int
}

// SessionRecordFilter is used to query session records.
type SessionRecordFilter struct {
	ProfileEmail string
	UserID       string
	SessionID    string
	Limit        int
	Offset       int
	Order        string // "asc" for oldest-first (session detail lazy-load); default newest-first
}

// SessionOverviewFilter scopes the paged session overview and matching total count.
type SessionOverviewFilter struct {
	ProfileEmail  string
	LoginEmail    string
	UserID        string
	Since         *time.Time
	Until         *time.Time
	Limit         int
	FoldLineage   bool
	Source        string
	Offset        int
	ProjectHashes []string
	Agent         string
	// OnlyUnattributed counts the sessions a login_email filter necessarily
	// drops: those carrying no account identity anywhere, in OTEL or in synced
	// records. Reporting that number is what keeps the filter honest -- these
	// sessions are real usage, and silently omitting them reads as "there was
	// nothing here". Filling them in from profile_email instead would be a
	// guess, which is the mis-attribution #220 exists to remove.
	//
	// A session the inference pass attributed (#346) drops out of this count. That
	// narrows the population an operator sees, deliberately: see the has_account
	// expression in postgres_session_overview_rollup.go for the argument.
	OnlyUnattributed bool
}

// CostSummary holds aggregated cost data.
type CostSummary struct {
	ProfileEmail    string   `json:"profile_email"`
	UserID          string   `json:"user_id,omitempty"`
	LoginEmails     []string `json:"login_emails,omitempty"`
	UserTeam        string   `json:"user_team"`
	Model           string   `json:"model"`
	Agent           string   `json:"agent"`            // "claude" | "codex" | "gjc" | "omo"
	BillingProvider string   `json:"billing_provider"` // "anthropic" | "openai"
	TotalCost       float64  `json:"total_cost"`
	TotalInput      int64    `json:"total_input_tokens"`
	TotalOutput     int64    `json:"total_output_tokens"`
	RequestCount    int64    `json:"request_count"`
}

// ModelStat holds aggregated cost data grouped by model.
type ModelStat struct {
	Model           string  `json:"model"`
	ProfileEmail    string  `json:"profile_email,omitempty"`
	UserTeam        string  `json:"user_team,omitempty"`
	Agent           string  `json:"agent"`
	BillingProvider string  `json:"billing_provider"`
	TotalCost       float64 `json:"total_cost"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	RequestCount    int64   `json:"request_count"`
}

// ModelUsageAggregate holds token usage grouped by model for the external read API.
type ModelUsageAggregate struct {
	Model        string
	InputTokens  int64
	OutputTokens int64
}

// UsageAggregate holds overall session usage and an exact per-model token split.
// WorkTimeSeconds is the sum of each session's elapsed time (last event minus first event).
type UsageAggregate struct {
	SessionCount    int64
	InputTokens     int64
	OutputTokens    int64
	WorkTimeSeconds int64
	ByModel         []*ModelUsageAggregate
}

// WeeklyInsights is a privacy-preserving personal report. It contains no raw
// session content, prompt text, command text, or other users' dimensions.
type WeeklyInsights struct {
	AgentSessions []WeeklyInsightAgentSessions `json:"agent_sessions"`
	SegmentCount  int64                        `json:"segment_count"`
	// UncoveredSessionCount includes visible sessions with no caller-scoped fact
	// anywhere in history, even if their records fall within this report window.
	UncoveredSessionCount int64 `json:"uncovered_session_count"`
	// ExcludedRecordCount is how many of THIS caller's records in the window the
	// visibility views hide. Counted in records rather than exclusion rows on
	// purpose: an exclusion the admin configured that never touched this caller's
	// week is not this caller's caveat, and the screen prints the sentence only
	// above zero.
	ExcludedRecordCount int64                  `json:"excluded_record_count"`
	Hours               []WeeklyInsightHour    `json:"hours"`
	Projects            []WeeklyInsightProject `json:"projects"`
	Tasks               []WeeklyInsightTask    `json:"tasks"`
	Tools               []WeeklyInsightTool    `json:"tools"`
	// TypedTurnCount is every typed prompt in the window, classified or not --
	// the denominator Tasks' counts are a subset of, so the dashboard can show
	// what fraction of activity actually has a task_type instead of implying
	// Tasks is everything. Counted over the same rows Tasks is counted over; it
	// used to sum task_segment_facts and that is a narrower population, which
	// made the shares exceed 100% whenever the reconciler was behind.
	TypedTurnCount int64 `json:"typed_turn_count"`
	// CollapsedTimelineSessions counts sessions in the window whose records all
	// carry effectively the same timestamp -- a conversation cannot happen inside
	// one second. They are real work with unusable times, and every hour bucket
	// they land in is an artefact of when the file was written, not when the work
	// happened (#686).
	//
	// Reported rather than filtered out. Dropping them would make the week look
	// smaller than it was; the screen's job is to say which part of the time axis
	// cannot be trusted.
	CollapsedTimelineSessions int64 `json:"collapsed_timeline_sessions"`
}

type WeeklyInsightAgentSessions struct {
	Agent        string `json:"agent"`
	SessionCount int64  `json:"session_count"`
}

type WeeklyInsightHour struct {
	Hour         int   `json:"hour"`
	SessionCount int64 `json:"session_count"`
}
type WeeklyInsightProject struct {
	// ProjectHash remains the stable representative for older clients. New
	// consumers must use ProjectHashes: one canonical repository identity may
	// own several path-derived hashes (for example main and worktree).
	ProjectHash   string   `json:"project_hash"`
	ProjectHashes []string `json:"project_hashes"`
	ProjectName   string   `json:"project_name"`
	SessionCount  int64    `json:"session_count"`
	TotalTokens   int64    `json:"total_tokens"`
}
type WeeklyInsightTask struct {
	TaskType    string `json:"task_type"`
	PromptCount int64  `json:"prompt_count"`
}
type WeeklyInsightTool struct {
	ToolName  string `json:"tool_name"`
	UseCount  int64  `json:"use_count"`
	FailCount int64  `json:"fail_count"`
}

// TaskSegmentPage carries the ceiling with the rows.
//
// The list stops at taskSegmentsLimit and used to say nothing about it, so a week
// with more segments than the cap showed a shorter list than the number above it
// without a word (#669). Total is counted over the period, not the page, so it
// stays right when the page is cut.
//
// Total also counts a different unit from the card that opens this list: the card
// counts prompts carrying the task_type, this counts segments containing at least
// one. One segment can hold several such prompts, so the two numbers legitimately
// differ -- which is exactly why both have to be on screen.
type TaskSegmentPage struct {
	Segments  []*TaskSegment `json:"segments"`
	Total     int64          `json:"total"`
	Truncated bool           `json:"truncated"`
}

// TaskSegment is one work segment shown behind a Task types row in the weekly
// report. It carries no prompt or command text -- task_segment_facts has no such
// column, so there is nothing to leak.
type TaskSegment struct {
	SessionID     string     `json:"session_id"`
	StartTs       time.Time  `json:"start_ts"`
	EndTs         *time.Time `json:"end_ts,omitempty"`
	ProjectHash   string     `json:"project_hash,omitempty"`
	ProjectName   string     `json:"project_name,omitempty"`
	ToolCallCount int64      `json:"tool_call_count"`
	ToolFailCount int64      `json:"tool_fail_count"`
	CommandCount  int64      `json:"command_count"`
	InputTokens   int64      `json:"input_tokens"`
	OutputTokens  int64      `json:"output_tokens"`
	// ToolEvidence says whether ToolCallCount was measured. A Claude segment's calls
	// come from OTEL and its tokens from the JSONL sync, so a session collected on a
	// machine with no exporter reports real tokens and zero calls -- which reads as
	// "did a lot of work, used no tools" rather than "not measured". Measured on
	// production, that is 7,223 of the 7,989 segments showing zero calls; only 98
	// were a stale count (#435). A Codex segment's calls come from its own JSONL
	// tool_call rows, so they are always measured.
	ToolEvidence bool `json:"tool_evidence"`
	// ToolOutcomeEvidence says whether ToolFailCount was measured. Codex JSONL
	// records calls but not their outcome, so a Codex segment's zero failures are
	// unobserved, not zero.
	ToolOutcomeEvidence bool `json:"tool_outcome_evidence"`
	// TimelineCollapsed says this segment's session carries no usable times: its
	// whole record span fits inside a second. The work is real, the clock is not
	// -- the moment shown is when the file was written, not when the work
	// happened (#686).
	TimelineCollapsed bool `json:"timeline_collapsed"`
}

// DailyStat holds aggregated daily usage data.
type DailyStat struct {
	Date         string  `json:"date"` // YYYY-MM-DD
	CostUSD      float64 `json:"cost_usd"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	EventCount   int64   `json:"event_count"`
}

// ModelDailyStat is like DailyStat but includes model breakdown.
type ModelDailyStat struct {
	Date         string  `json:"date"`
	Model        string  `json:"model"`
	CostUSD      float64 `json:"cost_usd"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	EventCount   int64   `json:"event_count"`
}

// UserDailyStat is like DailyStat but includes user breakdown.
type UserDailyStat struct {
	Date         string  `json:"date"`
	UserID       string  `json:"user_id"`
	ProfileEmail string  `json:"profile_email"`
	CostUSD      float64 `json:"cost_usd"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	EventCount   int64   `json:"event_count"`
}

// UserInfo holds per-user aggregated data.
type UserInfo struct {
	UserID       string   `json:"user_id"`
	ProfileEmail string   `json:"profile_email"`
	LoginEmails  []string `json:"login_emails"`
	TotalCost    float64  `json:"total_cost"`
}

// PluginUsageSummary holds aggregated plugin usage statistics.
type PluginUsageSummary struct {
	CommandName     string `json:"command_name"`
	Agent           string `json:"agent"`
	ProfileEmail    string `json:"profile_email"`
	UserID          string `json:"user_id"`
	ProjectHash     string `json:"project_hash,omitempty"`
	ProjectName     string `json:"project_name,omitempty"`
	RepositoryID    string `json:"repository_id,omitempty"`
	RepositoryName  string `json:"repository_name,omitempty"`
	RepoSubpath     string `json:"repo_subpath,omitempty"`
	HasGit          bool   `json:"has_git"`
	InvocationCount int64  `json:"invocation_count"`
	TotalTokens     int64  `json:"total_tokens"`
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
}

// SkillUsageSummary holds aggregated skill invocation metrics.
type SkillUsageSummary struct {
	SkillName      string `json:"skill_name"`
	Agent          string `json:"agent"`
	InvokeType     string `json:"invoke_type"` // explicit | implicit
	ProfileEmail   string `json:"profile_email"`
	LoginEmail     string `json:"login_email"`
	UserID         string `json:"user_id"`
	ProjectHash    string `json:"project_hash,omitempty"`
	ProjectName    string `json:"project_name,omitempty"`
	RepositoryID   string `json:"repository_id,omitempty"`
	RepositoryName string `json:"repository_name,omitempty"`
	RepoSubpath    string `json:"repo_subpath,omitempty"`
	HasGit         bool   `json:"has_git"`
	SuccessCount   int64  `json:"success_count"`
	FailCount      int64  `json:"fail_count"`
	TotalCount     int64  `json:"total_count"`
}

// ToolUsageSummary holds aggregated tool usage.
type ToolUsageSummary struct {
	ToolName     string `json:"tool_name"`
	UseCount     int64  `json:"use_count"`
	SuccessCount int64  `json:"success_count"`
	FailCount    int64  `json:"fail_count"`
}

// OrganizationInsights is a privacy-preserving instance-wide aggregate. It is
// intentionally separate from user-scoped summaries: every returned dimension
// has already met the minimum distinct-contributor threshold.
type OrganizationInsights struct {
	Available    bool                            `json:"available"`
	ActiveUsers  int64                           `json:"active_users,omitempty"`
	MinimumUsers int64                           `json:"minimum_users"`
	Models       []OrganizationModelInsight      `json:"models,omitempty"`
	Tools        []OrganizationToolInsight       `json:"tools,omitempty"`
	Tasks        []OrganizationTaskInsight       `json:"tasks,omitempty"`
	Hours        []OrganizationHourInsight       `json:"hours,omitempty"`
	Projects     []OrganizationProjectInsight    `json:"projects,omitempty"`
	Bottlenecks  []OrganizationBottleneckInsight `json:"bottlenecks,omitempty"`
	// TypedTurnCount is every typed prompt org-wide in the window, classified or
	// not -- see WeeklyInsights.TypedTurnCount for why Tasks alone overstates
	// coverage. Not gated by minUsers: it is a single aggregate, not a per-task
	// cohort, so it carries no more re-identification risk than ActiveUsers.
	TypedTurnCount int64 `json:"typed_turn_count,omitempty"`
}

type OrganizationModelInsight struct {
	Model            string  `json:"model"`
	ContributorCount int64   `json:"contributor_count"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CostUSD          float64 `json:"cost_usd"`
}

type OrganizationToolInsight struct {
	ToolName         string `json:"tool_name"`
	ContributorCount int64  `json:"contributor_count"`
	UseCount         int64  `json:"use_count"`
	SuccessCount     int64  `json:"success_count"`
	FailCount        int64  `json:"fail_count"`
}

type OrganizationTaskInsight struct {
	TaskType         string `json:"task_type"`
	ContributorCount int64  `json:"contributor_count"`
	PromptCount      int64  `json:"prompt_count"`
}

type OrganizationHourInsight struct {
	Hour             int   `json:"hour"`
	ContributorCount int64 `json:"contributor_count"`
	SessionCount     int64 `json:"session_count"`
}

type OrganizationProjectInsight struct {
	// ProjectHash is retained for compatibility; ProjectHashes is the complete
	// member set of the canonical repository identity.
	ProjectHash      string   `json:"project_hash"`
	ProjectHashes    []string `json:"project_hashes"`
	ContributorCount int64    `json:"contributor_count"`
	SessionCount     int64    `json:"session_count"`
	TotalTokens      int64    `json:"total_tokens"`
}

type OrganizationBottleneckInsight struct {
	ToolName         string `json:"tool_name"`
	ContributorCount int64  `json:"contributor_count"`
	UseCount         int64  `json:"use_count"`
	FailCount        int64  `json:"fail_count"`
}

// ToolTimeBucket holds per-period success/fail counts for a single tool.
type ToolTimeBucket struct {
	Date         string `json:"date"`
	SuccessCount int64  `json:"success_count"`
	FailCount    int64  `json:"fail_count"`
}

// ToolFailure holds details of a single tool failure event.
type ToolFailure struct {
	Ts           time.Time              `json:"ts"`
	SessionID    string                 `json:"session_id"`
	UserID       string                 `json:"user_id"`
	ProfileEmail string                 `json:"profile_email"`
	LoginEmail   string                 `json:"login_email"`
	Model        string                 `json:"model"`
	DurationMs   *int                   `json:"duration_ms,omitempty"`
	Attrs        map[string]interface{} `json:"attrs,omitempty"`
}

// SessionSummary holds aggregated data per session.
type SessionSummary struct {
	SessionID    string `json:"session_id"`
	ProfileEmail string `json:"profile_email"`
	// AccountCount is how many distinct accounts this session's records belong
	// to. More than 1 means the token totals below are a sum across accounts and
	// ProfileEmail is only the dominant one.
	AccountCount int       `json:"account_count"`
	ProjectHash  string    `json:"project_hash"`
	ProjectName  string    `json:"project_name"`
	Model        string    `json:"model"`
	StartTime    time.Time `json:"start_time"`
	EndTime      time.Time `json:"end_time"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	RecordCount  int64     `json:"record_count"`
}

// Project holds project metadata derived from CWD.
type Project struct {
	ProjectHash    string     `json:"project_hash"`
	ProjectName    string     `json:"project_name"`
	GitRemoteURL   string     `json:"git_remote_url"`
	RepositoryID   string     `json:"repository_id"`
	RepositoryName string     `json:"repository_name"`
	RepoSubpath    string     `json:"repo_subpath"`
	LastSessionAt  *time.Time `json:"last_session_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// ProjectFilter scopes the project picker to the same population the session list
// shows. Every field means exactly what it means in SessionOverviewFilter -- without
// them the dropdown offered projects whose every session the list then filtered away,
// so picking one opened an empty list (#352). LoginEmail is the one the header's
// account selector drives, and it reproduced that symptom on its own.
type ProjectFilter struct {
	ProfileEmail string
	LoginEmail   string
	UserID       string
	Source       string
	Agent        string
	// FoldLineage mirrors assembled=1: a branch/clear child is folded into its root
	// and is not a session of its own.
	FoldLineage bool
}

// ProjectRule represents the current state of a repository instruction file.
type ProjectRule struct {
	ID                 int64     `json:"id"`
	Agent              string    `json:"agent"` // "claude" | "codex" | "gjc" | "omo"
	ProjectHash        string    `json:"project_hash,omitempty"`
	ProjectName        string    `json:"project_name,omitempty"`
	RepositoryID       string    `json:"repository_id,omitempty"`
	RepositoryKey      string    `json:"repository_key"`
	RepositoryName     string    `json:"repository_name,omitempty"`
	RulePath           string    `json:"rule_path"`
	RuleKind           string    `json:"rule_kind"`  // agents | agents_override | claude | claude_project | claude_rule | ...
	RuleScope          string    `json:"rule_scope"` // repository | directory | global | local
	Title              string    `json:"title,omitempty"`
	CurrentVersionID   int64     `json:"current_version_id,omitempty"`
	CurrentContentHash string    `json:"current_content_hash,omitempty"`
	CurrentStatus      string    `json:"current_status"` // active | missing | deleted | unreadable | archived
	DiscoveredAt       time.Time `json:"discovered_at"`
	LastSeenAt         time.Time `json:"last_seen_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// ProjectRuleListItem is the compact payload used by list views.
type ProjectRuleListItem struct {
	ProjectRule
	VersionCount int64 `json:"version_count"`
	CommentCount int64 `json:"comment_count"`
}

// ProjectRuleFilter is the dashboard query contract for rule lists.
type ProjectRuleFilter struct {
	ProfileEmail  string `json:"-"`
	UserID        string `json:"-"`
	Agent         string `json:"agent,omitempty"`
	RepositoryKey string `json:"repository_key,omitempty"`
	RepositoryID  string `json:"repository_id,omitempty"`
	ProjectHash   string `json:"project_hash,omitempty"`
	Status        string `json:"status,omitempty"`
	Query         string `json:"query,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	Offset        int    `json:"offset,omitempty"`
}

// ProjectRuleListResponse is returned by GET /api/project-rules.
type ProjectRuleListResponse struct {
	Items []*ProjectRuleListItem `json:"items"`
	Total int64                  `json:"total"`
}

// ProjectRuleVersion is an immutable content snapshot for a project rule.
type ProjectRuleVersion struct {
	ID                    int64                  `json:"id"`
	RuleID                int64                  `json:"rule_id"`
	VersionNumber         int                    `json:"version_number"`
	ContentHash           string                 `json:"content_hash"`
	Content               string                 `json:"content,omitempty"`
	SizeBytes             int                    `json:"size_bytes"`
	ChangeReason          string                 `json:"change_reason,omitempty"`
	CommitSHA             string                 `json:"commit_sha,omitempty"`
	Branch                string                 `json:"branch,omitempty"`
	AppliesTo             []string               `json:"applies_to,omitempty"`
	Frontmatter           map[string]interface{} `json:"frontmatter,omitempty"`
	RawMetadata           map[string]interface{} `json:"raw_metadata,omitempty"`
	CreatedByProfileEmail string                 `json:"created_by_profile_email,omitempty"`
	CreatedByUserID       string                 `json:"created_by_user_id,omitempty"`
	DiscoveredAt          time.Time              `json:"discovered_at"`
}

// ProjectRuleComment is a user-authored note attached to a rule or version.
type ProjectRuleComment struct {
	ID                 int64     `json:"id"`
	RuleID             int64     `json:"rule_id"`
	VersionID          int64     `json:"version_id,omitempty"`
	CommentType        string    `json:"comment_type"` // comment | change_reason
	AuthorProfileEmail string    `json:"author_profile_email,omitempty"`
	AuthorUserID       string    `json:"author_user_id,omitempty"`
	Body               string    `json:"body"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// ProjectRuleDetail is the dashboard detail payload for one rule.
type ProjectRuleDetail struct {
	Rule     *ProjectRuleListItem  `json:"rule"`
	Versions []*ProjectRuleVersion `json:"versions"`
	Comments []*ProjectRuleComment `json:"comments"`
}

// ProjectRuleSnapshot is sent by cctrace sync after scanning local repo rule files.
type ProjectRuleSnapshot struct {
	RulePath    string                 `json:"rule_path"`
	RuleKind    string                 `json:"rule_kind"`
	RuleScope   string                 `json:"rule_scope,omitempty"`
	Title       string                 `json:"title,omitempty"`
	Status      string                 `json:"status"` // active | missing | deleted | unreadable | archived
	ContentHash string                 `json:"content_hash,omitempty"`
	Content     string                 `json:"content,omitempty"`
	SizeBytes   int                    `json:"size_bytes,omitempty"`
	AppliesTo   []string               `json:"applies_to,omitempty"`
	Frontmatter map[string]interface{} `json:"frontmatter,omitempty"`
	RawMetadata map[string]interface{} `json:"raw_metadata,omitempty"`
	ReadError   string                 `json:"read_error,omitempty"`
}

// ProjectRuleIngestRequest is the CLI-to-server payload for rule snapshots.
type ProjectRuleIngestRequest struct {
	ProfileEmail   string                 `json:"profile_email"`
	UserID         string                 `json:"user_id"`
	Agent          string                 `json:"agent"`
	ProjectHash    string                 `json:"project_hash,omitempty"`
	ProjectName    string                 `json:"project_name,omitempty"`
	RepositoryID   string                 `json:"repository_id,omitempty"`
	RepositoryKey  string                 `json:"repository_key,omitempty"`
	RepositoryName string                 `json:"repository_name,omitempty"`
	CommitSHA      string                 `json:"commit_sha,omitempty"`
	Branch         string                 `json:"branch,omitempty"`
	Rules          []*ProjectRuleSnapshot `json:"rules"`
}

// ProjectRuleIngestResponse summarizes ingest changes.
type ProjectRuleIngestResponse struct {
	InsertedRules    int `json:"inserted_rules"`
	UpdatedRules     int `json:"updated_rules"`
	InsertedVersions int `json:"inserted_versions"`
	UnchangedRules   int `json:"unchanged_rules"`
}

// CreateProjectRuleCommentRequest is the dashboard payload for adding a note.
type CreateProjectRuleCommentRequest struct {
	VersionID   int64  `json:"version_id,omitempty"`
	CommentType string `json:"comment_type,omitempty"` // defaults to comment
	Body        string `json:"body"`
}

// UpdateProjectRuleChangeReasonRequest updates the human change reason for a version.
type UpdateProjectRuleChangeReasonRequest struct {
	ChangeReason string `json:"change_reason"`
}

// ErrTokenNotIngestion identifies a valid token issued for the read API.
var ErrTokenNotIngestion = errors.New("token is issued for reads, not ingestion")

// ErrTokenNotOpenAPI means the token is real and active but was issued for
// ingestion, not for reading. `cctrace init` hands those out so a machine can upload
// its sessions; they live in plaintext in ~/.cctrace/profile.json and nobody chose
// them as a key to the documented read API. Distinguished from "not found" so the
// caller can say what to do instead of "unauthorized".
var ErrTokenNotOpenAPI = errors.New("token is issued for ingestion, not for the read API")

// ErrCLIReadTokenAdmin means a CLI-issued read token (created_via cli_read)
// belongs to an administrator. The CLI refuses to issue such tokens because an
// administrator's token reads every user's data from a plaintext profile file;
// this keeps that true when the owner is promoted after issuance.
var ErrCLIReadTokenAdmin = errors.New("CLI read tokens are not accepted for administrators")

// DashboardUser represents a dashboard login account.
type DashboardUser struct {
	ID                 int64     `json:"id"`
	Email              string    `json:"email"`
	PasswordHash       string    `json:"-"`
	Role               string    `json:"role"`
	Name               string    `json:"name"`
	Team               string    `json:"team"`
	IsActive           bool      `json:"is_active"`
	CctraceUserID      string    `json:"cctrace_user_id"`
	MustChangePassword bool      `json:"must_change_password"`
	ApiToken           string    `json:"-"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// DashboardAPIToken is the non-secret metadata for one user-owned API token.
type DashboardAPIToken struct {
	ID         int64      `json:"id"`
	UserID     int64      `json:"-"`
	Name       string     `json:"name"`
	TokenHint  string     `json:"token_hint"`
	CreatedVia string     `json:"created_via"`
	IsActive   bool       `json:"is_active"`
	IsExpired  bool       `json:"is_expired"`
	ExpiresAt  *time.Time `json:"expires_at"`
	CreatedAt  time.Time  `json:"created_at"`
	RotatedAt  *time.Time `json:"rotated_at"`
}

// UpdateDashboardUserParams holds optional fields for updating a dashboard user.
type UpdateDashboardUserParams struct {
	Role          *string `json:"role,omitempty"`
	Name          *string `json:"name,omitempty"`
	Team          *string `json:"team,omitempty"`
	IsActive      *bool   `json:"is_active,omitempty"`
	CctraceUserID *string `json:"cctrace_user_id,omitempty"`
}

// PrivacySetting represents a privacy rule for a user's data.
type PrivacySetting struct {
	ID           int64     `json:"id"`
	ProfileEmail string    `json:"profile_email"`
	UserID       string    `json:"user_id,omitempty"`
	ScopeType    string    `json:"scope_type"`
	ScopeValue   string    `json:"scope_value"`
	CreatedAt    time.Time `json:"created_at"`
}

// SessionOverview holds aggregated session data sourced from otel_events,
// with a flag indicating whether session_records have been synced.
type SessionOverview struct {
	SessionID    string `json:"session_id"`
	ProfileEmail string `json:"profile_email"`
	UserID       string `json:"user_id"`
	LoginEmail   string `json:"login_email"`
	// AccountCount is how many distinct accounts the session's records belong to.
	// It is normally 1; more means the account was switched mid-session, and the
	// LoginEmail above is only the dominant one, not the whole story.
	AccountCount int `json:"account_count"`
	// LoginEmailInferred reports whether any record of the session carries a
	// login_email_source that is a guess rather than an observation: 'inferred', an
	// account derived from the user's OTEL timeline rather than observed on this
	// session (#346), or 'quota-inferred', an account carried over from a quota
	// mapping at least one link of which was itself inferred (#524). Plain 'quota'
	// is an observation and does not count. loginEmailInferredExpr is the single
	// definition both read paths share.
	//
	// One such row is enough: the header names a single representative account, and
	// calling that account observed while some rows behind it were guessed
	// overstates what is known. The UI marks it so nobody reads a guess as a
	// measurement.
	LoginEmailInferred bool      `json:"login_email_inferred"`
	Model              string    `json:"model"`
	Agent              string    `json:"agent"` // "claude" | "codex" | "gjc" | "omo"
	StartTime          time.Time `json:"start_time"`
	EndTime            time.Time `json:"end_time"`
	InputTokens        int64     `json:"input_tokens"`
	OutputTokens       int64     `json:"output_tokens"`
	CostUSD            float64   `json:"cost_usd"`
	EventCount         int64     `json:"event_count"`
	HasSync            bool      `json:"has_sync"`
	ProjectHash        string    `json:"project_hash"`
	ProjectName        string    `json:"project_name"`
	Entrypoint         string    `json:"entrypoint"` // cli | sdk-cli (headless) | claude-desktop; '' for legacy
	// HasEnriched reports whether any record in the session carries source_file,
	// i.e. whether file-split/assembly metadata was collected for this session.
	HasEnriched bool `json:"has_enriched"`
	// CctraceVersion is the cctrace collector version, aggregated from session_records;
	// empty for legacy sessions collected before CctraceVersionSince.
	CctraceVersion string `json:"cctrace_version,omitempty"`
	// ClaudeVersion is the Claude Code client version, aggregated from otel_events.service_version.
	ClaudeVersion string `json:"claude_version,omitempty"`
}

// QuotaSnapshot holds the latest Anthropic rate-limit utilization for a profile.
// One row per profile_email; updated on each sync-daemon polling cycle.
type QuotaSnapshot struct {
	ProfileEmail           string     `json:"profile_email"`
	UserID                 string     `json:"user_id,omitempty"`
	FiveHourPct            float64    `json:"five_hour_pct"`
	FiveHourResetsAt       *time.Time `json:"five_hour_resets_at,omitempty"`
	SevenDayPct            float64    `json:"seven_day_pct"`
	SevenDayResetsAt       *time.Time `json:"seven_day_resets_at,omitempty"`
	SevenDaySonnetPct      float64    `json:"seven_day_sonnet_pct,omitempty"`
	SevenDaySonnetResetsAt *time.Time `json:"seven_day_sonnet_resets_at,omitempty"`
	UpdatedAt              time.Time  `json:"updated_at"`
}

// QuotaSample is one reading of one rate-limit window of one billing account.
//
// It is deliberately not a wider QuotaSnapshot. The snapshot table is a single
// row per profile overwritten every five minutes, so nothing is left to draw a
// line from; and it hardcodes a column per window, which the API has already
// outgrown — seven_day_sonnet now comes back null and codename windows keep
// appearing beside it. Windows are rows here for that reason, which also lets
// Codex's numeric window_minutes buckets share the table.
//
// The key is (billing_provider, account_id, window_key, sampled_at). Several
// profiles report the *same* billing account, and that is a gain rather than a
// duplication: they all read one meter, each at its own moment, so the reports
// union into one denser series. Keying on profile_email would instead split one
// account into N lines and count it N times in the weighted average.
type QuotaSample struct {
	BillingProvider string    `json:"billing_provider"` // anthropic | openai
	AccountID       string    `json:"account_id"`
	WindowKey       string    `json:"window_key"`
	SampledAt       time.Time `json:"sampled_at"`

	UsedPct       float64    `json:"used_pct"`
	ResetsAt      *time.Time `json:"resets_at,omitempty"`
	WindowMinutes *int       `json:"window_minutes,omitempty"`
	Severity      string     `json:"severity,omitempty"`
	IsActive      *bool      `json:"is_active,omitempty"`
	ScopeLabel    string     `json:"scope_label,omitempty"`
	Plan          string     `json:"plan,omitempty"`

	// LoginEmail labels the account on screen and joins to the existing
	// login_email axis. ProfileEmail records which client reported this.
	// Neither is part of the key.
	LoginEmail   string `json:"login_email,omitempty"`
	ProfileEmail string `json:"profile_email,omitempty"`

	// Attribution says whether the account this is credited to was observed at
	// this instant or inferred across a gap between observations. A value that
	// is present is not the same as a value that is certain, and the chart has
	// to be able to say so.
	Attribution string `json:"attribution,omitempty"` // observed | inferred

	// SourceSessionID names the Codex session log this reading was read out of,
	// so an inferred attribution can be checked against the file that produced
	// it. Empty for live reads -- the Claude poller and the Codex app-server --
	// which were never inferred and have nothing to check against.
	//
	// The session UUID rather than the file path: a path embeds a home directory,
	// which would carry a person's account name into every row and every export.
	SourceSessionID string `json:"source_session_id,omitempty"`
}

// Attribution values for QuotaSample.
const (
	AttributionObserved = "observed"
	AttributionInferred = "inferred"
)

// QuotaSampleFilter bounds a history query.
type QuotaSampleFilter struct {
	From            time.Time
	To              time.Time
	BillingProvider string
	WindowMinutes   int
}

// ClientVersionUpdate is the client metadata reported by one sync request.
type ClientVersionUpdate struct {
	UserID        string
	ProfileEmail  string
	ClientVersion string
	ClientOS      string
	ClientArch    string
	// UpdateStall is what the client says about its own self-update. A nil
	// pointer means the client did not say -- builds before this field shipped
	// cannot -- and a non-nil zero value means it said there is nothing wrong.
	// Those two are stored differently on purpose: reading silence as "fine" is
	// the mistake #455 made about the version header, and it is the reason a
	// client stuck on v0.7.14 for a month looked no different from a current one.
	UpdateStall *ClientUpdateStall
}

// ClientUpdateStall is one client's own account of a self-update it cannot
// finish. The client already keeps this on disk to pace its retries
// (cmd/cctrace/update_stall.go); this is the same state, reported.
//
// The zero value means "not stalled", which is what a client sends after an
// update finally lands.
type ClientUpdateStall struct {
	// TargetVersion is the server version the client is failing to reach, not
	// the one it is running -- that is ClientVersion, and the gap between them is
	// the whole signal.
	TargetVersion string    `json:"target_version,omitempty"`
	Consecutive   int       `json:"consecutive,omitempty"`
	FirstFailedAt time.Time `json:"first_failed_at,omitempty"`
	LastFailedAt  time.Time `json:"last_failed_at,omitempty"`
	// Reason is the client's own error text. It names local paths, which is what
	// makes it useful: "permission denied" on its own does not say what to fix.
	Reason string `json:"reason,omitempty"`
}

// ClientVersionRecord holds the latest reported cctrace CLI version and platform for a profile.
// One row per profile_email; updated on each sync run that includes a version header.
type ClientVersionRecord struct {
	ProfileEmail string `json:"profile_email"`
	// Name is the dashboard account's display name, empty when the profile has
	// no dashboard_users row. Omitted from the payload when empty so the client
	// renders "no name on file" rather than an empty string that looks like a
	// name someone forgot to fill in.
	Name          string    `json:"name,omitempty"`
	UserID        string    `json:"user_id,omitempty"`
	ClientVersion string    `json:"client_version"`
	ClientOS      string    `json:"client_os"`
	ClientArch    string    `json:"client_arch"`
	LastSeenAt    time.Time `json:"last_seen_at"`
	// ConcurrentVersions counts the distinct versions this account's records
	// carried over the last day. More than one is the signature of a sync daemon
	// that was replaced on disk while an old resident process kept running: both
	// write, and neither is wrong from the server's side.
	//
	// client_versions holds one row per account and keeps only the newest string,
	// so it cannot express this by construction -- which is why the condition was
	// invisible for a week while two clients downloaded the same binary hundreds
	// of times a day (#623). The history lives in session_records.cctrace_version.
	ConcurrentVersions int `json:"concurrent_versions"`
	// UpdateReported says whether this account's client is new enough to report
	// its self-update state at all. False means unknown, not healthy: the
	// accounts furthest behind are exactly the ones that cannot report.
	UpdateReported      bool       `json:"update_reported"`
	UpdateTargetVersion string     `json:"update_target_version,omitempty"`
	UpdateFailCount     int        `json:"update_fail_count"`
	UpdateFirstFailedAt *time.Time `json:"update_first_failed_at,omitempty"`
	UpdateLastFailedAt  *time.Time `json:"update_last_failed_at,omitempty"`
	UpdateFailReason    string     `json:"update_fail_reason,omitempty"`
}

// RetentionConfig carries opt-in TimescaleDB retention overrides read from env.
// A nil field means "do not touch this table's policy" — the core safety
// invariant, so a plain code deploy (no env set) never changes retention.
type RetentionConfig struct {
	OtelDays    *int // otel_events + otel_metrics; nil = leave as-is (default 90d from migrations)
	SessionDays *int // session_records; nil = leave as-is (permanent, no policy)
}

// RetentionSetting is the durably-persisted admin choice for an axis's retention
// (the source of truth so a UI edit survives reboot). days: 0 = permanent.
type RetentionSetting struct {
	Axis      string    `json:"axis"`
	Days      int       `json:"days"`
	UpdatedBy string    `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableRetention is the current storage/retention state of one hypertable, for
// admin display. RetentionDays/CompressionDays are nil when no such policy set.
type TableRetention struct {
	Table           string `json:"table"`
	RetentionDays   *int   `json:"retention_days"`
	CompressionDays *int   `json:"compression_days"`
	RowsApprox      int64  `json:"rows_approx"`
	SizeBytes       int64  `json:"size_bytes"`
}

// StorageReport is the read-only retention/size snapshot surfaced to admins.
type StorageReport struct {
	Tables []*TableRetention `json:"tables"`
}

// RetentionPreview is a per-table dry-run of a retention change: how many rows a
// new N-day policy would make eligible for the async drop job. RowsToDrop is 0
// for a safe change (new >= current, or 0/permanent) so the UI can decide
// whether to demand typed confirmation.
type RetentionPreview struct {
	Table       string     `json:"table"`
	CurrentDays *int       `json:"current_days"` // nil = no policy
	NewDays     int        `json:"new_days"`
	OldestTs    *time.Time `json:"oldest_ts"` // nil = empty table
	RowsToDrop  int64      `json:"rows_to_drop"`
}

// Store defines the persistence interface for OTEL data.
type Store interface {
	// Lifecycle
	Migrate(ctx context.Context) error
	Close() error
	Ping(ctx context.Context) error

	// Write
	InsertEvent(ctx context.Context, e *OtelEvent) error
	InsertEvents(ctx context.Context, events []*OtelEvent) error
	InsertMetric(ctx context.Context, m *OtelMetric) error
	InsertMetrics(ctx context.Context, metrics []*OtelMetric) error
	InsertSessionRecords(ctx context.Context, records []*SessionRecord) error
	ReenrichSessionRecords(ctx context.Context, records []*SessionRecord) (int, error)
	UpsertProject(ctx context.Context, agent, projectHash, projectName, gitRemoteURL, repositoryID, repositoryName, repoSubpath string, lastSessionAt time.Time) error
	UpsertProjectWithMetadata(ctx context.Context, agent, projectHash, projectName string, metadata ProjectIdentityMetadata, lastSessionAt time.Time) error

	// Read - Events
	ListEvents(ctx context.Context, f EventFilter) ([]*OtelEvent, error)
	CountEvents(ctx context.Context, f EventFilter) (int64, error)

	// Read - Metrics
	ListMetrics(ctx context.Context, f MetricFilter) ([]*OtelMetric, error)
	ListSessionRecords(ctx context.Context, filter SessionRecordFilter) ([]*SessionRecord, error)
	ListSessionLineage(ctx context.Context, filter SessionRecordFilter) ([]*SessionRecord, error)
	ListSessionSummaries(ctx context.Context, profileEmail string, userID string, limit int) ([]*SessionSummary, error)
	ListSessionOverviews(ctx context.Context, f SessionOverviewFilter) ([]*SessionOverview, error)
	SessionAccountSegments(ctx context.Context, sessionID string) ([]*SessionAccountSegment, error)
	CountSessionOverviews(ctx context.Context, f SessionOverviewFilter) (int, error)
	UsageAggregates(ctx context.Context, f SessionOverviewFilter) (*UsageAggregate, error)

	// Read - Aggregations
	ListLoginAccounts(ctx context.Context, userID string) ([]string, error)
	ListUsers(ctx context.Context) ([]*UserInfo, error)
	ListProjects(ctx context.Context, f ProjectFilter) ([]*Project, error)
	IngestProjectRules(ctx context.Context, req *ProjectRuleIngestRequest) (*ProjectRuleIngestResponse, error)
	ListProjectRules(ctx context.Context, f ProjectRuleFilter) (*ProjectRuleListResponse, error)
	GetProjectRuleDetail(ctx context.Context, id, contentVersionID int64) (*ProjectRuleDetail, error)
	// ProjectRuleVisibleTo reports whether the given user/profile has session
	// history for the rule's repository. Used to gate detail/comment access.
	ProjectRuleVisibleTo(ctx context.Context, ruleID int64, userID, profileEmail string) (bool, error)
	CreateProjectRuleComment(ctx context.Context, ruleID int64, req *CreateProjectRuleCommentRequest, authorProfileEmail, authorUserID string) (*ProjectRuleComment, error)
	UpdateProjectRuleChangeReason(ctx context.Context, ruleID, versionID int64, changeReason, authorProfileEmail, authorUserID string) (*ProjectRuleVersion, error)
	CostByUser(ctx context.Context, since, until time.Time, profileEmail string, loginEmail string, userID string) ([]*CostSummary, error)
	CostByTeam(ctx context.Context, since, until time.Time, profileEmail string, userID string) ([]*CostSummary, error)
	CostByModel(ctx context.Context, since, until time.Time, profileEmail string, loginEmail string, userID string) ([]*ModelStat, error)
	ToolUsage(ctx context.Context, since, until time.Time, profileEmail string, loginEmail string, userID string) ([]*ToolUsageSummary, error)
	ToolTimeSeries(ctx context.Context, toolName string, since, until time.Time, profileEmail string, loginEmail string, userID string, granularity string, tz string) ([]*ToolTimeBucket, error)
	ToolFailures(ctx context.Context, toolName string, since, until time.Time, profileEmail string, loginEmail string, userID string, limit int) ([]*ToolFailure, error)
	PluginUsage(ctx context.Context, since, until time.Time, profileEmail string, loginEmail string, userID string, agent string) ([]*PluginUsageSummary, error)
	SkillUsage(ctx context.Context, since, until time.Time, profileEmail string, loginEmail string, userID string, agent string) ([]*SkillUsageSummary, error)
	DailyStats(ctx context.Context, f EventFilter) ([]*DailyStat, error)
	TimeSeriesStats(ctx context.Context, f EventFilter, granularity string) ([]*DailyStat, error)
	TimeSeriesStatsByModel(ctx context.Context, f EventFilter, granularity string) ([]*ModelDailyStat, error)
	LatestActivityTs(ctx context.Context, f EventFilter) (time.Time, bool, error)
	TimeSeriesStatsByUser(ctx context.Context, f EventFilter, granularity string) ([]*UserDailyStat, error)
	CoverageGapStats(ctx context.Context, f CoverageGapFilter) (*CoverageGap, error)
	SessionList(ctx context.Context, profileEmail string, limit, offset int) ([]string, error)
	DeleteUserData(ctx context.Context, profileEmail string, userID string) error
	MergeUsers(ctx context.Context, fromProfileEmail string, fromUserID string, toProfileEmail string) error
	UpsertAlias(ctx context.Context, fromEmail, toEmail string) error
	LoadAliases(ctx context.Context) (map[string]string, error)

	// CheckUserID checks whether a user_id exists in otel_events and returns associated profile_emails.
	CheckUserID(ctx context.Context, userID string) (bool, []string, error)

	// Dashboard Users
	ListOtelUserIDs(ctx context.Context) ([]string, error)
	AccountSwitchStats(ctx context.Context, since time.Time) ([]*AccountSwitchStat, error)
	PreviewBackfillSessionRecordLoginEmail(ctx context.Context, since time.Time) (*BackfillPreview, error)
	PreviewInferSessionRecordLoginEmail(ctx context.Context, since time.Time) (*LoginEmailInferencePreview, error)
	PreviewCodexInferredRevert(ctx context.Context) (*CodexInferredRevertPreview, error)
	PreviewCodexAccountFill(ctx context.Context, since time.Time) (*CodexAccountFillPreview, error)
	PreviewCodexQuotaAttribution(ctx context.Context, since time.Time) (*CodexQuotaAttributionPreview, error)
	CountDashboardUsers(ctx context.Context) (int, error)
	CreateInitialDashboardUser(ctx context.Context, u *DashboardUser) error
	CreateDashboardUser(ctx context.Context, u *DashboardUser) (*DashboardUser, error)
	GetDashboardUserByEmail(ctx context.Context, email string) (*DashboardUser, error)
	GetDashboardUserByID(ctx context.Context, id int64) (*DashboardUser, error)
	GetDashboardUserByCctraceUserID(ctx context.Context, cctraceUserID string) (*DashboardUser, error)
	ListDashboardUsers(ctx context.Context) ([]*DashboardUser, error)
	UpdateDashboardUser(ctx context.Context, id int64, p UpdateDashboardUserParams) error
	UpdateDashboardUserPassword(ctx context.Context, id int64, passwordHash string, mustChange bool) error
	GetDashboardUserByApiToken(ctx context.Context, token string) (*DashboardUser, error)
	// GetDashboardUserByOpenAPIToken resolves only tokens a person created for
	// reading. See ErrTokenNotOpenAPI.
	GetDashboardUserByOpenAPIToken(ctx context.Context, token string) (*DashboardUser, error)
	SetDashboardUserApiToken(ctx context.Context, id int64, token string) error
	ListDashboardUserAPITokens(ctx context.Context, userID int64) ([]*DashboardAPIToken, error)
	CreateDashboardUserAPIToken(ctx context.Context, userID int64, name, token, createdVia string, expiresAt *time.Time) (*DashboardAPIToken, error)
	RotateDashboardUserAPIToken(ctx context.Context, userID, tokenID int64, token string) (*DashboardAPIToken, error)
	SetDashboardUserAPITokenActive(ctx context.Context, userID, tokenID int64, active bool) (*DashboardAPIToken, error)
	SetDashboardUserAPITokenExpiration(ctx context.Context, userID, tokenID int64, expiresAt *time.Time) (*DashboardAPIToken, error)
	DeleteDashboardUserAPIToken(ctx context.Context, userID, tokenID int64) (bool, error)
	// DeleteCLIReadToken removes the caller's own CLI read token with this exact
	// secret, reporting whether one was removed.
	DeleteCLIReadToken(ctx context.Context, userID int64, secret string) (bool, error)
	DeleteAllDashboardUserAPITokens(ctx context.Context, userID int64) error

	// Privacy
	SetPrivacy(ctx context.Context, userID, profileEmail, scopeType, scopeValue string) error
	RemovePrivacy(ctx context.Context, userID, profileEmail, scopeType, scopeValue string) error
	ListPrivacySettings(ctx context.Context, userID, profileEmail string) ([]*PrivacySetting, error)
	IsPrivate(ctx context.Context, profileEmail, projectHash, sessionID string) (bool, error)
	PrivateSessionIDs(ctx context.Context, sessions []*SessionOverview) (map[string]bool, error)

	// User email resolution (alias integration)
	ResolveUserEmails(ctx context.Context, email string) ([]string, error)

	// Quota snapshots
	UpsertQuotaSnapshot(ctx context.Context, s *QuotaSnapshot) error
	GetQuotaSnapshot(ctx context.Context, profileEmail string) (*QuotaSnapshot, error)
	ListQuotaSnapshots(ctx context.Context) ([]*QuotaSnapshot, error)

	// Quota history
	InsertQuotaSamples(ctx context.Context, samples []*QuotaSample) (int, error)
	ListQuotaSamples(ctx context.Context, f QuotaSampleFilter) ([]*QuotaSample, error)

	// Client versions
	UpsertClientVersion(ctx context.Context, update ClientVersionUpdate) error
	ListClientVersions(ctx context.Context) ([]*ClientVersionRecord, error)

	// Retention (opt-in): reconcile drop-chunk policies to match RetentionConfig.
	ReconcileRetention(ctx context.Context, cfg RetentionConfig) error
	// RetentionInfo returns the current retention/compression/size snapshot
	// for the managed hypertables (admin read).
	RetentionInfo(ctx context.Context) (*StorageReport, error)
	// RetentionPreviewAxis dry-runs an N-day retention change for an axis
	// ("otel" -> otel_events+otel_metrics, "session" -> session_records),
	// reporting rows that would become drop-eligible. Never mutates.
	RetentionPreviewAxis(ctx context.Context, axis string, days int) ([]*RetentionPreview, error)
	// Retention settings: durable per-axis config. UpsertRetentionSetting records
	// an admin choice; EffectiveRetentionConfig resolves the boot config (env
	// override wins, else persisted setting, else nil = leave as-is).
	UpsertRetentionSetting(ctx context.Context, axis string, days int, updatedBy string) error
	ListRetentionSettings(ctx context.Context) ([]*RetentionSetting, error)
	EffectiveRetentionConfig(ctx context.Context, env RetentionConfig) (RetentionConfig, error)

	// Read - Health
	LatestEventTime(ctx context.Context) (*time.Time, error)

	// UsageRollupRebuildPending is whether an exclusion change is still waiting for
	// the usage rebuild it queued; the exclusion lists report it.
	UsageRollupRebuildPending(ctx context.Context) (bool, error)

	// Admin - Excluded accounts (hidden from the dashboard, rows kept)
	ListExcludedAccounts(ctx context.Context) ([]ExcludedAccount, error)
	// ExcludeAccount returns how many events the exclusion hides, for the audit trail.
	ExcludeAccount(ctx context.Context, loginEmail, reason, actor string) (int64, error)
	RemoveExcludedAccount(ctx context.Context, loginEmail string) error

	// Admin - Excluded billing accounts, for accounts with no login email to
	// exclude by. See ExcludedBillingAccount for why that case exists.
	ListExcludedBillingAccounts(ctx context.Context) ([]ExcludedBillingAccount, error)
	// ExcludeBillingAccount returns how many quota readings and usage events the exclusion hides.
	ExcludeBillingAccount(ctx context.Context, provider, accountID, reason, actor string) (samples, events int64, err error)
	RemoveExcludedBillingAccount(ctx context.Context, provider, accountID string) error

	// Admin - Unpriced models (#441): usage costed at $0 because no rate matched,
	// minus the models an admin marked flat-rate ("$0 is correct").
	ListUnpricedModels(ctx context.Context) ([]UnpricedModel, error)
	ListFlatRateModels(ctx context.Context) ([]FlatRateModel, error)
	MarkFlatRateModel(ctx context.Context, agent, model, reason, actor string) error
	UnmarkFlatRateModel(ctx context.Context, agent, model string) error

	// Self exclusion (#716): a user hides a billing account seen in their own data.
	// ListObservedBillingAccounts is that data's accounts, scoped like every other
	// user read; RemoveSelfExcludedBillingAccount reports false when the entry is
	// not the actor's own registration and leaves it in place.
	ListObservedBillingAccounts(ctx context.Context, profileEmail, userID, actor string) ([]ObservedBillingAccount, error)
	// BillingAccountUsedByOthers is whether anyone else's session records carry the account.
	BillingAccountUsedByOthers(ctx context.Context, provider, accountID, profileEmail, userID string) (bool, error)
	SelfExcludeBillingAccount(ctx context.Context, provider, accountID, reason, actor string) error
	RemoveSelfExcludedBillingAccount(ctx context.Context, provider, accountID, actor string) (bool, error)

	// Session deletion (rows removed, not hidden -- unlike excluded accounts above)
	//
	// SessionOwner reports the profile_email / user_id a session belongs to, so the
	// caller can decide whether a non-admin may delete it. Empty strings mean the
	// session is unknown, which is not the same as ownerless.
	SessionOwner(ctx context.Context, sessionID string) (profileEmail string, userID string, err error)
	// DeleteSession removes the session from every table and writes a tombstone that
	// keeps a later sync from re-adding it. Rejects an empty session id.
	DeleteSession(ctx context.Context, sessionID, actor, reason string, blockProject, purgeProject bool) (*DeleteSessionResult, error)
	DeleteProject(ctx context.Context, projectHashes []string, actor, reason string, blockProject bool) (*DeleteSessionResult, error)
	// ProjectSessionCount powers the "and the other N sessions?" step, so the number
	// has to be available before anything is deleted.
	ProjectSessionCount(ctx context.Context, projectHash, excludeSessionID string) (int, error)
	// Blocked projects refuse FUTURE collection; stored history is untouched.
	ListBlockedProjects(ctx context.Context) ([]BlockedProject, error)
	BlockProject(ctx context.Context, projectHash, projectName, actor, reason string) error
	UnblockProject(ctx context.Context, projectHash string) error
	// LoadIngestBlocklist feeds the ingest-path cache; PurgeBlockedProjects sweeps
	// rows that raced past it.
	LoadIngestBlocklist(ctx context.Context) (*IngestBlocklist, error)
	PurgeBlockedProjects(ctx context.Context) (int64, error)
	// SweepDeletedSessions reclaims the rows behind tombstones, in batches. The
	// tombstones already hide the sessions, so this is reclamation, not correctness.
	SweepDeletedSessions(ctx context.Context, limit int) (int64, error)
	GetDeletionPolicy(ctx context.Context) (*DeletionPolicy, error)
	SetDeletionPolicy(ctx context.Context, allowOwnerDelete bool, actor string) error

	// Maintenance
	// CleanupOrphanSessionRecords deletes session_records that have no matching otel_events.
	// Returns the number of deleted records.
	CleanupOrphanSessionRecords(ctx context.Context) (int64, error)
}
