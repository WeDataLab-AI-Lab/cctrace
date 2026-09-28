package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

func patchRetention(t *testing.T, srv *Server, body setRetentionRequest, role string, csrf bool) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/retention", bytes.NewReader(b))
	if csrf {
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	if role != "" {
		req = req.WithContext(auth.WithUser(req.Context(), &auth.DashboardUser{Role: role, Email: "admin@example.com"}))
	}
	rec := httptest.NewRecorder()
	srv.handleSetRetention(rec, req)
	return rec
}

func TestHandleSetRetention_Guards(t *testing.T) {
	t.Run("missing csrf -> 403", func(t *testing.T) {
		srv := newTestServer(&mockStore{}, nil)
		rec := patchRetention(t, srv, setRetentionRequest{Axis: "otel", Days: 30}, "admin", false)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})
	t.Run("no user -> 401", func(t *testing.T) {
		srv := newTestServer(&mockStore{}, nil)
		rec := patchRetention(t, srv, setRetentionRequest{Axis: "otel", Days: 30}, "", true)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})
	t.Run("non-admin -> 403", func(t *testing.T) {
		srv := newTestServer(&mockStore{}, nil)
		rec := patchRetention(t, srv, setRetentionRequest{Axis: "otel", Days: 30}, "user", true)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})
	t.Run("bad axis -> 400", func(t *testing.T) {
		srv := newTestServer(&mockStore{}, nil)
		rec := patchRetention(t, srv, setRetentionRequest{Axis: "bogus", Days: 30}, "admin", true)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})
	t.Run("negative days -> 400", func(t *testing.T) {
		srv := newTestServer(&mockStore{}, nil)
		rec := patchRetention(t, srv, setRetentionRequest{Axis: "otel", Days: -5}, "admin", true)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})
}

func TestHandleSetRetention_ConfirmGuard(t *testing.T) {
	dangerous := &mockStore{
		retentionPreviewAxisFn: func(ctx context.Context, axis string, days int) ([]*store.RetentionPreview, error) {
			return []*store.RetentionPreview{{Table: "otel_events", RowsToDrop: 5}}, nil
		},
	}

	t.Run("dangerous change without confirm -> 400 + preview, no apply/persist", func(t *testing.T) {
		applied := false
		persisted := false
		dangerous.reconcileRetentionFn = func(ctx context.Context, cfg store.RetentionConfig) error {
			applied = true
			return nil
		}
		dangerous.upsertRetentionSettingFn = func(ctx context.Context, axis string, days int, updatedBy string) error {
			persisted = true
			return nil
		}
		srv := newTestServer(dangerous, nil)
		rec := patchRetention(t, srv, setRetentionRequest{Axis: "otel", Days: 30, Confirmed: false}, "admin", true)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
		if applied {
			t.Errorf("reconcile must NOT run for an unconfirmed destructive change")
		}
		if persisted {
			t.Errorf("a blocked destructive change must NOT persist a setting")
		}
		var resp map[string]json.RawMessage
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		if _, ok := resp["preview"]; !ok {
			t.Errorf("response should include the preview")
		}
	})

	t.Run("dangerous change with confirm -> previews and applies the same axis/days", func(t *testing.T) {
		var gotAxis string
		var gotDays int
		dangerous.retentionPreviewAxisFn = func(ctx context.Context, axis string, days int) ([]*store.RetentionPreview, error) {
			gotAxis, gotDays = axis, days
			return []*store.RetentionPreview{{Table: "otel_events", RowsToDrop: 5}}, nil
		}
		var gotCfg store.RetentionConfig
		dangerous.reconcileRetentionFn = func(ctx context.Context, cfg store.RetentionConfig) error {
			gotCfg = cfg
			return nil
		}
		srv := newTestServer(dangerous, nil)
		rec := patchRetention(t, srv, setRetentionRequest{Axis: "otel", Days: 30, Confirmed: true}, "admin", true)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if gotAxis != "otel" || gotDays != 30 {
			t.Errorf("preview guard called with axis=%q days=%d, want otel/30", gotAxis, gotDays)
		}
		if gotCfg.OtelDays == nil || *gotCfg.OtelDays != 30 || gotCfg.SessionDays != nil {
			t.Errorf("applied cfg = %+v, want OtelDays=30 SessionDays=nil", gotCfg)
		}
	})

	t.Run("preview error fails closed (no apply)", func(t *testing.T) {
		m := &mockStore{
			retentionPreviewAxisFn: func(ctx context.Context, axis string, days int) ([]*store.RetentionPreview, error) {
				return nil, errors.New("preview boom")
			},
		}
		applied := false
		m.reconcileRetentionFn = func(ctx context.Context, cfg store.RetentionConfig) error {
			applied = true
			return nil
		}
		srv := newTestServer(m, nil)
		rec := patchRetention(t, srv, setRetentionRequest{Axis: "otel", Days: 30, Confirmed: false}, "admin", true)
		if rec.Code == http.StatusOK {
			t.Fatalf("status = %d, must not succeed when preview errors", rec.Code)
		}
		if applied {
			t.Errorf("must not apply when the preview guard errors (fail closed)")
		}
	})

	t.Run("safe change (no rows to drop) applies without confirm", func(t *testing.T) {
		safe := &mockStore{
			retentionPreviewAxisFn: func(ctx context.Context, axis string, days int) ([]*store.RetentionPreview, error) {
				return []*store.RetentionPreview{{Table: "session_records", RowsToDrop: 0}}, nil
			},
		}
		var gotCfg store.RetentionConfig
		applied := false
		safe.reconcileRetentionFn = func(ctx context.Context, cfg store.RetentionConfig) error {
			applied = true
			gotCfg = cfg
			return nil
		}
		srv := newTestServer(safe, nil)
		rec := patchRetention(t, srv, setRetentionRequest{Axis: "session", Days: 0, Confirmed: false}, "admin", true)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if !applied || gotCfg.SessionDays == nil || *gotCfg.SessionDays != 0 {
			t.Errorf("safe change should apply SessionDays=0; applied=%v cfg=%+v", applied, gotCfg)
		}
	})
}

