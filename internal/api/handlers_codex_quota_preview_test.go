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

// These three endpoints preview the #524 repair passes before they run: what
// reverting the OTEL-timeline fiction on Codex rows clears, what the quota
// mapping can fill in for account_id, and what it can attribute for
// login_email. Each measures through the same predicate its apply-side pass
// uses, so an operator approves the row count the UPDATE will actually produce.

func TestHandleCodexInferredRevertPreview(t *testing.T) {
	var called bool
	m := &mockStore{
		previewCodexInferredRevertFn: func(ctx context.Context) (*store.CodexInferredRevertPreview, error) {
			called = true
			return &store.CodexInferredRevertPreview{
				RevertRows: 1236569, RevertSessions: 40000, Reattributable: 352631,
			}, nil
		},
	}
	srv := newTestServer(m, nil)

	get := func(role string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/codex-inferred-revert-preview", nil)
		if role != "" {
			req = req.WithContext(auth.WithUser(req.Context(), &auth.DashboardUser{Role: role, Email: "a@x"}))
		}
		rec := httptest.NewRecorder()
		srv.handleCodexInferredRevertPreview(rec, req)
		return rec
	}

	t.Run("non-admin -> 403", func(t *testing.T) {
		if rec := get("user"); rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})
	t.Run("admin -> 200 with reattributable count", func(t *testing.T) {
		rec := get("admin")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if !called {
			t.Fatal("store preview was not called")
		}
		var p store.CodexInferredRevertPreview
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if p.RevertRows != 1236569 || p.RevertSessions != 40000 || p.Reattributable != 352631 {
			t.Errorf("preview = %+v", p)
		}
	})
}

func TestHandleCodexAccountPreview(t *testing.T) {
	var gotSince time.Time
	m := &mockStore{
		previewCodexAccountFillFn: func(ctx context.Context, since time.Time) (*store.CodexAccountFillPreview, error) {
			gotSince = since
			return &store.CodexAccountFillPreview{
				MissingAccountRows: 458762, FillableRows: 106131, InferredEvidenceRows: 27246,
				FillableSessions: 5000, AmbiguousSessions: 0,
			}, nil
		},
	}
	srv := newTestServer(m, nil)

	get := func(query, role string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/codex-account-preview"+query, nil)
		if role != "" {
			req = req.WithContext(auth.WithUser(req.Context(), &auth.DashboardUser{Role: role, Email: "a@x"}))
		}
		rec := httptest.NewRecorder()
		srv.handleCodexAccountPreview(rec, req)
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
	t.Run("admin -> 200 with fillable and ambiguous counts", func(t *testing.T) {
		rec := get("", "admin")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var p store.CodexAccountFillPreview
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if p.FillableRows != 106131 || p.InferredEvidenceRows != 27246 || p.AmbiguousSessions != 0 {
			t.Errorf("preview = %+v", p)
		}
		// No `since` means the unbounded pass, matching backfill-preview.
		if !gotSince.IsZero() {
			t.Errorf("since = %v, want zero", gotSince)
		}
	})
}

func TestHandleCodexAttributionPreview(t *testing.T) {
	var gotSince time.Time
	m := &mockStore{
		previewCodexQuotaAttributionFn: func(ctx context.Context, since time.Time) (*store.CodexQuotaAttributionPreview, error) {
			gotSince = since
			return &store.CodexQuotaAttributionPreview{
				AccountRows: 458762, RewriteRows: 132151, ObservedRows: 104979, InferredRows: 27172,
				UnmappedAccountRows: 0, MappedAccounts: 2, AmbiguousAccounts: 0,
			}, nil
		},
	}
	srv := newTestServer(m, nil)

	get := func(query, role string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/codex-attribution-preview"+query, nil)
		if role != "" {
			req = req.WithContext(auth.WithUser(req.Context(), &auth.DashboardUser{Role: role, Email: "a@x"}))
		}
		rec := httptest.NewRecorder()
		srv.handleCodexAttributionPreview(rec, req)
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
	t.Run("admin -> 200 with mapped/ambiguous accounts", func(t *testing.T) {
		rec := get("", "admin")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var p store.CodexQuotaAttributionPreview
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if p.MappedAccounts != 2 || p.AmbiguousAccounts != 0 || p.RewriteRows != 132151 {
			t.Errorf("preview = %+v", p)
		}
		// No `since` means the unbounded pass, matching backfill-preview.
		if !gotSince.IsZero() {
			t.Errorf("since = %v, want zero", gotSince)
		}
	})
}
