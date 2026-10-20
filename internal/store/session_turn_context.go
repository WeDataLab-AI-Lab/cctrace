package store

import (
	"context"
	"errors"
)

var ErrTurnContextTurnNotFound = errors.New("turn not found")
var ErrTurnContextInvalid = errors.New("invalid turn context request")
var ErrTurnContextRevision = errors.New("expired turn context revision")

// TurnContextStore reads canonical membership independently of trace demand.
type TurnContextStore interface {
	TurnContext(context.Context, string, string, SessionAnalysisAccess, TurnContextRequest) (TurnContextResponse, error)
}

type TurnContextRequest struct {
	Revision, Cursor string
	Limit            int
}

type TurnContextResponse struct {
	SessionID  string           `json:"session_id"`
	TurnKey    string           `json:"turn_key"`
	State      string           `json:"state"`
	Revision   string           `json:"revision"`
	Records    []*SessionRecord `json:"records"`
	NextCursor *string          `json:"next_cursor"`
}
