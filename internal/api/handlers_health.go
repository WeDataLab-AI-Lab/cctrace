package api

import (
	"net/http"

	"cctrace/internal/store"
)

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"version":                      s.version,
		"session_record_version_since": store.CctraceVersionSince,
		"client_version_header_since":  store.ClientVersionHeaderSince,
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := s.store.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"status": "unhealthy",
			"error":  err.Error(),
		})
		return
	}

	resp := map[string]interface{}{"status": "ok"}

	if t, err := s.store.LatestEventTime(ctx); err == nil && t != nil {
		resp["latest_event_at"] = t
	}

	if s.healthExtra != nil {
		extra, err := s.healthExtra(ctx)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
				"status": "unhealthy",
				"error":  err.Error(),
			})
			return
		}
		for k, v := range extra {
			resp[k] = v
		}
	}

	writeJSON(w, http.StatusOK, resp)
}
