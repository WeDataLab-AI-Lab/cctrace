package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

// The inference pass rewrites attribution on rows nobody observed, and the
// excluded-account tiebreak makes some of them disappear from the dashboard on a
// judgement rather than a measurement. An operator has to be able to read those
// counts before the pass runs, which is what this endpoint is for.
func TestHandleInferencePreview(t *testing.T) {
	var gotSince time.Time
	m := &mockStore{
		previewInferLoginEmailFn: func(ctx context.Context, since time.Time) (*store.LoginEmailInferencePreview, error) {
			gotSince = since
			return &store.LoginEmailInferencePreview{
				EmptyRows: 100, FillableRows: 48, UnfillableRows: 52,
				ExcludedTiebreakRows: 3, FillableUsers: 2,
			}, nil
		},
	}
	srv := newTestServer(m, nil)

	get := func(query, role string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/inference-preview"+query, nil)
		if role != "" {
			req = req.WithContext(auth.WithUser(req.Context(), &auth.DashboardUser{Role: role, Email: "a@x"}))
		}
		rec := httptest.NewRecorder()
		srv.handleInferencePreview(rec, req)
		return rec
	}

	t.Run("non-admin -> 403", func(t *testing.T) {
		if rec := get("", "user"); rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})
	t.Run("bad since -> 400", func(t *testing.T) {
		if rec := get("?since=nonsense", "admin"); rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})
	t.Run("admin -> 200 with the tiebreak count", func(t *testing.T) {
		rec := get("", "admin")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var p store.LoginEmailInferencePreview
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatalf("decode: %v", err)
		}
		// The tiebreak count is the reason this endpoint exists separately from
		// backfill-preview, so its presence in the body is the assertion.
		if p.FillableRows != 48 || p.ExcludedTiebreakRows != 3 || p.FillableUsers != 2 {
			t.Errorf("preview = %+v", p)
		}
		// No `since` means the unbounded pass, matching backfill-preview.
		if !gotSince.IsZero() {
			t.Errorf("since = %v, want zero", gotSince)
		}
	})
}
