package openinsights

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// ErrUploadTokenRefused is the capability route refusing the token it was
// asked with: none, a revoked one, or a read token in the upload token's place.
var ErrUploadTokenRefused = errors.New("the server refused the upload token")

// DecisionSourceSupported reports whether the server announces the
// open_api_decision_source capability: Open API events filtered by, and
// carrying, the source of a permission decision. The capability route takes
// the upload token, so c must hold one; a refusal of it is
// ErrUploadTokenRefused, since support cannot then be asked about. A missing
// route (404), another refusal or a missing flag is no support: only the flag
// says yes, never an empty result.
func (c *Client) DecisionSourceSupported(ctx context.Context) (bool, error) {
	var caps struct {
		OpenAPIDecisionSource bool `json:"open_api_decision_source"`
	}
	err := c.get(ctx, "/api/sync/capabilities", nil, &caps)
	var apiErr *APIError
	switch {
	case errors.As(err, &apiErr) && (apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden && apiErr.Code == "read_only_token"):
		return false, ErrUploadTokenRefused
	case errors.As(err, &apiErr) && (apiErr.Status == http.StatusForbidden || apiErr.Status == http.StatusNotFound):
		return false, nil
	case err != nil:
		return false, err
	}
	return caps.OpenAPIDecisionSource, nil
}

// DecisionEvent is the part of a tool_decision event the dependency audit
// reads.
type DecisionEvent struct {
	EventName      string    `json:"event_name"`
	SessionID      string    `json:"session_id"`
	ToolUseID      string    `json:"tool_use_id"`
	DecisionSource string    `json:"decision_source"`
	Ts             time.Time `json:"ts"`
	ToolName       string    `json:"tool_name"`
}

// DecisionEvents lists the tool decisions in w whose source is one of sources.
// A row that is not such a decision is dropped whatever the server sent. A row
// is identified by its tool_use_id, whatever session it names, as the
// dependency audit identifies a call; a row without one by its whole value.
func (c *Client) DecisionEvents(ctx context.Context, w Window, sources []string) ([]DecisionEvent, bool, error) {
	filter := url.Values{"event_name": {"tool_decision"}, "decision_source": {strings.Join(sources, ",")}}
	rows, truncated, err := fetchAll(ctx, c, "/api/open/v1/events", w, filter, decisionKey)
	if err != nil {
		return nil, false, err
	}
	kept := rows[:0]
	for _, r := range rows {
		if r.EventName == "tool_decision" && slices.Contains(sources, r.DecisionSource) {
			kept = append(kept, r)
		}
	}
	return kept, truncated, nil
}

func decisionKey(e DecisionEvent) string {
	if e.ToolUseID == "" {
		b, _ := json.Marshal(e)
		return string(b)
	}
	return e.ToolUseID
}

// Practices mirrors GET /api/open/v1/practices: people who show a practice in
// the server's fixed window against people who do not. It names practices,
// never people.
type Practices struct {
	WindowStart time.Time     `json:"window_start"`
	WindowEnd   time.Time     `json:"window_end"`
	Rows        []PracticeRow `json:"rows"`
	Self        PracticeRates `json:"self"`
	Caveats     []string      `json:"caveats"`
}

// PracticeRow carries UsersWithout and Metrics only when the server shows them.
type PracticeRow struct {
	Kind         string           `json:"kind"`
	Name         string           `json:"name"`
	UsersWith    int64            `json:"users_with"`
	UsersWithout *int64           `json:"users_without,omitempty"`
	Metrics      *PracticeMetrics `json:"metrics,omitempty"`
}

type PracticeMetrics struct {
	ManualApprovalsPer1000 PracticeComparison `json:"manual_approvals_per_1000"`
	HumanRejectionsPer1000 PracticeComparison `json:"human_rejections_per_1000"`
}

type PracticeComparison struct {
	With    float64 `json:"with"`
	Without float64 `json:"without"`
}

// PracticeRates is the caller's own rates; nil when they made no decision.
type PracticeRates struct {
	ManualApprovalsPer1000 *float64 `json:"manual_approvals_per_1000"`
	HumanRejectionsPer1000 *float64 `json:"human_rejections_per_1000"`
}

// Practices reads the practices comparison.
func (c *Client) Practices(ctx context.Context) (*Practices, error) {
	var p Practices
	if err := c.get(ctx, "/api/open/v1/practices", nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}
