package api

import (
	"errors"
	"net/http"

	"cctrace/internal/auth"
	"cctrace/internal/sessionrecord"
	"cctrace/internal/store"
)

func (s *Server) handleSessionAnalysis(w http.ResponseWriter, r *http.Request) {
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
	access := store.SessionAnalysisAccess{User: user.Role == "user", DashboardUserID: user.ID,
		RepoStatusOnly: r.URL.Query().Get("view") == "repo-status"}
	if s.useUserIDAccessControl {
		access.OwnerUserID = user.CctraceUserID
	} else {
		access.OwnerProfileEmail = user.Email
	}
	if access.User {
		access.UserID = uid
		access.ProfileEmail = email
	} else {
		access.ProfileEmail = r.URL.Query().Get("profile_email")
		access.LoginEmail = r.URL.Query().Get("login_email")
		access.UserID = r.URL.Query().Get("user_id")
	}
	if s.sessionRecordsEnabled && !access.RepoStatusOnly {
		_, runtimeKey, runtimeAvailable, runtimeReason, settings, runtimeErr := s.currentSessionRecordRuntime(r.Context())
		access.RecordFeatureEnabled = true
		access.RecordRuntimeKey = runtimeKey
		access.RecordDisclosureVersion = sessionrecord.RecordDisclosureVersion
		access.RecordRuntimeAvailable = runtimeAvailable
		access.RecordRuntimeReason = runtimeReason
		if s.sessionRecordsWorker != nil {
			access.RecordTimezone = s.sessionRecordsWorker.DefaultTZ
		}
		if runtimeErr == nil && settings != nil {
			access.RecordAdminEnabled = settings.Enabled
		}
	}
	result, err := s.store.ReadSessionAnalysis(r.Context(), r.PathValue("session_id"), access)
	if errors.Is(err, store.ErrSessionAnalysisNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if r.URL.Query().Get("view") == "repo-status" {
		var repoStatus interface{}
		if result["owner_view"] == true {
			if header, ok := result["header"].(map[string]interface{}); ok {
				repoStatus = header["repo_status"]
			}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"session_id":     result["session_id"],
			"owner_view":     result["owner_view"],
			"analysis_state": result["analysis_state"],
			"repo_status":    repoStatus,
		})
		return
	}
	if !s.sessionRecordsEnabled {
		delete(result, "record")
		delete(result, "judgement")
	}
	writeJSON(w, http.StatusOK, result)
}
