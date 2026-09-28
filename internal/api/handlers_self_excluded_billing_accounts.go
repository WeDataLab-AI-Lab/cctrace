package api

import (
	"encoding/json"
	"log"
	"net/http"
	"unicode/utf8"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

// Self exclusion (#716): a user stops a billing account of their own -- a
// personal subscription used on the same machine -- from being shown or stored,
// without asking an admin. The entry lands in excluded_billing_accounts, so it
// hides and is refused at ingest exactly as an admin's does.
//
// What a user may pick is limited to accounts their own data was billed to, and
// not an account billing several people: either would let one user hide someone
// else's data. They may take back only what they registered themselves.

// A change rebuilds the excluded-session set and the usage rollups for everyone,
// so a user gets a handful at once and one a minute after that. Plenty for
// excluding the accounts they have; not enough to keep the server rebuilding.
const (
	selfExclusionRPS   = 1.0 / 60
	selfExclusionBurst = 5
)

// maxSelfExclusionReason bounds the free-text reason a non-admin writes into
// the table and the audit log.
const maxSelfExclusionReason = 200

const sharedAccountMsg = "billing account is shared with other people; ask an admin"

func (s *Server) observedBillingAccounts(r *http.Request, user *auth.DashboardUser) ([]store.ObservedBillingAccount, error) {
	profileEmail, _, userID := s.resolveUserAccessParams(user)
	return s.store.ListObservedBillingAccounts(r.Context(), profileEmail, userID, user.Email)
}

func (s *Server) handleListObservedBillingAccounts(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	accounts, err := s.observedBillingAccounts(r, user)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeExclusionList(s, w, r, accounts)
}

func (s *Server) handleSelfExcludeBillingAccount(w http.ResponseWriter, r *http.Request) {
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
		BillingProvider string `json:"billing_provider"`
		AccountID       string `json:"account_id"`
		Reason          string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	provider, accountID, msg := validBillingKey(body.BillingProvider, body.AccountID)
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	if utf8.RuneCountInString(body.Reason) > maxSelfExclusionReason {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "reason too long"})
		return
	}

	accounts, err := s.observedBillingAccounts(r, user)
	if err != nil {
		writeErr(w, err)
		return
	}
	var found *store.ObservedBillingAccount
	for i := range accounts {
		if accounts[i].BillingProvider == provider && accounts[i].AccountID == accountID {
			found = &accounts[i]
			break
		}
	}
	switch {
	case found == nil:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "billing account not seen in your data"})
		return
	case found.Shared:
		writeJSON(w, http.StatusForbidden, map[string]string{"error": sharedAccountMsg})
		return
	case found.Excluded && found.SelfRegistered:
		// A retry of the caller's own exclusion: a POST that outlived the server's
		// write timeout is resent by the browser and lands here. Nothing changes.
		writeJSON(w, http.StatusOK, map[string]string{"status": "excluded"})
		return
	case found.Excluded:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "billing account is already excluded"})
		return
	}
	profileEmail, _, userID := s.resolveUserAccessParams(user)
	usedByOthers, err := s.store.BillingAccountUsedByOthers(r.Context(), provider, accountID, profileEmail, userID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if usedByOthers {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": sharedAccountMsg})
		return
	}

	if err := s.store.SelfExcludeBillingAccount(r.Context(), provider, accountID, body.Reason, user.Email); err != nil {
		// A failed commit does not say whether the change landed. Ingest
		// follows the table either way.
		s.reloadIngestBlocklist(r.Context())
		writeErr(w, err)
		return
	}
	s.reloadIngestBlocklist(r.Context())

	log.Printf("[audit] action=self_exclude_billing_account actor=%s billing_provider=%s account_id=%s reason=%q",
		user.Email, provider, accountID, body.Reason)
	writeJSON(w, http.StatusOK, map[string]string{"status": "excluded"})
}

func (s *Server) handleRemoveSelfExcludedBillingAccount(w http.ResponseWriter, r *http.Request) {
	if !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return
	}
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	provider, accountID, msg := validBillingKey(
		r.URL.Query().Get("billing_provider"), r.URL.Query().Get("account_id"))
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	removed, err := s.store.RemoveSelfExcludedBillingAccount(r.Context(), provider, accountID, user.Email)
	if err != nil {
		// A failed commit does not say whether the change landed. Ingest
		// follows the table either way.
		s.reloadIngestBlocklist(r.Context())
		writeErr(w, err)
		return
	}
	if !removed {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only exclusions you registered yourself can be removed"})
		return
	}
	if s.ingestBlock != nil {
		s.ingestBlock.RemoveExcludedAccount(provider, accountID)
	}
	s.reloadIngestBlocklist(r.Context())

	log.Printf("[audit] action=remove_self_excluded_billing_account actor=%s billing_provider=%s account_id=%s",
		user.Email, provider, accountID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}
