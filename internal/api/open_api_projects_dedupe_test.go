package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cctrace/internal/store"
)

// Once hashes are canonical, one directory worked on with two agents is two rows in
// projects -- the table is keyed by (agent, project_hash). The caller asked which
// values the filter accepts, and the answer is one value, listed once (#303).
func TestOpenAPIProjectsListsEachHashOnce(t *testing.T) {
	older := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	m := &mockStore{
		getDashboardUserByOpenAPITokenFn: func(context.Context, string) (*store.DashboardUser, error) {
			return &store.DashboardUser{ID: 1, Email: "caller", Role: "user", IsActive: true}, nil
		},
		listProjectsFn: func(context.Context, store.ProjectFilter) ([]*store.Project, error) {
			return []*store.Project{
				{ProjectHash: "-Users-a-app", ProjectName: "", RepositoryName: "org/app", UpdatedAt: older},
				{ProjectHash: "-Users-a-app", ProjectName: "app", RepositoryName: "org/app", UpdatedAt: newer},
				{ProjectHash: "-Users-a-other", ProjectName: "other", UpdatedAt: older},
			}, nil
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/open/v1/projects", nil)
	req.Header.Set("Authorization", "Bearer web-token")
	rec := httptest.NewRecorder()
	newTestServer(m, nil).mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []struct {
			ProjectHash string `json:"project_hash"`
			ProjectName string `json:"project_name"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 2 || body.Total != 2 {
		t.Fatalf("got %d items (total %d), want 2 -- the duplicate hash was not folded: %+v",
			len(body.Items), body.Total, body.Items)
	}
	for _, it := range body.Items {
		if it.ProjectHash == "-Users-a-app" && it.ProjectName != "app" {
			t.Errorf("kept the older row; want the most recently updated one, got %+v", it)
		}
	}
}
