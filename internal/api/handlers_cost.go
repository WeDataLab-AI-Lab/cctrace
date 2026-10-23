package api

import (
	"net/http"

	"cctrace/internal/auth"
)

// handleAccounts lists every non-excluded account for every role by default. The
// chips it feeds sit above shared screens whose handlers apply login_email to all
// members' rows. scope=mine is a role=user display choice that trims the list to
// the accounts in the caller's own rows; an admin's list is never trimmed. In the legacy
// profile_email mode (CCTRACE_USERID_ACCESS_CONTROL=false) there is no user_id to trim by,
// so mine lists every account there.
func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	var userID string
	if user, ok := auth.UserFromContext(r.Context()); ok && user.Role == "user" && r.URL.Query().Get("scope") == "mine" {
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
