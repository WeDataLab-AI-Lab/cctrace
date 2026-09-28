package api

import (
	"net/http"

	"cctrace/internal/store"
)

func (s *Server) handleToolUsage(w http.ResponseWriter, r *http.Request) {
	since, until := parseTimeRange(r)
	profileEmail := r.URL.Query().Get("profile_email")
	loginEmail := r.URL.Query().Get("login_email")
	userID := r.URL.Query().Get("user_id")
	result, err := s.store.ToolUsage(r.Context(), since, until, profileEmail, loginEmail, userID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleToolDetail(w http.ResponseWriter, r *http.Request) {
	toolName := r.URL.Query().Get("tool_name")
	if toolName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tool_name required"})
		return
	}
	since, until := parseTimeRange(r)
	profileEmail := r.URL.Query().Get("profile_email")
	loginEmail := r.URL.Query().Get("login_email")
	userID := r.URL.Query().Get("user_id")
	granularity := r.URL.Query().Get("granularity")
	if granularity == "" {
		granularity = "day"
	}
	// Same validation as the stats handlers: an unvalidated tz is interpolated
	// into SQL, and an absent one falls back to UTC in the store.
	tz := ""
	if v := r.URL.Query().Get("tz"); v != "" && tzRegexp.MatchString(v) {
		tz = v
	}
	limit := queryInt(r, "limit", 50)
	ctx := r.Context()

	var timeseries []*store.ToolTimeBucket
	var failures []*store.ToolFailure
	var tsErr, fErr error

	done := make(chan struct{}, 2)
	go func() {
		timeseries, tsErr = s.store.ToolTimeSeries(ctx, toolName, since, until, profileEmail, loginEmail, userID, granularity, tz)
		done <- struct{}{}
	}()
	go func() {
		failures, fErr = s.store.ToolFailures(ctx, toolName, since, until, profileEmail, loginEmail, userID, limit)
		done <- struct{}{}
	}()
	<-done
	<-done

	if tsErr != nil {
		writeErr(w, tsErr)
		return
	}
	if fErr != nil {
		writeErr(w, fErr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"timeseries": timeseries,
		"failures":   stripToolFailureAttrs(failures),
	})
}

func (s *Server) handlePluginUsage(w http.ResponseWriter, r *http.Request) {
	since, until := parseTimeRange(r)
	profileEmail := r.URL.Query().Get("profile_email")
	loginEmail := r.URL.Query().Get("login_email")
	userID := r.URL.Query().Get("user_id")
	agent := r.URL.Query().Get("agent")

	result, err := s.store.PluginUsage(r.Context(), since, until, profileEmail, loginEmail, userID, agent)
	if err != nil {
		writeErr(w, err)
		return
	}
	if result == nil {
		result = []*store.PluginUsageSummary{}
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleSkillUsage(w http.ResponseWriter, r *http.Request) {
	since, until := parseTimeRange(r)
	profileEmail := r.URL.Query().Get("profile_email")
	loginEmail := r.URL.Query().Get("login_email")
	userID := r.URL.Query().Get("user_id")
	agent := r.URL.Query().Get("agent")
	if agent == "" {
		agent = "codex"
	}

	result, err := s.store.SkillUsage(r.Context(), since, until, profileEmail, loginEmail, userID, agent)
	if err != nil {
		writeErr(w, err)
		return
	}
	if result == nil {
		result = []*store.SkillUsageSummary{}
	}
	writeJSON(w, http.StatusOK, result)
}
