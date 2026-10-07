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
	// role=user: the scope is pinned to the caller's user_id and the query's
	// login_email is discarded, so an account filter can neither widen nor narrow it (#811).
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
