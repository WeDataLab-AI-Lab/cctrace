package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"cctrace/internal/store"
)

// retentionOpTimeout bounds the drop-eligible row count, which can scan old
// chunks of a large hypertable.
const retentionOpTimeout = 15 * time.Second

// axisToConfig maps a UI/API axis + days to a RetentionConfig that touches only
// that axis. days must be >= 0 (0 = permanent/remove policy).
func axisToConfig(axis string, days int) (store.RetentionConfig, error) {
	if days < 0 {
		return store.RetentionConfig{}, fmt.Errorf("days must be >= 0")
	}
	switch axis {
	case "otel":
		return store.RetentionConfig{OtelDays: &days}, nil
	case "session":
		return store.RetentionConfig{SessionDays: &days}, nil
	default:
		return store.RetentionConfig{}, fmt.Errorf("unknown axis %q", axis)
	}
}

// handleRetentionPreview (GET /api/admin/retention/preview?axis=&days=) is an
// admin-only DRY RUN: it reports how many rows a new N-day retention policy would
// make drop-eligible, without changing anything.
func (s *Server) handleRetentionPreview(w http.ResponseWriter, r *http.Request) {
	if _, ok := adminFromRequest(w, r); !ok {
		return
	}
	axis := r.URL.Query().Get("axis")
	days, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil || days < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid days"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), retentionOpTimeout)
	defer cancel()
	preview, err := s.store.RetentionPreviewAxis(ctx, axis, days)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

type setRetentionRequest struct {
	Axis      string `json:"axis"`
	Days      int    `json:"days"`
	Confirmed bool   `json:"confirmed"`
}

// handleSetRetention (PATCH /api/admin/retention) applies a retention change for
// an axis. It is guarded: CSRF + admin, and any change that would drop data
// requires an explicit confirmed=true (a dry-run preview is returned otherwise),
// so a stray API call can never delete rows. The change is applied via the
// tx-atomic ReconcileRetention; actual drops run later in the async job.
func (s *Server) handleSetRetention(w http.ResponseWriter, r *http.Request) {
	if !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return
	}
	user, ok := adminFromRequest(w, r)
	if !ok {
		return
	}

	var body setRetentionRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	cfg, err := axisToConfig(body.Axis, body.Days)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// An env-pinned axis is authoritative: the boot reconcile re-asserts the env
	// value, so any edit here would be reverted (and could drop data below the env
	// value in the meantime). Refuse it server-side, not just in the UI.
	if axisEnvManaged()[body.Axis] {
		log.Printf("[audit] action=set_retention_rejected actor=%s axis=%s days=%d reason=env_managed",
			user.Email, body.Axis, body.Days)
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "axis retention is managed by an environment variable; change the env var instead",
		})
		return
	}

	// Danger guard: compute drop-eligible rows on its OWN budget (the count scans
	// old chunks) so it never eats into the apply's budget below.
	previewCtx, previewCancel := context.WithTimeout(r.Context(), retentionOpTimeout)
	preview, err := s.store.RetentionPreviewAxis(previewCtx, body.Axis, body.Days)
	previewCancel()
	if err != nil {
		log.Printf("[audit] action=set_retention_error actor=%s axis=%s days=%d err=%v",
			user.Email, body.Axis, body.Days, err)
		writeErr(w, err)
		return
	}
	var totalDrop int64
	for _, p := range preview {
		totalDrop += p.RowsToDrop
	}
	if totalDrop > 0 && !body.Confirmed {
		// Record the blocked attempt too — an unconfirmed destructive request
		// against an irreversible-deletion endpoint should leave an audit trail.
		log.Printf("[audit] action=set_retention_blocked actor=%s axis=%s days=%d eligible_drop=%d",
			user.Email, body.Axis, body.Days, totalDrop)
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error":   "confirmation required",
			"preview": preview,
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), retentionOpTimeout)
	defer cancel()

	// Persist the durable setting FIRST (source of truth), then reconcile the DB
	// to match. If reconcile fails, the stored setting makes the next boot
	// converge, so a UI edit is never silently lost.
	if err := s.store.UpsertRetentionSetting(ctx, body.Axis, body.Days, user.Email); err != nil {
		writeErr(w, err)
		return
	}

	// Audit the apply attempt regardless of outcome (ReconcileRetention is
	// per-table; a partial failure still mutated some policies).
	applyErr := s.store.ReconcileRetention(ctx, cfg)
	log.Printf("[audit] action=set_retention actor=%s axis=%s days=%d eligible_drop=%d ok=%t",
		user.Email, body.Axis, body.Days, totalDrop, applyErr == nil)
	if applyErr != nil {
		writeErr(w, applyErr)
		return
	}

	// Return the same full snapshot shape as GET (tables + volume + env_managed).
	resp, err := s.buildStorageResponse(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
