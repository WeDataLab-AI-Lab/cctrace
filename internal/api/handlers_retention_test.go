package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"cctrace/internal/auth"
)

func TestHandleRetention_AdminOnly(t *testing.T) {
	srv := newTestServer(&mockStore{}, nil).WithDataPath(os.TempDir())

	t.Run("non-admin gets 403", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/retention", nil)
		req = req.WithContext(auth.WithUser(req.Context(), &auth.DashboardUser{Role: "user"}))
		rec := httptest.NewRecorder()
		srv.handleRetention(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})

	t.Run("missing user gets 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/retention", nil)
		rec := httptest.NewRecorder()
		srv.handleRetention(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("admin gets 200 with tables and volume", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/retention", nil)
		req = req.WithContext(auth.WithUser(req.Context(), &auth.DashboardUser{Role: "admin"}))
		rec := httptest.NewRecorder()
		srv.handleRetention(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var resp storageResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(resp.Tables) == 0 {
			t.Errorf("expected tables in response")
		}
		// The volume panel only populates on platforms with a real diskUsage
		// (linux/darwin); elsewhere the stub returns 0 and Volume is omitted.
		if diskUsageSupported {
			if resp.Volume == nil || resp.Volume.TotalBytes == 0 {
				t.Errorf("expected volume with non-zero total, got %+v", resp.Volume)
			}
		} else if resp.Volume != nil {
			t.Errorf("expected no volume panel on unsupported platform, got %+v", resp.Volume)
		}
	})
}

func TestHandleRetention_EnvManaged(t *testing.T) {
	t.Setenv("OTEL_RETENTION_DAYS", "30")
	os.Unsetenv("SESSION_RETENTION_DAYS")
	srv := newTestServer(&mockStore{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/retention", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.DashboardUser{Role: "admin"}))
	rec := httptest.NewRecorder()
	srv.handleRetention(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp storageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.EnvManaged["otel"] {
		t.Errorf("otel should be env-managed when OTEL_RETENTION_DAYS is set")
	}
	if resp.EnvManaged["session"] {
		t.Errorf("session should NOT be env-managed when its env var is unset")
	}
}

func TestDiskUsage(t *testing.T) {
	if !diskUsageSupported {
		t.Skip("diskUsage returns no data on this platform")
	}
	total, free, used, err := diskUsage(os.TempDir())
	if err != nil {
		t.Fatalf("diskUsage: %v", err)
	}
	if total == 0 || free == 0 || used == 0 {
		t.Errorf("diskUsage(tmp) = total=%d free=%d used=%d, want all > 0", total, free, used)
	}
	if free > total || used > total {
		t.Errorf("diskUsage(tmp): free=%d used=%d must each be <= total=%d", free, used, total)
	}
}
