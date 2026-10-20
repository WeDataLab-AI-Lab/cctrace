package api

import (
	"errors"
	"net/http"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

func (s *Server) handleSessionTraceIndex(w http.ResponseWriter, r *http.Request) {
	if !s.sessionAnalysisEnabled {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session_analysis_disabled"})
		return
	}
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	email, _, uid := s.resolveUserAccessParams(user)
	access := store.SessionAnalysisAccess{User: user.Role == "user", DashboardUserID: user.ID}
	if s.useUserIDAccessControl {
		access.OwnerUserID = user.CctraceUserID
	} else {
		access.OwnerProfileEmail = user.Email
	}
	if access.User {
		access.ProfileEmail = email
		access.UserID = uid
	} else {
		access.ProfileEmail = r.URL.Query().Get("profile_email")
		access.LoginEmail = r.URL.Query().Get("login_email")
		access.UserID = r.URL.Query().Get("user_id")
	}
	st, ok := s.store.(store.SessionTraceIndexStore)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "trace_index_unavailable"})
		return
	}
	v, err := st.SessionTraceIndex(r.Context(), r.PathValue("session_id"), access)
	if errors.Is(err, store.ErrSessionAnalysisNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