func TestHandleSetRetention_EnvManagedRejected(t *testing.T) {
	t.Setenv("OTEL_RETENTION_DAYS", "30")
	applied := false
	persisted := false
	m := &mockStore{
		reconcileRetentionFn: func(ctx context.Context, cfg store.RetentionConfig) error {
			applied = true
			return nil
		},
		upsertRetentionSettingFn: func(ctx context.Context, axis string, days int, updatedBy string) error {
			persisted = true
			return nil
		},
	}
	srv := newTestServer(m, nil)
	// Even a "safe" increase with confirm must be refused for an env-pinned axis,
	// because boot reconcile would revert it (and it could reduce below env first).
	rec := patchRetention(t, srv, setRetentionRequest{Axis: "otel", Days: 365, Confirmed: true}, "admin", true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for env-managed axis", rec.Code)
	}
	if applied || persisted {
		t.Errorf("env-managed axis must not apply/persist: applied=%v persisted=%v", applied, persisted)
	}
}

func TestHandleSetRetention_PersistsSetting(t *testing.T) {
	var gotAxis, gotBy string
	var gotDays int
	m := &mockStore{
		retentionPreviewAxisFn: func(ctx context.Context, axis string, days int) ([]*store.RetentionPreview, error) {
			return []*store.RetentionPreview{{Table: "session_records", RowsToDrop: 0}}, nil
		},
		upsertRetentionSettingFn: func(ctx context.Context, axis string, days int, updatedBy string) error {
			gotAxis, gotDays, gotBy = axis, days, updatedBy
			return nil
		},
	}
	srv := newTestServer(m, nil)
	rec := patchRetention(t, srv, setRetentionRequest{Axis: "session", Days: 45, Confirmed: false}, "admin", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// A successful apply must durably persist the choice (survives reboot).
	if gotAxis != "session" || gotDays != 45 || gotBy != "admin@example.com" {
		t.Errorf("persisted %s/%d by %q, want session/45 by admin@example.com", gotAxis, gotDays, gotBy)
	}
}

func TestHandleRetentionPreview(t *testing.T) {
	m := &mockStore{
		retentionPreviewAxisFn: func(ctx context.Context, axis string, days int) ([]*store.RetentionPreview, error) {
			return []*store.RetentionPreview{{Table: "otel_events", NewDays: days, RowsToDrop: 3}}, nil
		},
	}
	srv := newTestServer(m, nil)

	get := func(query, role string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/retention/preview"+query, nil)
		if role != "" {
			req = req.WithContext(auth.WithUser(req.Context(), &auth.DashboardUser{Role: role, Email: "a@x"}))
		}
		rec := httptest.NewRecorder()
		srv.handleRetentionPreview(rec, req)
		return rec
	}

	t.Run("non-admin -> 403", func(t *testing.T) {
		if rec := get("?axis=otel&days=30", "user"); rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})
	t.Run("bad days -> 400", func(t *testing.T) {
		if rec := get("?axis=otel&days=abc", "admin"); rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})
	t.Run("admin -> 200 with preview", func(t *testing.T) {
		rec := get("?axis=otel&days=30", "admin")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var prev []*store.RetentionPreview
		if err := json.Unmarshal(rec.Body.Bytes(), &prev); err != nil || len(prev) != 1 || prev[0].RowsToDrop != 3 {
			t.Errorf("preview = %v (err %v)", prev, err)
		}
	})
}
