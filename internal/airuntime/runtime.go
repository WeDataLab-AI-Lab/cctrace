// Package airuntime is the runtime-neutral contract between the weekly AI
// report service and whatever agent loop actually runs the model. It has no
// external dependencies so the service, the Codex driver and the tests can all
// import it without importing each other.
package airuntime

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Auth modes reported by Info.AuthMode.
const (
	AuthModeChatGPT = "chatgpt"
	AuthModeAPIKey  = "api_key"
	AuthModeNone    = "none"
)

// Runtime runs one agent turn with caller-provided tools.
type Runtime interface {
	Info() Info
	Status(ctx context.Context) Status
	// Run blocks until the turn ends. sink receives progress events in order and
	// is never called after Run returns.
	Run(ctx context.Context, req RunRequest, sink func(Event)) (*Result, error)
}

// Info is static identity: Key such as "codex-app-server", Model, AuthMode.
type Info struct {
	Key      string
	Model    string
	AuthMode string
}

// Status is the runtime's current readiness. UsedPercent is nil when the
// runtime did not report a rate-limit reading.
type Status struct {
	Configured   bool
	Available    bool
	Reason       string
	AccountEmail string
	PlanType     string
	UsedPercent  *float64
}

type RunRequest struct {
	// Model and ReasoningEffort are this run's choice; empty leaves it to the
	// runtime's own default.
	Model           string
	ReasoningEffort string
	Instructions    string
	Prompt          string
	Tools           []Tool
	OutputSchema    json.RawMessage
	ToolTimeout     time.Duration
	WallClock       time.Duration
	MaxToolCalls    int
	// MaxTotalTokens caps cumulative input+output tokens for the turn.
	MaxTotalTokens int64
}

type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	Handler     func(ctx context.Context, args json.RawMessage) (ToolOutput, error)
}

// ToolOutput is what a handler returns. Text goes to the model; Rows and
// ArgsSummary go to the tool-call log, which never stores the raw payload.
type ToolOutput struct {
	Text        string
	Rows        int
	ArgsSummary map[string]any
}

// Tool call statuses, shared by Event.Status and the tool-call log.
const (
	ToolStatusOK      = "ok"
	ToolStatusFailed  = "failed"
	ToolStatusTimeout = "timeout"
)

type EventKind string

const (
	EventToolCall   EventKind = "tool_call"
	EventToolResult EventKind = "tool_result"
	EventTextDelta  EventKind = "text_delta"
	EventUsage      EventKind = "usage"
	EventError      EventKind = "error"
)

// Event is one progress item. Seq is the 1-based tool call ordinal: a
// tool_call and its tool_result carry the same Seq; other kinds carry 0.
type Event struct {
	Kind       EventKind
	Seq        int
	Tool       string
	Args       map[string]any
	Status     string
	Rows       int
	DurationMs int
	Usage      *Usage
	Text       string
	Err        *RunError
}

// Usage is cumulative for the turn. Reported false means the runtime never
// sent token counts; the zero values then mean "unknown", not zero.
type Usage struct {
	Reported          bool
	InputTokens       int64
	CachedInputTokens int64
	OutputTokens      int64
}

type Result struct {
	FinalText string
	// Model is the model the runtime actually used, when it said; Info.Model
	// is only what was configured and is empty for the runtime's default.
	Model    string
	Usage    Usage
	Duration time.Duration
}

var (
	ErrNotConfigured  = errors.New("ai runtime not configured")
	ErrUnavailable    = errors.New("ai runtime unavailable")
	ErrTimeLimit      = errors.New("ai run exceeded its time limit")
	ErrCanceled       = errors.New("ai run canceled")
	ErrProtocol       = errors.New("ai runtime protocol error")
	ErrUnexpectedTool = errors.New("ai runtime used an unexpected tool")
	ErrBudget         = errors.New("ai run exceeded its budget")
)

// RunError carries a stable code for storage and the API alongside the
// sentinel it wraps, so callers can still use errors.Is.
type RunError struct {
	Code    string
	Message string
	Err     error
}

func (e *RunError) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

func (e *RunError) Unwrap() error { return e.Err }

// NewRunError builds a RunError. Each runtime had its own identical three-line
// helper for this; they can all sit on top of this one.
func NewRunError(code, message string, sentinel error) *RunError {
	return &RunError{Code: code, Message: message, Err: sentinel}
}
