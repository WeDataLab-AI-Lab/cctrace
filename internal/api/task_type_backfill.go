package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"cctrace/internal/auth"
)

type taskTypeBackfiller interface {
	BackfillTaskTypes(ctx context.Context, limit int) (int, error)
}

// A rule change makes stored verdicts stale, and the two passes answer different
// questions -- one labels rows nobody has looked at, the other relabels rows
// looked at under older rules. They run from the same button because an operator
// asking to backfill task types wants the chart to be consistent afterwards, and
// leaving the second one to a separate request would mean a chart that mixes two
// rule sets until somebody remembers it exists (#429).
type taskTypeReclassifier interface {
	ReclassifyStaleTaskTypes(ctx context.Context) (int, error)
}

func (s *Server) handleTaskTypeBackfill(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || !requireAdmin(user, w) {
		return
	}
	limit := 1000
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 1000 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "limit must be between 1 and 1000"})
			return
		}
		limit = parsed
	}
	backfiller, ok := s.store.(taskTypeBackfiller)
	if !ok {
		writeOpenAPIInternalError(w, fmt.Errorf("task type backfill is not supported by this store"))
		return
	}
	updated, err := backfiller.BackfillTaskTypes(r.Context(), limit)
	if err != nil {
		writeOpenAPIInternalError(w, err)
		return
	}
	// Bounded per call like the backfill: an operator repeats the request until
	// both numbers come back zero.
	relabelled := 0
	if reclassifier, ok := s.store.(taskTypeReclassifier); ok {
		relabelled, err = reclassifier.ReclassifyStaleTaskTypes(r.Context())
		if err != nil {
			writeOpenAPIInternalError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"updated": updated, "relabelled": relabelled})
}
