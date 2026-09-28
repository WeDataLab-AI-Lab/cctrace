package api

import (
	"net/http"

	"cctrace/internal/auth"
)

// requireTokenPasswordChanged protects API token creation, rotation and updates.
// Login, password changes and revocation stay available. Read the current DB
// state: a JWT may predate an administrator's password reset or the user's change.
func (s *Server) requireTokenPasswordChanged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.UserFromContext(r.Context())
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		current, err := s.store.GetDashboardUserByID(r.Context(), user.ID)
		if err != nil {
			writeErr(w, err)
			return
		}
		if current == nil || !current.IsActive {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "account is disabled"})
			return
		}
		if current.MustChangePassword {
			writeJSON(w, http.StatusForbidden, map[string]interface{}{
				"code":                 "password_change_required",
				"error":                "Please change your password on the dashboard before managing API tokens.",
				"must_change_password": true,
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}
