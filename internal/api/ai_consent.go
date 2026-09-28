package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"cctrace/internal/aireport"
	"cctrace/internal/airuntime"
)

type aiConsentInfoJSON struct {
	RuntimeKey        string     `json:"runtime_key"`
	DisclosureVersion string     `json:"disclosure_version"`
	Granted           bool       `json:"granted"`
	GrantedAt         *time.Time `json:"granted_at"`
	Sends             []string   `json:"sends"`
	NotSends          []string   `json:"not_sends"`
	Runtime           string     `json:"runtime"`
	Provider          string     `json:"provider"`
	ProviderRetention string     `json:"provider_retention"`
}

// handleGetAIConsent serves the caller's consent and the disclosure text. The
// text lives on the server so a version bump changes both together.
func (s *Server) handleGetAIConsent(w http.ResponseWriter, r *http.Request) {
	svc, sc, ok := s.aiRequest(w, r)
	if !ok {
		return
	}
	cs, err := svc.Consent(r.Context(), sc)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, aiConsentInfoJSON{
		RuntimeKey: cs.RuntimeKey, DisclosureVersion: cs.DisclosureVersion, Granted: cs.Granted, GrantedAt: cs.GrantedAt,
		Sends: aireport.ConsentSends, NotSends: aireport.ConsentNotSends, Runtime: cs.Runtime, Provider: aireport.ProviderLabels[cs.Runtime], ProviderRetention: aireport.RetentionFor(cs.Runtime),
	})
}

// handlePostAIConsent records consent for the runtime key and disclosure version
// the screen showed; 409 when either is no longer current.
func (s *Server) handlePostAIConsent(w http.ResponseWriter, r *http.Request) {
	svc, sc, ok := s.aiRequest(w, r)
	if !ok {
		return
	}
	var body struct {
		RuntimeKey        string `json:"runtime_key"`
		DisclosureVersion string `json:"disclosure_version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAIError(w, http.StatusBadRequest, "invalid_request", "요청 본문을 읽을 수 없습니다")
		return
	}
	switch err := svc.GrantConsent(r.Context(), sc, body.RuntimeKey, body.DisclosureVersion); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, aireport.ErrRuntimeDisabled):
		writeAIDisabled(w)
	case errors.Is(err, airuntime.ErrNotConfigured):
		writeAIError(w, http.StatusServiceUnavailable, "runtime_unconfigured", "AI 런타임이 설정되지 않았습니다")
	case errors.Is(err, aireport.ErrConsentMismatch):
		writeAIError(w, http.StatusConflict, "consent_mismatch", "동의 문구가 바뀌었습니다. 다시 확인해 주세요")
	default:
		writeErr(w, err)
	}
}
