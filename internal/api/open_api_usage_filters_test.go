package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"cctrace/internal/store"
)

// Tokens and cost come from different queries over different tables. Tokens filter
// on project because session records carry project_hash; unified_events, which the
// cost aggregations read, has no such column. That is a fact of the data model, not
// a missing argument.
//
// So a project-filtered request cannot be answered honestly: it would pair one
// project's tokens with the whole fleet's cost in a single JSON object, in a product
// whose reason for existing is the cost number. Refusing is the honest answer, for
// the same reason an unknown group_by is refused rather than ignored.
func TestOpenAPIUsageRefusesProjectFilterItCannotCost(t *testing.T) {
	m := &mockStore{
		getDashboardUserByOpenAPITokenFn: func(ctx context.Context, token string) (*store.DashboardUser, error) {
			return &store.DashboardUser{ID: 1, Email: "caller", Role: "user", IsActive: true}, nil
		},
	}
	for _, path := range []string{
		"/api/open/v1/usage?project_hash=proj-a",
		"/api/open/v1/usage?group_by=user&project_hash=proj-a",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer web-token")
		rec := httptest.NewRecorder()
		newTestServer(m, nil).mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400 -- cost cannot honour a project filter, "+
				"and answering anyway pairs one project's tokens with everyone's cost", path, rec.Code)
		}
		var body map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body["code"] != "unsupported_filter" {
			t.Fatalf("%s: code = %q, want unsupported_filter", path, body["code"])
		}
	}
}

// An unbounded window makes one request scan all of history, twice. `until` also has
// to reach the cost query, or the two halves of the answer describe different spans.
func TestOpenAPIUsageRequiresABoundedWindow(t *testing.T) {
	m := &mockStore{
		getDashboardUserByOpenAPITokenFn: func(ctx context.Context, token string) (*store.DashboardUser, error) {
			return &store.DashboardUser{ID: 1, Email: "caller", Role: "user", IsActive: true}, nil
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/open/v1/usage", nil)
	req.Header.Set("Authorization", "Bearer web-token")
	rec := httptest.NewRecorder()
	newTestServer(m, nil).mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 -- an open-ended usage request scans all history twice", rec.Code)
	}
}
