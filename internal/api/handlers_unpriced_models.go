package api

import (
	"encoding/json"
	"log"
	"net/http"

	"cctrace/internal/store"
)

// Unpriced models (#441): usage no rate matched, which the cost figures count as
// $0. Admin-only: the list is aggregates by model, but deciding what gets a price
// and what is free by design is an admin call.

// flatRateKey reports the normalized pair, or an error message naming what was
// missing. A blank model is refused even though the unpriced list can show one:
// an empty model is a collection gap to fix, not a model to declare free.
func flatRateKey(agent, model string) (string, string, string) {
	agent, model = store.NormalizeFlatRateKey(agent, model)
	if agent == "" {
		return "", "", "agent required"
	}
	if model == "" {
		return "", "", "model required"
	}
	return agent, model, ""
}

func (s *Server) handleListUnpricedModels(w http.ResponseWriter, r *http.Request) {
	if _, ok := adminFromRequest(w, r); !ok {
		return
	}
	unpriced, err := s.store.ListUnpricedModels(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	flatRate, err := s.store.ListFlatRateModels(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"unpriced": unpriced, "flat_rate": flatRate})
}

func (s *Server) handleMarkFlatRateModel(w http.ResponseWriter, r *http.Request) {
	if !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return
	}
	user, ok := adminFromRequest(w, r)
	if !ok {
		return
	}
	var body struct {
		Agent  string `json:"agent"`
		Model  string `json:"model"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	agent, model, msg := flatRateKey(body.Agent, body.Model)
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	if err := s.store.MarkFlatRateModel(r.Context(), agent, model, body.Reason, user.Email); err != nil {
		writeErr(w, err)
		return
	}
	log.Printf("[audit] action=mark_flat_rate_model actor=%s agent=%s model=%s reason=%s", user.Email, agent, model, body.Reason)
	writeJSON(w, http.StatusOK, map[string]string{"status": "marked"})
}

func (s *Server) handleUnmarkFlatRateModel(w http.ResponseWriter, r *http.Request) {
	if !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return
	}
	user, ok := adminFromRequest(w, r)
	if !ok {
		return
	}
	agent, model, msg := flatRateKey(r.URL.Query().Get("agent"), r.URL.Query().Get("model"))
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	if err := s.store.UnmarkFlatRateModel(r.Context(), agent, model); err != nil {
		writeErr(w, err)
		return
	}
	log.Printf("[audit] action=unmark_flat_rate_model actor=%s agent=%s model=%s", user.Email, agent, model)
	writeJSON(w, http.StatusOK, map[string]string{"status": "unmarked"})
}
