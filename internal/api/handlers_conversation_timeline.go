package api

import (
	"net/http"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

// handleConversationTimeline serves the body-free shape of a whole session for
// the conversation axis. Scope follows handleListSessions exactly.
func (s *Server) handleConversationTimeline(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	st, ok := s.store.(store.ConversationTimelineStore)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "timeline_unavailable"})
		return
	}
	f := store.SessionRecordFilter{SessionID: r.PathValue("session_id"), ProfileEmail: r.URL.Query().Get("profile_email")}
	switch user.Role {
	case "user":
		f.ProfileEmail = ""
		s.applyUserSessionFilter(&f, user, "handleConversationTimeline")
	case "admin":
		f.UserID = r.URL.Query().Get("user_id")
		f.LoginEmail = r.URL.Query().Get("login_email")
	}
	v, err := st.ConversationTimeline(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeLargeJSON(w, r, http.StatusOK, v)
}
