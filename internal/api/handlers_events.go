package api

import (
	"net/http"

	"cctrace/internal/store"
)

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	f := store.EventFilter{
		SessionID:    r.URL.Query().Get("session_id"),
		ProfileEmail: r.URL.Query().Get("profile_email"),
		LoginEmail:   r.URL.Query().Get("login_email"),
		UserTeam:     r.URL.Query().Get("user_team"),
		EventName:    r.URL.Query().Get("event_name"),
		Limit:        queryInt(r, "limit", 100),
		Offset:       queryInt(r, "offset", 0),
	}
	f.Since, f.Until = lenientQueryTimeRange(r)
	events, err := s.store.ListEvents(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stripTextBearingAttrs(events))
}

func (s *Server) handleCountEvents(w http.ResponseWriter, r *http.Request) {
	f := store.EventFilter{
		SessionID:    r.URL.Query().Get("session_id"),
		ProfileEmail: r.URL.Query().Get("profile_email"),
		LoginEmail:   r.URL.Query().Get("login_email"),
		UserTeam:     r.URL.Query().Get("user_team"),
		Agent:        r.URL.Query().Get("agent"),
		EventName:    r.URL.Query().Get("event_name"),
	}
	f.Since, f.Until = lenientQueryTimeRange(r)
	count, err := s.store.CountEvents(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"count": count})
}
