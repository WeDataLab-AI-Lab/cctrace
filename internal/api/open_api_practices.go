package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"cctrace/internal/store"
)

type practicesReader interface {
	Practices(ctx context.Context, since, until time.Time, selfUserID string) (*store.Practices, error)
}

// handleOpenAPIPractices serves one shared aggregate to every caller, admins
// included; only self follows the caller. The window is always the last 30 days
// ending now: there is no since/until to choose.
func (s *Server) handleOpenAPIPractices(w http.ResponseWriter, r *http.Request) {
	user, ok := openAPIUser(w, r)
	if !ok {
		return
	}
	reader, ok := s.store.(practicesReader)
	if !ok {
		writeOpenAPIInternalError(w, fmt.Errorf("practices are not supported by this store"))
		return
	}
	until := time.Now().UTC()
	practices, err := reader.Practices(r.Context(), until.AddDate(0, 0, -30), until, user.CctraceUserID)
	if err != nil {
		writeOpenAPIInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, practices)
}
