package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"cctrace/internal/aireport"
)

// aiUserScheduleJSON is when this user's report runs by itself, already
// resolved across their own row, the admin's default and the built-in one.
//
// The sources let the screen say "관리자 기본값" against a value the user has
// not set, which reads differently from one they chose. NextRun is the firing
// the screen prints; it is null when automatic runs are off, because there is
// no next one to name.
type aiUserScheduleJSON struct {
	Enabled       bool       `json:"enabled"`
	EnabledSource string     `json:"enabled_source"`
	Weekday       int        `json:"weekday"`
	Hour          int        `json:"hour"`
	Minute        int        `json:"minute"`
	WhenSource    string     `json:"when_source"`
	TZ            string     `json:"tz"`
	NextRun       *time.Time `json:"next_run"`
}

// userScheduleJSON renders a resolved schedule for the weekly screen. A firing
// that cannot be computed -- a zone that no longer loads -- leaves next_run
// null rather than failing the whole week's request: the rest of the page is
// still true.
func userScheduleJSON(s aireport.ResolvedSchedule, now time.Time) aiUserScheduleJSON {
	out := aiUserScheduleJSON{
		Enabled:       s.Enabled,
		EnabledSource: s.EnabledSource,
		Weekday:       int(s.Weekday),
		Hour:          s.Hour,
		Minute:        s.Minute,
		WhenSource:    s.WhenSource,
		TZ:            s.TZ,
	}
	if s.Enabled {
		if at, err := s.NextRun(now); err == nil {
			out.NextRun = &at
		}
	}
	return out
}

// handleSetAIUserSchedule saves this user's own override and answers with what
// now applies, so the screen shows the resolved value instead of working out
// how the change combined with the admin's default.
//
// The fields are pointers: a missing one must not decode to zero and silently
// move the firing to Sunday midnight. A nil clears that part, putting it back
// under the admin's default.
func (s *Server) handleSetAIUserSchedule(w http.ResponseWriter, r *http.Request) {
	svc, sc, ok := s.aiRequest(w, r)
	if !ok {
		return
	}
	var body struct {
		Enabled *bool  `json:"enabled"`
		Weekday *int   `json:"weekday"`
		Hour    *int   `json:"hour"`
		Minute  *int   `json:"minute"`
		TZ      string `json:"tz"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAIError(w, http.StatusBadRequest, "invalid_request", "요청 본문을 읽을 수 없습니다")
		return
	}
	saved, err := svc.SetUserSchedule(r.Context(), sc.DashboardUserID, body.Enabled, body.Weekday, body.Hour, body.Minute, body.TZ)
	if err != nil {
		if errors.Is(err, aireport.ErrInvalidSchedule) {
			writeAIError(w, http.StatusBadRequest, "invalid_schedule", "요일은 0~6, 시각은 00:00~23:59, 시간대는 IANA 이름이어야 합니다")
			return
		}
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedule": userScheduleJSON(saved, time.Now())})
}
