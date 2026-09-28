package api

import (
	"net/http"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	// Parsed exactly as handleCountSessionOverview parses them, so the picker and the
	// list it drives cannot disagree about which projects exist (#352).
	f := store.ProjectFilter{
		LoginEmail:  r.URL.Query().Get("login_email"),
		Source:      r.URL.Query().Get("source"),
		Agent:       r.URL.Query().Get("agent"),
		FoldLineage: r.URL.Query().Get("assembled") == "1",
	}
	// A restricted user's own scope overwrites what the query asked for, so a filter
	// can only narrow it, never widen it.
	if user, ok := auth.UserFromContext(r.Context()); ok && user.Role == "user" {
		f.ProfileEmail, f.LoginEmail, f.UserID = s.resolveUserAccessParams(user)
	}
	projects, err := s.store.ListProjects(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projects)
}
