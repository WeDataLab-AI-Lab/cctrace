package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"cctrace/internal/store"
)

// Hashes are lowercased now, but people have the old spelling: in a bookmark, in a
// script, in output this API produced last week. A filter that silently returns
// nothing for a value we ourselves published is worse than an error, so filter input
// is canonicalised exactly the way the stored value was (#303).
func TestSessionFilterCanonicalisesProjectHash(t *testing.T) {
	var got store.SessionOverviewFilter
	m := &mockStore{
		getDashboardUserByOpenAPITokenFn: func(context.Context, string) (*store.DashboardUser, error) {
			return &store.DashboardUser{ID: 1, Email: "caller", Role: "admin", IsActive: true}, nil
		},
		listSessionOverviewsFn: func(_ context.Context, f store.SessionOverviewFilter) ([]*store.SessionOverview, error) {
			got = f
			return nil, nil
		},
	}
	req := httptest.NewRequest(http.MethodGet,
		`/api/open/v1/sessions?project_hash=C--Work-App&project_hashes=users-alice-my_app`, nil)
	req.Header.Set("Authorization", "Bearer web-token")
	rec := httptest.NewRecorder()
	newTestServer(m, nil).mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	want := []string{"c--work-app", "-users-alice-my-app"}
	if len(got.ProjectHashes) != len(want) {
		t.Fatalf("filter hashes = %v, want %v", got.ProjectHashes, want)
	}
	for i, w := range want {
		if got.ProjectHashes[i] != w {
			t.Errorf("filter hash[%d] = %q, want %q", i, got.ProjectHashes[i], w)
		}
	}
}
