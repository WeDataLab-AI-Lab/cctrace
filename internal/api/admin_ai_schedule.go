package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"cctrace/internal/aireport"
)

// adminAIScheduleJSON is the admin's default for automatic weekly runs, already
// resolved against the built-in one, with the layer each part came from so the
// screen can mark a value the admin has not set.
type adminAIScheduleJSON struct {
	Enabled       bool   `json:"enabled"`
	EnabledSource string `json:"enabled_source"`
	Weekday       int    `json:"weekday"`
	Hour          int    `json:"hour"`
	Minute        int    `json:"minute"`
	WhenSource    string `json:"when_source"`
	// TZ is the zone the weekday and time are read in for a user who has saved
	// none and has no report yet: the deployment's default. Empty is UTC.
	TZ string `json:"tz"`
}

func scheduleJSON(s aireport.ResolvedSchedule) adminAIScheduleJSON {
	return adminAIScheduleJSON{
		Enabled:       s.Enabled,
		EnabledSource: s.EnabledSource,
		Weekday:       int(s.Weekday),
		Hour:          s.Hour,
		Minute:        s.Minute,
		WhenSource:    s.WhenSource,
		TZ:            s.TZ,
	}
}

// handleSetAdminAISchedule saves the admin's default for automatic runs.
//
// The fields are pointers: a missing one must not decode to zero and silently
// move the firing to Sunday midnight. A nil clears that part and hands it back
// to the built-in default, as an empty model does for settings.
func (s *Server) handleSetAdminAISchedule(w http.ResponseWriter, r *http.Request) {
	user, ok := adminAISession(w, r, true)
	if !ok {
		return
	}
	if s.aiReports == nil {
		writeAIError(w, http.StatusServiceUnavailable, "runtime_unconfigured", "AI 런타임이 설정되지 않았습니다")
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
		Weekday *int  `json:"weekday"`
		Hour    *int  `json:"hour"`
		Minute  *int  `json:"minute"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAIError(w, http.StatusBadRequest, "invalid_request", "요청 본문을 읽을 수 없습니다")
		return
	}
	if err := s.aiReports.SetAutoSchedule(r.Context(), body.Enabled, body.Weekday, body.Hour, body.Minute, user.Email); err != nil {
		if errors.Is(err, aireport.ErrInvalidSchedule) {
			writeAIError(w, http.StatusBadRequest, "invalid_schedule", "요일은 0~6, 시각은 00:00~23:59 여야 합니다")
			return
		}
		writeErr(w, err)
		return
	}
	saved, err := s.aiReports.AutoSchedule(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedule": scheduleJSON(saved)})
}
