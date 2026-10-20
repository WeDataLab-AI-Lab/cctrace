// Package sessionfacts computes a session's analysis facts -- its session row,
// turn rows and activity segments -- from the rows a worker read for it
// (p1b-design §3, computeSessionFacts). It has no database and no clock: Compute
// is a function of its Input alone, so the rules are pinned by table fixtures
// and the same rows give the same facts whatever order they arrived in.
package sessionfacts

import (
	"encoding/json"
	"time"
)

// RulesVersion is the latest worker rule set (p1b-design §2.1,
// SessionFactsRulesVersion). The r2-only preparation entry points keep their
// historical baseRulesVersion; the combined worker entry point applies this
// version and hashes the P4 and copy-classification facts into its output.
const RulesVersion = 6
const baseRulesVersion = 2

// Input is everything one computation reads (p1b-design §3.1, §3.10, §4, §5.3),
// as the worker's read-only snapshot gave it. The slices are in no particular
// order; Compute sorts what it reads.
//
// A NULL text column is the empty string and a NULL jsonb column is nil: no rule
// tells a NULL from an empty text.
type Input struct {
	// ReadAt is the worker's snapshot time. It decides whether a Codex turn
	// with no end line has gone untouched for more than a day; zero means the
	// read time was not supplied and the age cannot be judged.
	ReadAt time.Time

	// Agent is the session's agent, "claude" or "codex". A session can have no
	// row to read it from -- one known only by its telemetry or its snapshot --
	// and the worker has to know it anyway to leave gjc and omo sessions alone.
	Agent string

	// Rows are a Claude session's visible rows (claudeSlimRowsSQL), main thread
	// and subagents alike. Empty for a Codex session.
	Rows []ClaudeRow
	// CodexRows are a Codex session's visible rows. Empty for a Claude session.
	CodexRows []CodexRow

	// Heads are the session's requests (p1b-design §3.10): for Claude one row
	// per request of claudeRequestHeadsSQL over the visible rows, for Codex one
	// per codex_imputed_cost row.
	Heads []Head

	// OTEL are the session's visible api_request events.
	OTEL []OTELRow
	// HasOTEL says the session has an otel_events row of any kind, visible or
	// not, which is what makes it an OTEL session (p1b-design §4.1).
	HasOTEL bool
	// OTELOldest is the oldest ts otel_events still holds across all sessions:
	// what started before it may have lost events to retention (p1b-design
	// §4.4). Zero when the table is empty.
	OTELOldest time.Time

	// Snapshots are the session's session_snapshots rows, one per identity that
	// sent one.
	Snapshots []Snapshot

	// CommitResults are bounded, selected tool results from the same snapshot as
	// Rows. Each must match its row, source file and tool_use id.
	CommitResults []CommitResult
	// CommitClaims is the explicit cross-session check for a repository and full
	// hash. A missing check is unknown, not evidence of an uncontested claim.
	CommitClaims []CommitClaim

	// CopiedIDs are the session_records ids of this session marked as copies of
	// another session's lines (session_record_copies), and CopyGate says the copy
	// predicate is on (the session_fact_copies_v1 marker). The marks are skipped
	// only while the gate is on, as in the shared request SQL (p1b-design §3.16).
	CopiedIDs []int64
	CopyGate  bool
	// CopyState and CopySource describe the raw-source classifier result that
	// accompanies this calculation. Empty means that the caller did not run it.
	CopyState  string
	CopySource string

	// TranscriptIO contains only text counters derived in the same snapshot.
	// The loader discards each raw record after counting it.
	TranscriptIO *TranscriptIODetail
}

