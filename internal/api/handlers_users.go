package api

import (
	"encoding/json"
	"net/http"

	"cctrace/internal/auth"
)

func (s *Server) handleDeleteUserData(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	if !requireAdmin(user, w) {
		return
	}
	profileEmail := r.URL.Query().Get("profile_email")
	userID := r.URL.Query().Get("user_id")
	if err := s.store.DeleteUserData(r.Context(), profileEmail, userID); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleMergeUsers(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	if !requireAdmin(user, w) {
		return
	}
	var body struct {
		FromProfileEmail string `json:"from_profile_email"`
		FromUserID       string `json:"from_user_id"`
		ToProfileEmail   string `json:"to_profile_email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.store.MergeUsers(r.Context(), body.FromProfileEmail, body.FromUserID, body.ToProfileEmail); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "merged"})
}
