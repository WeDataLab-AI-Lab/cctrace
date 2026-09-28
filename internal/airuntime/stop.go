package airuntime

// Stop is why one response ended, normalized across providers. The wire field
// differs -- response.status, stop_reason, finish_reason -- and mapping it is
// each runtime's own business. What is not provider business is the outcome:
// the same end has to carry the same code and the same sentinel everywhere, or
// the API and the screen answer differently depending on which provider ran.
//
// The raw value is not thrown away: callers put it in the message so an
// unexpected one can still be read in the log and the stored run.
type Stop string

const (
	// StopFinal and StopToolUse continue the turn: an answer or another round
	// of tool calls. Neither is an error.
	StopFinal   Stop = "final"
	StopToolUse Stop = "tool_use"

	// StopOutputLimit is the model running out of output tokens mid-answer.
	StopOutputLimit Stop = "output_limit"
	// StopContextWindow is the conversation outgrowing the model's window.
	StopContextWindow Stop = "context_window"
	// StopRefused is the model declining.
	StopRefused Stop = "refused"
	// StopFailed is the provider failing to generate at all.
	StopFailed Stop = "failed"
	// StopUnknown is a stop value this runtime does not know.
	StopUnknown Stop = "unknown"
)

// StopError pairs a normalized stop with the code and sentinel it must carry,
// and returns nil while the turn continues. The message stays the caller's: it
// names provider specifics (a configured max_tokens, an unexpected raw value)
// that only that runtime knows.
//
// The codes are an API and screen contract -- internal/api/ai_reports.go and
// the weekly report panel map them to what the reader sees -- so they are
// spelled here exactly as they were spelled in the three runtimes.
func StopError(s Stop, message string) *RunError {
	switch s {
	case StopFinal, StopToolUse:
		return nil
	case StopOutputLimit:
		return NewRunError("output_limit", message, ErrBudget)
	case StopContextWindow:
		return NewRunError("context_window_exceeded", message, ErrBudget)
	case StopRefused:
		return NewRunError("refused", message, ErrProtocol)
	case StopFailed:
		return NewRunError("turn_failed", message, ErrUnavailable)
	default:
		return NewRunError("protocol_error", message, ErrProtocol)
	}
}
