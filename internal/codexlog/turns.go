package codexlog

import (
	"encoding/json"
	"math"
	"time"
)

// A rollout says how each of its turns ran in four kinds of line: the event_msg
// payloads task_started, task_complete and turn_aborted, and the turn_context
// line (docs/planning/session-detail-analysis/plan.md §4.1(6)). They are read
// during the scan as a side output, the way rate-limit readings are. Lifecycle
// facts never become Records. Instruction-bearing turn_context lines can also
// emit a separate context Record for inspection.

// TurnEvent is what one of those lines says about its turn.
//
// Only the fields below are decoded. task_complete's last_agent_message is the
// model's final reply, its error's message is the error's text, and
// turn_context's collaboration_mode settings carry the developer instructions:
// all are conversation text, and none is read into a TurnEvent.
type TurnEvent struct {
	// Kind is the line's kind: task_started, turn_context, task_complete or
	// turn_aborted.
	Kind string
	// Offset is where the line starts in its file, so a caller can keep the
	// events of the part of the file it collected.
	Offset int64
	// At is the line's own timestamp.
	At         time.Time
	TurnID     string
	RootTurnID string
	// StartedAt and CompletedAt are the payload's epoch seconds, zero when the
	// line has none or one past what a timestamp can say.
	StartedAt          time.Time
	CompletedAt        time.Time
	DurationMs         *int64
	TimeToFirstTokenMs *int64
	ModelContextWindow *int64
	// CollaborationModeKind is task_started's collaboration_mode_kind.
	CollaborationModeKind string
	// Model and Effort are turn_context's.
	Model  string
	Effort string
	// Reason is why a turn_aborted turn ended.
	Reason string
	// Error says a task_complete line carried an error: the turn ended in it.
	// ErrorInfo is the error's codex_error_info when that is a string, an enum
	// such as context_window_exceeded; "" when it is an object or absent.
	Error     bool
	ErrorInfo string
}

// turnEventKinds are the event_msg payload types that describe a turn.
var turnEventKinds = map[string]bool{"task_started": true, "task_complete": true, "turn_aborted": true}

// turnPayload is every field a TurnEvent keeps, of any of the four kinds. It is
// decoded apart from the decode the line's record goes through, so a field in a
// shape it does not expect costs that line its turn data and never a Record.
type turnPayload struct {
	TurnID                string   `json:"turn_id"`
	RootTurnID            string   `json:"root_turn_id"`
	StartedAt             *float64 `json:"started_at"`
	CompletedAt           *float64 `json:"completed_at"`
	DurationMs            *int64   `json:"duration_ms"`
	TimeToFirstTokenMs    *int64   `json:"time_to_first_token_ms"`
	ModelContextWindow    *int64   `json:"model_context_window"`
	CollaborationModeKind string   `json:"collaboration_mode_kind"`
	Model                 string   `json:"model"`
	Effort                string   `json:"effort"`
	Reason                string   `json:"reason"`
	// Error is kept raw: only its codex_error_info is read from it (turnError).
	Error json.RawMessage `json:"error"`
}

// turnError is the one member of a task_complete error a TurnEvent reads. Its
// message is the error's text and is never decoded.
type turnError struct {
	CodexErrorInfo json.RawMessage `json:"codex_error_info"`
}

// noteTurn keeps what a turn line says, unless the line is inherited fork
// history: the copied lines are the parent's turns, gated exactly as the
// parent's records are. A line without a turn_id names no turn and adds nothing.
//
// A metadata scan reads no turn at all (scanState.metadataOnly): it wants the
// file's context, and decoding every turn line of a prefix would be work thrown
// away.
func (s *scanState) noteTurn(kind string, ts time.Time, payload json.RawMessage) {
	if s.isSubagentFork && s.forkHasTriggerTurn && !s.forkBoundaryReached {
		return
	}
	var p turnPayload
	if json.Unmarshal(payload, &p) != nil || p.TurnID == "" {
		return
	}
	s.updateActiveTurn(kind, p)
	if s.metadataOnly {
		return
	}
	var errorInfo string
	hasError := len(p.Error) > 0 && string(p.Error) != "null"
	if hasError {
		var e turnError
		if json.Unmarshal(p.Error, &e) == nil {
			_ = json.Unmarshal(e.CodexErrorInfo, &errorInfo)
		}
	}
	s.turns = append(s.turns, TurnEvent{
		Kind:                  kind,
		Offset:                s.offset,
		At:                    ts,
		TurnID:                p.TurnID,
		RootTurnID:            p.RootTurnID,
		StartedAt:             epochSeconds(p.StartedAt),
		CompletedAt:           epochSeconds(p.CompletedAt),
		DurationMs:            p.DurationMs,
		TimeToFirstTokenMs:    p.TimeToFirstTokenMs,
		ModelContextWindow:    p.ModelContextWindow,
		CollaborationModeKind: p.CollaborationModeKind,
		Model:                 p.Model,
		Effort:                p.Effort,
		Reason:                p.Reason,
		Error:                 hasError,
		ErrorInfo:             errorInfo,
	})
}

func (s *scanState) updateActiveTurn(kind string, p turnPayload) {
	root := p.RootTurnID
	if root == "" {
		root = p.TurnID
	}
	switch kind {
	case "task_started":
		s.activeTurnID, s.activeRootTurnID = p.TurnID, root
	case "turn_context":
		if s.activeRootTurnID != "" && (p.RootTurnID == "" || p.RootTurnID == s.activeRootTurnID) {
			s.activeTurnID = p.TurnID
		}
	case "task_complete", "turn_aborted":
		switch p.TurnID {
		case s.activeRootTurnID:
			s.activeTurnID, s.activeRootTurnID = "", ""
		case s.activeTurnID:
			if s.activeRootTurnID != "" && s.activeRootTurnID != s.activeTurnID {
				s.activeTurnID = s.activeRootTurnID
			} else {
				s.activeTurnID, s.activeRootTurnID = "", ""
			}
		}
	}
}

// maxEpochSeconds is the first second of the year 10000, past which a time does
// not encode as JSON: one such value would fail every request carrying it.
const maxEpochSeconds = 253402300800

// epochSeconds is the time of an epoch-seconds field, fraction included, or zero
// for a missing, non-positive or out-of-range one.
func epochSeconds(v *float64) time.Time {
	if v == nil || *v <= 0 || *v >= maxEpochSeconds {
		return time.Time{}
	}
	sec, frac := math.Modf(*v)
	return time.Unix(int64(sec), int64(frac*1e9)).UTC()
}