// ClaudeRow is one row of claudeSlimRowsSQL (p1b-design §3.1), a field per
// column in the statement's order. The columns sync fills before redaction --
// command_name, command_invoke, prompt_source, is_meta, is_compact_summary --
// say what a line is whatever the policy did to its raw.
type ClaudeRow struct {
	ID                int64
	TS                time.Time
	RecordType        string
	UUID              string
	ParentUUID        string
	IsSidechain       bool
	AgentID           string
	SourceFile        string
	ToolUseID         string
	IsCompactSummary  bool
	IsMeta            bool
	PromptSource      string
	Entrypoint        string
	CommandName       string
	CommandInvoke     string
	Model             string
	ForkedFromSession string
	UserID            string
	ProfileEmail      string
	LoginEmail        string
	LoginEmailSource  string
	BillingProvider   string
	AccountID         string
	ProjectHash       string
	RepositoryID      string
	RepoSubpath       string
	Branch            string
	TaskType          string
	MessageID         string
	CWD               string

	// The collection marks of sessionlog (raw->'cctraceCollect' and its two
	// lists), which decide how an absent field reads (p1b-design §3.2).
	CctraceCollect  json.RawMessage
	CctraceDropped  json.RawMessage
	CctraceWithheld json.RawMessage

	PromptID             string
	OriginKind           string // raw->'origin'->>'kind'
	TurnOrigin           string
	TurnPosition         json.RawMessage
	PermissionMode       string
	ToolDenialKind       string
	InterruptedMessageID string
	ImagePasteCount      json.RawMessage
	ToolUseResult        json.RawMessage
	IsAPIErrorMessage    json.RawMessage
	HasError             bool   // raw ? 'error'
	RawRedacted          string // raw->>'redacted': the marker when the whole raw was withheld
	Subtype              string
	LogicalParentUUID    string

	PendingBackgroundAgentCount json.RawMessage
	PendingWorkflowCount        json.RawMessage
	Attachment                  json.RawMessage

	// ContentKind is jsonb_typeof(message.content): "string", "array", or ""
	// for a line without one.
	ContentKind string
	// TextHead is the first 512 characters of a user line's text -- a string
	// content, or its first text or input_text block -- and "" for every other
	// line.
	TextHead string
	// Blocks is the summary of an array content, one object per block: t (type),
	// id, n (name), tu (tool_use_id), err (is_error) and in, the few tool inputs
	// the rules read.
	Blocks json.RawMessage
}

// CodexRow is one row of the Codex projection (p1b-design §3.1). The design
// lists the identity columns without naming them; ProjectHash, RepositoryID and
// Branch are here because the session row carries a project, a repository key
// and a branch for a Codex session too.
//
// The token columns are the row's own, cache reads still inside input. A Codex
// session's token sums come from its Heads.
type CodexRow struct {
	projection *codexProjection
	ID         int64
	TS         time.Time
	RecordType string
	SourceFile string
	// UUID is the storage key's uuid: sync's content digest for a Codex line,
	// which names the row and says nothing about its place. Empty when the
	// loader did not read it.
	UUID                   string
	Model                  string
	InputTokens            int64
	OutputTokens           int64
	CacheReadTokens        int64
	CacheCreateTokens      int64
	UserID                 string
	ProfileEmail           string
	LoginEmail             string
	LoginEmailSource       string
	BillingProvider        string
	AccountID              string
	ProjectHash            string
	RepositoryID           string
	Branch                 string
	Entrypoint             string
	RawType                string // raw->>'type'
	Role                   string // raw->'payload'->'message'->>'role'; legacy raw->>'role' may be absent from this projection
	CommandName            string // parsed from the legacy user text or rollup projection
	PayloadType            string // raw->'payload'->>'type'
	TurnID                 string // raw->'payload'->>'turn_id'
	RootTurnID             string // raw->'payload'->>'root_turn_id'
	CodexTurnID            string // lifecycle turn id reconstructed by the collector in JSONL order
	CodexRootTurnID        string // active root turn id reconstructed by the collector
	CodexTurnMetadataKnown bool   // true when the collector had exact lifecycle context for this row
	ResponseID             string // raw->'payload'->>'response_id'
	ToolName               string
	ToolCallID             string
	ToolArgs               string
	ToolArgsTruncated      bool
	ToolOutput             string
	ToolStatus             string
	CWD                    string
	TextHead               string // a user row's first text block, 512 characters
	// ContextWindow is the last usage record's model_context_window when known.
	ContextWindow *int
}

