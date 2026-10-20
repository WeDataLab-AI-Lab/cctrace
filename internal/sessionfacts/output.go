package sessionfacts

import (
	"time"

	"cctrace/internal/sessiontranscript"
)

// Output is the rows one computation publishes (p1b-design §2.3): the session's
// row of session_analysis_facts, its rows of session_turn_facts in ordinal
// order, and its rows of session_activity_segments in time order.
//
// The session id is the caller's and no row repeats what the session row says:
// a turn or segment row takes its agent and rules_version from Session, and a
// segment row its two owner columns.
type Output struct {
	Session  Session
	Turns    []Turn
	Segments []Segment
	// Hash is the normalised hash of everything above but the session row's
	// bookkeeping columns (p1b-design §5.3 step 6). A publish that finds it
	// equal to the stored one rewrites nothing.
	Hash []byte
}

// Session is the session's row of session_analysis_facts. A pointer
// is a nullable column.
type Session struct {
	Agent    string
	Coverage string // always "full": the truncated path does not compute

	// The identities of every contributing row -- JSONL, api_request events,
	// snapshots -- distinct and sorted, the empty string included: an empty
	// user_id is a row that is no one's. An owner column is set only when its
	// list is one non-empty value (p1b-design §2.1).
	UserIDs            []string
	ProfileEmails      []string
	LoginEmails        []string
	OwnerUserID        *string
	OwnerProfileEmail  *string
	LoginEmailInferred bool
	AccountKeys        []AccountKey

	ProjectHash   string
	RepositoryKey string // repository_id, else project_hash
	Branch        string
	Model         string
	Transcript    bool // has JSONL rows

	Title       *string
	TitleSource string // custom | ai | agent_name | first_request | none
	TitleState  string // ok | withheld | redacted | none

	FirstActivityAt *time.Time
	LastActivityAt  *time.Time
	FirstRecordAt   *time.Time
	ActiveSeconds   int // the segments' lengths, summed

	// TurnBoundaries is observed or undeterminable. When it is undeterminable
	// there are no turn rows and the four counts are nil. Codex turns use
	// lifecycle and usage rows under p1b-design §3.8.
	TurnBoundaries       string
	TurnBoundariesReason string
	Turns                *int // active turns
	Requests             *int // active request turns, and the human inputs queued inside turns
	Inputs               *int
	AbandonedTurns       *int

	APIRequests       int
	InputTokens       int64
	OutputTokens      int64
	CacheReadTokens   int64
	CacheCreateTokens int64
	CostUSD           *float64
	CostSource        string // otel | otel_partial | estimate | estimate_pending | none

	LastTurnKey         string // the last active turn
	LastTurnStatus      string
	LastContextTokens   *int64
	LastContextWindow   *int
	LastContextSource   string
	LargeWindowSeen     bool
	OpenQuestions       *int
	OpenQuestionTurnKey string
	HasPlanFile         bool
	UsedPlanMode        bool
	Detail              SessionDetail
	RulesVersion        int

	// The bookkeeping columns. Compute leaves them zero: they say when and from
	// which queue generation a row was written, which is the publish's to know,
	// and the hash leaves them out so that recomputing the same facts is not a
	// change.
	ComputedAt       time.Time `hash:"-"`
	ComputeMS        int       `hash:"-"`
	RecomputeCount   int64     `hash:"-"`
	SourceGeneration int64     `hash:"-"`
}

// AccountKey is one element of account_keys. The read predicate unpacks the
// column by these names (jsonb_to_recordset, p1b-design §2.1) to hide a session
// an account exclusion reached after it was computed.
type AccountKey struct {
	LoginEmail      string `json:"login_email"`
	BillingProvider string `json:"billing_provider"`
	AccountID       string `json:"account_id"`
}

