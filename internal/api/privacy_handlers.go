package api

import (
	"encoding/json"
	"log"
	"net/http"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

func (s *Server) handleListPrivacy(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	settings, err := s.store.ListPrivacySettings(r.Context(), user.CctraceUserID, user.Email)
	if err != nil {
		writeErr(w, err)
		return
	}
	if settings == nil {
		settings = make([]*store.PrivacySetting, 0)
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) handleSetPrivacy(w http.ResponseWriter, r *http.Request) {
	if !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return
	}

	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var body struct {
		ScopeType  string `json:"scope_type"`
		ScopeValue string `json:"scope_value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if body.ScopeType != "session" && body.ScopeType != "project" && body.ScopeType != "user" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "scope_type must be session, project, or user"})
		return
	}

	if err := s.store.SetPrivacy(r.Context(), user.CctraceUserID, user.Email, body.ScopeType, body.ScopeValue); err != nil {
		writeErr(w, err)
		return
	}

	log.Printf("[audit] action=set_privacy actor=%s scope_type=%s scope_value=%s", user.Email, body.ScopeType, body.ScopeValue)
	writeJSON(w, http.StatusOK, map[string]string{"status": "created"})
}

func (s *Server) handleRemovePrivacy(w http.ResponseWriter, r *http.Request) {
	if !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return
	}

	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var body struct {
		ScopeType  string `json:"scope_type"`
		ScopeValue string `json:"scope_value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	if err := s.store.RemovePrivacy(r.Context(), user.CctraceUserID, user.Email, body.ScopeType, body.ScopeValue); err != nil {
		writeErr(w, err)
		return
	}

	log.Printf("[audit] action=remove_privacy actor=%s scope_type=%s scope_value=%s", user.Email, body.ScopeType, body.ScopeValue)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
