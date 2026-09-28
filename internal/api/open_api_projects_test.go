package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"cctrace/internal/store"
)

// project_hash filters sessions and events, but until now nothing told a caller
// which hashes exist. A filter whose values cannot be discovered is a filter nobody
// outside this repository can use.
func TestOpenAPIProjectsListsWhatTheFilterAccepts(t *testing.T) {
	m := &mockStore{
		getDashboardUserByOpenAPITokenFn: func(ctx context.Context, token string) (*store.DashboardUser, error) {
			return &store.DashboardUser{ID: 1, Email: "caller", Role: "user", IsActive: true}, nil
		},
		listProjectsFn: func(ctx context.Context, f store.ProjectFilter) ([]*store.Project, error) {
			return []*store.Project{
				{ProjectHash: "h1", ProjectName: "alpha", RepositoryName: "org/alpha"},
				{ProjectHash: "h2", ProjectName: "beta", RepositoryName: "org/beta"},
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
	if body.Total != 2 || len(body.Items) != 2 {
		t.Fatalf("got %d items / total %d, want 2", len(body.Items), body.Total)
	}
	if body.Items[0].ProjectHash == "" {
		t.Fatal("project_hash is empty -- it is the value the filter takes")
	}
}

// A non-admin sees only its own projects. The scope arrives the same way every other
// endpoint gets it, not from query parameters the caller controls.
func TestOpenAPIProjectsScopesToCaller(t *testing.T) {
	var gotProfile, gotUserID string
	m := &mockStore{
		getDashboardUserByOpenAPITokenFn: func(ctx context.Context, token string) (*store.DashboardUser, error) {
			return &store.DashboardUser{ID: 1, Email: "caller", Role: "user", IsActive: true}, nil
		},
		listProjectsFn: func(ctx context.Context, f store.ProjectFilter) ([]*store.Project, error) {
			gotProfile, gotUserID = f.ProfileEmail, f.UserID
			return nil, nil
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/open/v1/projects?profile_email=someone-else", nil)
	req.Header.Set("Authorization", "Bearer web-token")
	rec := httptest.NewRecorder()
	newTestServer(m, nil).mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if gotProfile == "someone-else" {
		t.Fatal("a caller reached another account's projects by passing profile_email")
	}
	if gotProfile == "" && gotUserID == "" {
		t.Fatal("a non-admin was given an unscoped project list")
	}
}