// SessionDetail is the part of the session row's detail document these rules
// compute.
type SessionDetail struct {
	Cost                 CostDetail            `json:"cost"`
	SubagentUnattributed SubagentUnattributed  `json:"subagent_unattributed"`
	ContextMix           ContextMix            `json:"context_mix"`
	Signals              Signals               `json:"signals"`
	EditedFiles          EditedFiles           `json:"edited_files"`
	ChangeArea           AreaFact              `json:"change_area"`
	TaskType             TaskTypeFact          `json:"task_type"`
	ToolCalls            int                   `json:"tool_calls"`
	SubagentsRunning     *int                  `json:"subagents_running"`
	Plans                []PlanReference       `json:"plans"`
	RecapHint            RecapHint             `json:"recap_hint"`
	Copy                 *CopyStateDetail      `json:"copy,omitempty" hash:"-"`
	CodexTurnAttribution *CodexTurnAttribution `json:"codex_turn_attribution,omitempty" hash:"-"`
	TranscriptIO         *TranscriptIODetail   `json:"transcript_io,omitempty" hash:"-"`
}

const TranscriptIOVersion = 1

// TranscriptIODetail preserves scalar measurements separately for each stored
// identity. The read path chooses only the currently verified owner's entry.
type TranscriptIODetail struct {
	Version int                 `json:"version"`
	Owners  []TranscriptIOOwner `json:"owners"`
}

type TranscriptIOOwner struct {
	UserID       string                        `json:"user_id"`
	ProfileEmail string                        `json:"profile_email"`
	Measurement  sessiontranscript.Measurement `json:"measurement"`
}

// CopyStateDetail describes whether raw-source duplicate ownership was
// classified for this session. Source names the evidence path and is retained
// when the decision cannot be made.
type CopyStateDetail struct {
	State  string `json:"state"`
	Source string `json:"source,omitempty"`
}

// CodexTurnAttribution preserves uncertainty when a legacy row falls on a
// same-second lifecycle boundary without an exact collected turn id.
type CodexTurnAttribution struct {
	State  string `json:"state"`
	Source string `json:"source"`
	Reason string `json:"reason"`
}

// CostDetail splits the session's cost between its turns and the rest. In an
// OTEL session the rest is the api_request events no turn could take: no
// prompt_id, one no turn has, or one several turns share. In an estimated
// session it is the imputed cost of the requests attached to no turn. Either
// way the two add up to the session's cost (p1b-design §4.4).
type CostDetail struct {
	TurnAttachedUSD   float64 `json:"turn_attached_usd"`
	UnattachedTurnUSD float64 `json:"unattached_turn_usd"`
	// Note is otel_expired when the session's telemetry is gone and only its
	// trace is left, so the cost shown is an estimate of what was once measured.
	Note string `json:"note,omitempty"`
}

// SubagentUnattributed counts the subagent requests no turn could be found for.
// They are in the session's totals all the same. Tokens is all four token
// fields, summed.
type SubagentUnattributed struct {
	Requests int   `json:"requests"`
	Tokens   int64 `json:"tokens"`
}

// Turn is a turn's row of session_turn_facts.
type Turn struct {
	TurnKey string // uuid of the turn's first line
	Ordinal int    // start order from 1, abandoned branches included
	// RequestNo numbers the active request turns from 1; nil for any other turn.
	RequestNo      *int
	Branch         string // active | abandoned
	OpenerCopied   bool   // some of the turn's lines are another session's
	PromptID       string
	PromptIDShared bool // another turn has the same prompt id (rule 1-b)
	OpenerUUID     string
	StartedAt      time.Time
	EndedAt        time.Time
	InputKind      string
	IsRequest      bool
	LoginEmail     string // the opening line's
	Status         string // ended | interrupted | error | no_response | open | undeterminable
	StatusReason   string
	PermissionMode string
	Denied         bool
	ImageCount     int
	Preview        *string // first line of the turn's input, 160 bytes
	PreviewState   string  // ok | redacted | none

	APIRequests       int
	InputTokens       int64
	OutputTokens      int64
	CacheReadTokens   int64
	CacheCreateTokens int64
	CostUSD           *float64
	// CostSource is otel, otel_partial, expired, estimate, estimate_pending,
	// no_rate, unattributable or none; CostReason says why a turn is
	// unattributable: pre_p0 or shared_prompt_id.
	CostSource             string
	CostReason             string
	ContextTokens          *int64
	ContextWindow          *int
	ContextSource          string
	ToolCalls              int
	Subagents              int
	LastAssistantMessageID string

	Detail TurnDetail
}

