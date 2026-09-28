package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"cctrace/internal/store"
)

type checkUIDPrivacyStore struct {
	mockStore
	exists bool
}

func (m *checkUIDPrivacyStore) CheckUserID(context.Context, string) (bool, []string, error) {
	return m.exists, []string{"private@example.com"}, nil
}
func (m *checkUIDPrivacyStore) GetDashboardUserByCctraceUserID(context.Context, string) (*store.DashboardUser, error) {
	return &store.DashboardUser{Name: "Private Name", Email: "private@example.com"}, nil
}
func TestCheckUIDMinimalResponse(t *testing.T) {
	for _, exists := range []bool{false, true} {
		srv := newTestServer(&checkUIDPrivacyStore{exists: exists}, nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/users/check-uid?user_id=handle", nil))
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusOK || len(body) != 1 || body["exists"] != exists {
			t.Errorf("expected only exists=%v, got %d %s", exists, rec.Code, rec.Body.String())
		}
	}
}
func TestCheckUIDRateLimit(t *testing.T) {
	srv := newTestServer(&mockStore{}, nil)
	for i := 0; i < 6; i++ {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/users/check-uid?user_id=handle", nil))
		want := http.StatusOK
		if i == 5 {
			want = http.StatusTooManyRequests
		}
		if rec.Code != want {
			t.Fatalf("request %d: want %d, got %d", i+1, want, rec.Code)
		}
	}
}
