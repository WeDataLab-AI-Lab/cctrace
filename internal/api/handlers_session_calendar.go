package api

import (
	"errors"
	"net/http"
	"time"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

func (s *Server) handleSessionCalendar(w http.ResponseWriter, r *http.Request) {
	if !s.sessionAnalysisEnabled {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session_analysis_disabled"})
		return
	}
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	q := r.URL.Query()
	from, fromErr := time.Parse(time.RFC3339Nano, q.Get("from"))
	to, toErr := time.Parse(time.RFC3339Nano, q.Get("to"))
	if fromErr != nil || toErr != nil || !from.Before(to) || to.Sub(from) > 14*24*time.Hour {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid time range"})
		return
	}
	access := store.SessionAnalysisAccess{User: user.Role == "user", DashboardUserID: user.ID}
	email, _, uid := s.resolveUserAccessParams(user)
	if s.useUserIDAccessControl {
		access.OwnerUserID = user.CctraceUserID
	} else {
		access.OwnerProfileEmail = user.Email
	}
	personalID, personalEmail, personalUID := personalOverviewCaller(s, user)
	filter := store.SessionOverviewFilter{Agent: q.Get("agent"), Source: q.Get("source"),
		Classification: q.Get("classification"), DashboardUserID: personalID, PersonalEmail: personalEmail, PersonalUserID: personalUID,
		ProjectHashes: queryProjectHashes(r, "project_hashes"), FoldLineage: q.Get("assembled") == "1",
		IncludeSessionRecords: s.sessionRecordsEnabled}
	if filter.Classification != "" && !validOverviewClassification(filter.Classification) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid classification"})
		return
	}
	switch filter.Agent {
	case "", "claude", "codex", "other":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid agent"})
		return
	}
	if assembled := q.Get("assembled"); assembled != "" && assembled != "0" && assembled != "1" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid assembled"})
		return
	}
	if access.User {
		access.UserID, access.ProfileEmail = uid, email
		if uid != "" {
			filter.UserID = uid
		} else {
			filter.ProfileEmail = email
		}
	} else {
		filter.UserID, filter.ProfileEmail, filter.LoginEmail = q.Get("user_id"), q.Get("profile_email"), q.Get("login_email")
		if filter.UserID != "" && filter.ProfileEmail != "" || s.useUserIDAccessControl && filter.ProfileEmail != "" || !s.useUserIDAccessControl && filter.UserID != "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid calendar scope"})
			return
		}
		if filter.UserID == "" && filter.ProfileEmail == "" {
			filter.UserID, filter.ProfileEmail = uid, email
		}
		access.UserID, access.ProfileEmail, access.LoginEmail = filter.UserID, filter.ProfileEmail, filter.LoginEmail
	}
	if filter.Source != "" && filter.Source != "interactive" && filter.Source != "headless" && filter.Source != "all" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid source"})
		return
	}
	result, err := s.store.ReadSessionCalendar(r.Context(), store.SessionCalendarFilter{
		From: from, To: to, SessionOverviewFilter: filter, Access: access})
	if errors.Is(err, store.ErrSessionCalendarRange) || errors.Is(err, store.ErrPersonalInvalid) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid calendar scope"})
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
