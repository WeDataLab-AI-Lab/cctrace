package api

import (
	"encoding/json"
	"net/http"

	"cctrace/internal/store"
)

func (s *Server) handleQuotaWrite(w http.ResponseWriter, r *http.Request) {
	var snap store.QuotaSnapshot
	if err := json.NewDecoder(r.Body).Decode(&snap); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if snap.ProfileEmail == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "profile_email required"})
		return
	}
	if err := s.store.UpsertQuotaSnapshot(r.Context(), &snap); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleQuotaRead(w http.ResponseWriter, r *http.Request) {
	profileEmail := r.URL.Query().Get("profile_email")
	if profileEmail != "" {
		snap, err := s.store.GetQuotaSnapshot(r.Context(), profileEmail)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		writeJSON(w, http.StatusOK, snap)
		return
	}
	snaps, err := s.store.ListQuotaSnapshots(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snaps)
}
