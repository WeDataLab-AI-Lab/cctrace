package localapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"cctrace/internal/profile"
)

const maxProfileSelectionBody = 8 << 10

func validLocalProfileIdentity(entry ProfileIdentity) bool {
	if entry.Agent != "claude" && entry.Agent != "codex" || entry.ProfileName == "" ||
		entry.ProfileName != "default" && profile.ValidateName(entry.ProfileName) != nil ||
		entry.BillingProvider != "anthropic" && entry.BillingProvider != "openai" ||
		entry.Agent == "claude" && entry.BillingProvider != "anthropic" ||
		entry.Agent == "codex" && entry.BillingProvider != "openai" ||
		entry.AccountID == "" || len(entry.AccountID) > 256 || !utf8.ValidString(entry.AccountID) ||
		strings.TrimSpace(entry.AccountID) != entry.AccountID || strings.ContainsAny(entry.AccountID, "\x00\r\n@/\\") {
		return false
	}
	return true
}

type profileSelectionRequest struct {
	Agent            string  `json:"agent"`
	ProfileName      string  `json:"profile_name"`
	BillingProvider  string  `json:"billing_provider"`
	AccountID        string  `json:"account_id"`
	ExpectedRevision *uint64 `json:"expected_revision"`
}

func (s *Server) profileCatalog(w http.ResponseWriter) {
	if s.opts.SelectionPath == "" {
		fail(w, http.StatusServiceUnavailable, "unavailable", "local profile selection is not configured")
		return
	}
	selection, err := profile.ReadSelection(s.opts.SelectionPath)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "unavailable", "local profile selection cannot be read")
		return
	}
	profiles := make([]ProfileIdentity, len(s.opts.Profiles))
	for i, entry := range s.opts.Profiles {
		profiles[i] = entry
		selected := selection.Claude
		if entry.Agent == "codex" {
			selected = selection.Codex
		}
		profiles[i].Selected = selected == entry.ProfileName || selected == "" && entry.ProfileName == "default"
	}
	send(w, ProfileCatalogResponse{Revision: selection.Revision, Profiles: profiles})
}

func (s *Server) selectLocalProfile(w http.ResponseWriter, r *http.Request) {
	if s.opts.SelectionPath == "" {
		fail(w, http.StatusServiceUnavailable, "unavailable", "local profile selection is not configured")
		return
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		fail(w, http.StatusUnsupportedMediaType, "invalid_request", "application/json required")
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxProfileSelectionBody))
	decoder.DisallowUnknownFields()
	var request profileSelectionRequest
	if err := decoder.Decode(&request); err != nil {
		fail(w, http.StatusBadRequest, "invalid_request", "invalid profile selection")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		fail(w, http.StatusBadRequest, "invalid_request", "invalid profile selection")
		return
	}
	if request.ExpectedRevision == nil || request.Agent != "claude" && request.Agent != "codex" ||
		request.ProfileName == "" || request.ProfileName != "default" && profile.ValidateName(request.ProfileName) != nil ||
		(request.Agent == "claude" && request.BillingProvider != "anthropic") ||
		(request.Agent == "codex" && request.BillingProvider != "openai") {
		fail(w, http.StatusBadRequest, "invalid_request", "invalid profile selection")
		return
	}
	matched := false
	for _, entry := range s.opts.Profiles {
		if entry.Agent == request.Agent && entry.ProfileName == request.ProfileName &&
			entry.BillingProvider == request.BillingProvider && entry.AccountID != "" && entry.AccountID == request.AccountID {
			matched = true
			break
		}
	}
	if !matched {
		fail(w, http.StatusForbidden, "profile_account_mismatch", "profile does not match local account metadata")
		return
	}
	selection, err := profile.UpdateSelection(s.opts.SelectionPath, request.Agent, request.ProfileName, request.ExpectedRevision)
	if errors.Is(err, profile.ErrSelectionConflict) {
		fail(w, http.StatusConflict, "revision_conflict", "profile selection changed")
		return
	}
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid_request", "profile selection could not be saved")
		return
	}
	w.WriteHeader(http.StatusOK)
	send(w, selection)
}
