package api

import (
	"net/http"

	"cctrace/internal/auth"
)

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	var userID string
	if user, ok := auth.UserFromContext(r.Context()); ok && user.Role == "user" {
		_, _, userID = s.resolveUserAccessParams(user)
	}
	accounts, err := s.store.ListLoginAccounts(r.Context(), userID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, accounts)
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	result, err := s.store.ListUsers(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCostByUser(w http.ResponseWriter, r *http.Request) {
	since, until := parseTimeRange(r)
	profileEmail := r.URL.Query().Get("profile_email")
	loginEmail := r.URL.Query().Get("login_email")
	userID := r.URL.Query().Get("user_id")
	result, err := s.store.CostByUser(r.Context(), since, until, profileEmail, loginEmail, userID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCostByModel(w http.ResponseWriter, r *http.Request) {
	since, until := parseTimeRange(r)
	profileEmail := r.URL.Query().Get("profile_email")
	loginEmail := r.URL.Query().Get("login_email")
	userID := r.URL.Query().Get("user_id")
	result, err := s.store.CostByModel(r.Context(), since, until, profileEmail, loginEmail, userID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCostByTeam(w http.ResponseWriter, r *http.Request) {
	since, until := parseTimeRange(r)
	profileEmail := ""
	userID := ""
	result, err := s.store.CostByTeam(r.Context(), since, until, profileEmail, userID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
