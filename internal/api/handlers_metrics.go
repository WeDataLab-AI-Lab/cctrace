package api

import (
	"net/http"

	"cctrace/internal/store"
)

func (s *Server) handleListMetrics(w http.ResponseWriter, r *http.Request) {
	f := store.MetricFilter{
		MetricName:   r.URL.Query().Get("metric_name"),
		ProfileEmail: r.URL.Query().Get("profile_email"),
		UserTeam:     r.URL.Query().Get("user_team"),
		Agent:        r.URL.Query().Get("agent"),
		Model:        r.URL.Query().Get("model"),
		Limit:        queryInt(r, "limit", 100),
		Offset:       queryInt(r, "offset", 0),
	}
	f.Since, f.Until = lenientQueryTimeRange(r)
	metrics, err := s.store.ListMetrics(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, metrics)
}
