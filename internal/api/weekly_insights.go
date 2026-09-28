package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

type weeklyInsightsReader interface {
	WeeklyInsights(context.Context, time.Time, time.Time, string, string, string) (*store.WeeklyInsights, error)
}

func (s *Server) handleOpenAPIWeeklyInsights(w http.ResponseWriter, r *http.Request) {
	user, ok := openAPIUser(w, r)
	if !ok {
		return
	}
	s.handleWeeklyInsights(w, r, user)
}

// handleDashboardWeeklyInsights serves the same caller-scoped report through
// dashboard session authentication, so the report page never needs an API token.
func (s *Server) handleDashboardWeeklyInsights(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	s.handleWeeklyInsights(w, r, user)
}

func (s *Server) handleWeeklyInsights(w http.ResponseWriter, r *http.Request, user *auth.DashboardUser) {
	since, until, err := openAPITimeRange(r)
	if err != nil {
		writeOpenAPITimeRangeError(w, err)
		return
	}
	tz := ""
	if v := r.URL.Query().Get("tz"); v != "" && tzRegexp.MatchString(v) {
		tz = v
	}
	profileEmail, _, userID := s.resolveUserAccessParams(user)
	reader, ok := s.store.(weeklyInsightsReader)
	if !ok {
		writeOpenAPIInternalError(w, fmt.Errorf("weekly insights are not supported by this store"))
		return
	}
	insights, err := reader.WeeklyInsights(r.Context(), since, until, profileEmail, userID, tz)
	if err != nil {
		writeOpenAPIInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, insights)
}
