package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

// Excluding an account rewrites what every dashboard user sees and reveals the
// hidden spend, so all three endpoints are admin-only. dashMW only proves the
// caller is signed in; adminFromRequest is what checks the role.

// handleListExcludedAccounts reports the accounts hidden from the dashboard and
// what each one hides, so an exclusion never becomes an unexplained gap in
// company-wide totals.
func (s *Server) handleListExcludedAccounts(w http.ResponseWriter, r *http.Request) {
	if _, ok := adminFromRequest(w, r); !ok {
		return
	}
	accounts, err := s.store.ListExcludedAccounts(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeExclusionList(s, w, r, accounts)
}

// writeExclusionList answers an exclusion list together with whether the usage
// rebuild an exclusion change queues is still pending. The change answers before
// the charts follow, so the screen that made it has to be able to say they have
// not caught up yet. On the response, not an item: after the last exclusion is
// removed the list is empty and the rebuild is still owed.
//
// The flag is only a note beside the list, so failing to read it keeps the list.
// It leaves the flag out rather than answering false: the screen reads false as
// "the rebuild finished" -- it refetches the charts, stops polling and hides the
// note -- and a missing flag as "not known", keeping what it last saw.
func writeExclusionList[T any](s *Server, w http.ResponseWriter, r *http.Request, accounts []T) {
	if accounts == nil {
		accounts = []T{}
	}
	body := map[string]any{"accounts": accounts}
	pending, err := s.store.UsageRollupRebuildPending(r.Context())
	if err != nil {
		log.Printf("[exclusions] usage rebuild pending: %v", err)
	} else {
		body["usage_rebuild_pending"] = pending
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleExcludeAccount(w http.ResponseWriter, r *http.Request) {
	if !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return
	}
	user, ok := adminFromRequest(w, r)
	if !ok {
		return
	}

	var body struct {
		LoginEmail string `json:"login_email"`
		Reason     string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	loginEmail := strings.ToLower(strings.TrimSpace(body.LoginEmail))
	if loginEmail == "" || !strings.Contains(loginEmail, "@") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "login_email must be an email address"})
		return
	}

	affected, err := s.store.ExcludeAccount(r.Context(), loginEmail, body.Reason, user.Email)
	if err != nil {
		// A failed commit does not say whether the change landed. Ingest
		// follows the table either way.
		s.reloadIngestBlocklist(r.Context())
		writeErr(w, err)
		return
	}
	// The address itself is known here; its linked billing accounts only after
	// the reload. Apply what is known first so a failed reload still refuses it.
	if s.ingestBlock != nil {
		s.ingestBlock.AddExcludedEmail(loginEmail)
	}
	s.reloadIngestBlocklist(r.Context())

	// Log the normalized key — that is what the table stores and what a later
	// investigation will search for.
	log.Printf("[audit] action=exclude_account actor=%s login_email=%s reason=%s hidden_events=%d",
		user.Email, loginEmail, body.Reason, affected)
	writeJSON(w, http.StatusOK, map[string]any{"status": "excluded", "hidden_events": affected})
}

func (s *Server) handleRemoveExcludedAccount(w http.ResponseWriter, r *http.Request) {
	if !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return
	}
	user, ok := adminFromRequest(w, r)
	if !ok {
		return
	}

	loginEmail := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("login_email")))
	if loginEmail == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "login_email required"})
		return
	}
	if err := s.store.RemoveExcludedAccount(r.Context(), loginEmail); err != nil {
		// A failed commit does not say whether the change landed. Ingest
		// follows the table either way.
		s.reloadIngestBlocklist(r.Context())
		writeErr(w, err)
		return
	}
	// Forget the address before reloading: if the reload fails, a stale
	// "excluded" would keep dropping live data until the next tick.
	if s.ingestBlock != nil {
		s.ingestBlock.RemoveExcludedEmail(loginEmail)
	}
	s.reloadIngestBlocklist(r.Context())

	log.Printf("[audit] action=remove_excluded_account actor=%s login_email=%s", user.Email, loginEmail)
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}
