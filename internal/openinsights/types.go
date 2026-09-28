// Package openinsights computes small, decision-oriented aggregates from the
// read-only Open API so an agent reads a summary instead of thousands of rows.
// It is client-side only; the server-side task classifier lives in
// internal/insights, which the CLI must not import.
package openinsights

import (
	"math"
	"time"
)

// unknownKey buckets rows whose model or project is not known.
const unknownKey = "(unknown)"

// lowVolumeSessions is the session count under which a window is too small to
// read trends from.
const lowVolumeSessions = 5

// Session mirrors one element of GET /api/open/v1/sessions.
type Session struct {
	SessionID    string    `json:"session_id"`
	UserID       string    `json:"user_id"`
	Model        string    `json:"model"`
	ProjectName  string    `json:"project_name"`
	StartTime    time.Time `json:"start_time"`
	EndTime      time.Time `json:"end_time"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	CostUSD      float64   `json:"cost_usd"`
}

// Event mirrors the fields of GET /api/open/v1/events this package reads.
type Event struct {
	Ts                time.Time `json:"ts"`
	SessionID         string    `json:"session_id"`
	UserID            string    `json:"user_id"`
	Model             string    `json:"model"`
	CostUSD           *float64  `json:"cost_usd"`
	InputTokens       *int      `json:"input_tokens"`
	CacheReadTokens   *int      `json:"cache_read_tokens"`
	CacheCreateTokens *int      `json:"cache_create_tokens"`
}

// Window is a half-open [Since, Until) time range.
type Window struct {
	Since time.Time `json:"since"`
	Until time.Time `json:"until"`
}

// Caveat is a data limitation the reader must report alongside the numbers.
type Caveat struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

func round4(v float64) float64 {
	return math.Round(v*1e4) / 1e4
}

// ratio returns nil when the denominator is zero so JSON carries null rather
// than a misleading 0.
func ratio(num, den float64) *float64 {
	if den == 0 {
		return nil
	}
	r := round4(num / den)
	return &r
}

// Scope is what the caller's token can read, which decides whether session IDs
// and project names may be shown. An administrator reads every user's rows, and
// per-session detail would then describe other people's work, which only the
// thresholded organization-insights report may.
type Scope int

const (
	// ScopeUser is a regular token; the server limits rows to the caller.
	ScopeUser Scope = iota
	// ScopeAdmin is an administrator token reading every user.
	ScopeAdmin
	// ScopeUnknown means the role could not be established; treated as admin.
	ScopeUnknown
)

// withholdDetail decides whether per-session and per-project detail is hidden,
// and why. Rows naming two or more users withhold detail even under ScopeUser,
// so a wrong role verdict cannot expose someone else's sessions.
func withholdDetail[T any](scope Scope, rows []T, userOf func(T) string) (bool, *Caveat) {
	switch scope {
	case ScopeAdmin:
		return true, &Caveat{"admin_scope", "read with an administrator token, which covers every user; session and project detail is withheld"}
	case ScopeUnknown:
		return true, &Caveat{"role_unknown", "the token's role could not be confirmed, so it is treated as an administrator's; session and project detail is withheld"}
	}
	named := map[string]bool{}
	for _, r := range rows {
		if u := userOf(r); u != "" {
			named[u] = true
		}
	}
	if len(named) > 1 {
		return true, &Caveat{"multiple_users", "rows name more than one user; session and project detail is withheld"}
	}
	return false, nil
}
