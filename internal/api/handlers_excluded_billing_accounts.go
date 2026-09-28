package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

// The billing-id counterpart of handlers_excluded_accounts.go, for accounts with
// no login email to exclude by -- see store.ExcludedBillingAccount for why that
// case exists. Admin-only for the same reason: excluding an account rewrites
// what every dashboard user sees.

// validBillingKey reports the normalized pair, or an error message naming what
// was wrong. The provider is lowercased and the account id is not: an account id
// is an opaque provider token, so two ids differing only in case are two ids.
func validBillingKey(provider, accountID string) (string, string, string) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	accountID = strings.TrimSpace(accountID)
	if provider == "" {
		return "", "", "billing_provider required"
	}
	if accountID == "" {
		return "", "", "account_id required"
	}
	return provider, accountID, ""
}

// handleListExcludedBillingAccounts reports the accounts hidden by billing id and
// how many quota readings and usage events each one hides, so an exclusion never
// becomes an unexplained gap.
func (s *Server) handleListExcludedBillingAccounts(w http.ResponseWriter, r *http.Request) {
	if _, ok := adminFromRequest(w, r); !ok {
		return
	}
	accounts, err := s.store.ListExcludedBillingAccounts(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, accounts)
}

func (s *Server) handleExcludeBillingAccount(w http.ResponseWriter, r *http.Request) {
	if !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return
	}
	user, ok := adminFromRequest(w, r)
	if !ok {
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

	hiddenSamples, hiddenEvents, err := s.store.ExcludeBillingAccount(r.Context(), provider, accountID, body.Reason, user.Email)
	if err != nil {
		// A failed commit does not say whether the change landed. Ingest
		// follows the table either way.
		s.reloadIngestBlocklist(r.Context())
		writeErr(w, err)
		return
	}
	if s.ingestBlock != nil {
		s.ingestBlock.AddExcludedAccount(provider, accountID)
	}
	s.reloadIngestBlocklist(r.Context())

	// The normalized key is logged because that is what the table stores and what
	// a later investigation will search for.
	log.Printf("[audit] action=exclude_billing_account actor=%s billing_provider=%s account_id=%s reason=%s hidden_samples=%d hidden_events=%d",
		user.Email, provider, accountID, body.Reason, hiddenSamples, hiddenEvents)
	writeJSON(w, http.StatusOK, map[string]any{"status": "excluded", "hidden_samples": hiddenSamples, "hidden_events": hiddenEvents})
}

func (s *Server) handleRemoveExcludedBillingAccount(w http.ResponseWriter, r *http.Request) {
	if !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return
	}
	user, ok := adminFromRequest(w, r)
	if !ok {
		return
	}

	provider, accountID, msg := validBillingKey(
		r.URL.Query().Get("billing_provider"), r.URL.Query().Get("account_id"))
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	if err := s.store.RemoveExcludedBillingAccount(r.Context(), provider, accountID); err != nil {
		// A failed commit does not say whether the change landed. Ingest
		// follows the table either way.
		s.reloadIngestBlocklist(r.Context())
		writeErr(w, err)
		return
	}
	if s.ingestBlock != nil {
		s.ingestBlock.RemoveExcludedAccount(provider, accountID)
	}
	s.reloadIngestBlocklist(r.Context())

	log.Printf("[audit] action=remove_excluded_billing_account actor=%s billing_provider=%s account_id=%s",
		user.Email, provider, accountID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}