// TurnDetail is the part of a turn row's detail document these rules compute.
type TurnDetail struct {
	// Inputs are the turn's inputs on the active path that are not copies, each
	// with its number among the session's inputs.
	Inputs []TurnInput `json:"inputs"`
	// ExtraInputs counts the prompts a person queued while the turn was at work,
	// which are requests without a turn of their own, and ExtraInputTS is when.
	ExtraInputs  int                 `json:"extra_inputs"`
	ExtraInputTS []time.Time         `json:"extra_input_ts"`
	Questions    []QuestionReference `json:"questions"`
	Edited       TurnEdited          `json:"edited"`
	Plans        []TurnPlanReference `json:"plans"`
}

// TurnInput is one input (p1b-design §3.9): the line it is, its source, and N,
// its place among all of the session's inputs in time order.
type TurnInput struct {
	UUID string    `json:"uuid"`
	TS   time.Time `json:"ts"`
	Kind string    `json:"kind"`
	N    int       `json:"n"`
}

// Signal distinguishes an observed zero from a value that cannot be judged.
type Signal struct {
	State  string `json:"state"`
	Value  *int   `json:"value,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type Signals struct {
	Turn       Signal `json:"turn"`
	Question   Signal `json:"question"`
	Background Signal `json:"background"`
	Todo       Signal `json:"todo"`
}

type ContextMix struct {
	Input       int64 `json:"input"`
	CacheRead   int64 `json:"cache_read"`
	CacheCreate int64 `json:"cache_create"`
}

type EditedFile struct {
	Path           string `json:"path"`
	Edits          int    `json:"edits"`
	SubagentEdits  int    `json:"subagent_edits"`
	AbandonedEdits int    `json:"abandoned_edits"`
}

type EditedFiles struct {
	Total          int          `json:"total"`
	ActiveTotal    int          `json:"active_total"`
	AbandonedTotal int          `json:"abandoned_total"`
	Items          []EditedFile `json:"items"`
	Truncated      bool         `json:"truncated"`
	State          string       `json:"state"`
}

type AreaFact struct {
	Value  string `json:"value"`
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

type TaskTypeFact struct {
	Value string  `json:"value"`
	Share float64 `json:"share"`
}

type QuestionReference struct {
	ToolUseID    string   `json:"tool_use_id"`
	SrecID       int64    `json:"srec_id"`
	AnswerSrecID int64    `json:"answer_srec_id"`
	Headers      []string `json:"headers"`
	Status       string   `json:"status"`
	State        string   `json:"state"`
}

type TurnEdited struct {
	Count     int      `json:"count"`
	Sample    []string `json:"sample"`
	Abandoned int      `json:"abandoned"`
}

type TurnPlanReference struct {
	SrecID       int64  `json:"srec_id"`
	ToolUseID    string `json:"tool_use_id"`
	ResultSrecID int64  `json:"result_srec_id"`
}

type PlanReference struct {
	TurnKey          string    `json:"turn_key"`
	SrecID           int64     `json:"srec_id"`
	ToolUseID        string    `json:"tool_use_id"`
	ResultSrecID     int64     `json:"result_srec_id"`
	AttachmentSrecID int64     `json:"attachment_srec_id,omitempty"`
	FilePath         string    `json:"file_path"`
	Decision         string    `json:"decision"`
	TS               time.Time `json:"ts"`
}

type RecapHint struct {
	SystemRowsCollected bool `json:"system_rows_collected"`
	Turns               int  `json:"turns"`
	Headless            bool `json:"headless"`
}

// Segment is a row of session_activity_segments: a stretch of main-thread
// activity with no gap of 15 minutes inside it. A segment of one line starts and
// ends at the same instant.
type Segment struct {
	SegStart    time.Time
	SegEnd      time.Time
	Records     int
	HumanInputs int
}