func codexRowTurnID(row *CodexRow) string {
	if row.CodexTurnID != "" {
		return row.CodexTurnID
	}
	return row.TurnID
}

func codexRowRootTurnID(row *CodexRow) string {
	if row.CodexRootTurnID != "" {
		return row.CodexRootTurnID
	}
	return row.RootTurnID
}

func codexRowHasTurnID(row *CodexRow) bool {
	return codexRowTurnID(row) != "" || codexRowRootTurnID(row) != ""
}

// Head is one request (p1b-design §3.10): the columns of its head line and the
// request's tokens, each field's MAX over the request's lines.
//
// A Claude head is the head line of claudeRequestHeadsSQL. A Codex head is a
// codex_imputed_cost row: ID is its srec_id and the tokens are the imputed
// ones, input net of cache reads.
type Head struct {
	ID   int64 // session_records id of the head line
	UUID string
	TS   time.Time
	// SourceFile and AgentID place a subagent's request in its thread, which
	// is what gets attributed to a turn (p1b-design §3.7). The loader must
	// carry the head line's source_file, just as it does for ClaudeRow.
	IsSidechain bool
	AgentID     string
	SourceFile  string
	Model       string

	InputTokens       int64
	OutputTokens      int64
	CacheReadTokens   int64
	CacheCreateTokens int64

	// Imputed says an imputed cost row exists for this head
	// (claude_imputed_cost or codex_imputed_cost, by srec_id) and CostUSD is its
	// cost_usd. HasRate says a rate exists for the head's model at its ts,
	// whether or not the 30-minute rewrite has priced it yet: a head with a rate
	// and no row is pending, one with neither will never have a cost (p1b-design
	// §4.4).
	Imputed bool
	CostUSD float64
	HasRate bool
}

// OTELRow is one visible api_request event of the session. Its account id is
// always empty: telemetry carries none (unified_events).
type OTELRow struct {
	TS              time.Time
	PromptID        string
	CostUSD         float64 // NULL is 0
	UserID          string
	ProfileEmail    string
	LoginEmail      string
	BillingProvider string
}

// Snapshot is one session_snapshots row: the identity that sent it and what its
// document says (plan §4.1(2), syncer.SessionSnapshot).
//
// The three titles are pointers because their absence is a statement. nil is a
// key the client left out -- withheld, as a default re-enrichment does -- ""
// says the session has no such line, and the redaction marker says a policy hid
// it.
type Snapshot struct {
	UserID          string
	ProfileEmail    string
	LoginEmail      string
	BillingProvider string
	AccountID       string
	// ObservedAt picks the snapshot the title is read from when a session has
	// several: the latest, as an administrator's read does (p1b-design §8.2).
	ObservedAt time.Time

	AITitle     *string
	CustomTitle *string
	AgentName   *string
	CostStates  []SnapshotCostState
	PRLinks     []SnapshotPRLink
	CodexTurns  []SnapshotCodexTurn
	ContinuedIn []string
	// Commits nil means this snapshot did not collect commit observations.
	Commits *SnapshotCommits
	// Truncated names the lists above that reached their cap.
	Truncated []string
}

// SnapshotCodexTurn mirrors the P0d lifecycle fields needed to place a root
// turn and judge its end. ContextWindow is nil when the source gave no window.
type SnapshotCodexTurn struct {
	TurnID          string
	RootTurnID      string
	StartedAt       time.Time
	StartedAtSource string
	EndedAt         time.Time
	EndedAtSource   string
	TimestampsExact bool
	End             string
	ContextWindow   *int
}

// SnapshotCostState is where one run of the session (one startTime) last stood.
type SnapshotCostState struct {
	StartTimeMs         int64
	TotalCostUSD        float64
	LinesAdded          int64
	LinesRemoved        int64
	Models              []string
	HasUnknownModelCost bool
}

// SnapshotPRLink is a PR the session linked, as first seen.
type SnapshotPRLink struct {
	URL         string
	Number      int64
	Repository  string
	FirstSeenAt time.Time
}
