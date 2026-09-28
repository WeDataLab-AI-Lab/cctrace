package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

type taskSegmentsReader interface {
	TaskSegmentsByType(ctx context.Context, since, until time.Time, taskType, profileEmail, userID string) (*store.TaskSegmentPage, error)
}

// handleTaskSegments serves the segments behind one Task types row in the
// weekly report modal -- caller-scoped, same as weekly-insights.
func (s *Server) handleTaskSegments(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	since, until, err := openAPITimeRange(r)
	if err != nil {
		writeOpenAPITimeRangeError(w, err)
		return
	}
	taskType := r.URL.Query().Get("task_type")
	if taskType == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "task_type is required"})
		return
	}
	profileEmail, _, userID := s.resolveUserAccessParams(user)
	reader, ok := s.store.(taskSegmentsReader)
	if !ok {
		writeOpenAPIInternalError(w, fmt.Errorf("task segments are not supported by this store"))
		return
	}
	page, err := reader.TaskSegmentsByType(r.Context(), since, until, taskType, profileEmail, userID)
	if err != nil {
		writeOpenAPIInternalError(w, err)
		return
	}
	// The ceiling rides with the rows. A list that stops without saying so is how
	// this drill-down came to show a different population than the card above it
	// (#669).
	writeJSON(w, http.StatusOK, map[string]any{
		"segments": page.Segments, "total": page.Total, "truncated": page.Truncated,
	})
}
