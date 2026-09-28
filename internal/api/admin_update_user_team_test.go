package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cctrace/internal/auth"
)

// An empty team breaks that user's `cctrace init` (profile validation requires
// it), and create already refuses one. Update must refuse it the same way
// rather than overwrite a team with "" (#763 follow-up).
func TestHandleUpdateDashboardUserRejectsEmptyTeam(t *testing.T) {
	for _, team := range []string{`""`, `"   "`} {
		srv := newTestServer(&mockStore{}, nil)
		req := httptest.NewRequest(http.MethodPatch, "/api/admin/users/2", strings.NewReader(`{"team":`+team+`}`))
		req.SetPathValue("id", "2")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		req = req.WithContext(auth.WithUser(req.Context(), &auth.DashboardUser{Role: "admin", Email: "admin@example.com"}))
		rec := httptest.NewRecorder()
		srv.handleUpdateDashboardUser(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "team") {
			t.Fatalf("team=%s: status=%d body=%s, want 400 naming team", team, rec.Code, rec.Body.String())
		}
	}
}
