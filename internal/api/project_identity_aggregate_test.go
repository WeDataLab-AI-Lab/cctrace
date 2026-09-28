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

func TestWeeklyInsightsAPI_PreservesProjectMemberHashes(t *testing.T) {
	base := &mockStore{}
	srv := newTestServer(&weeklyInsightsMockStore{
		mockStore: base,
		fn: func(context.Context, time.Time, time.Time, string, string, string) (*store.WeeklyInsights, error) {
			return &store.WeeklyInsights{Projects: []store.WeeklyInsightProject{{
				ProjectHash: "h-main", ProjectHashes: []string{"h-main", "h-worktree"},
				ProjectName: "issue-382", SessionCount: 2, TotalTokens: 300,
			}}}, nil
		},
	}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/weekly-insights?since=2026-08-01T00:00:00Z&until=2026-08-08T00:00:00Z", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.DashboardUser{CctraceUserID: "qa-user", Role: "user"}))
	rec := httptest.NewRecorder()
	srv.handleDashboardWeeklyInsights(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Projects []struct {
			ProjectHash   string   `json:"project_hash"`
			ProjectHashes []string `json:"project_hashes"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Projects) != 1 || len(response.Projects[0].ProjectHashes) != 2 {
		t.Fatalf("projects = %+v, want explicit root member hashes", response.Projects)
	}
	if response.Projects[0].ProjectHash != "h-main" || response.Projects[0].ProjectHashes[1] != "h-worktree" {
		t.Fatalf("project response = %+v, want representative plus full member set", response.Projects[0])
	}
}

func TestOrganizationInsightsAPI_PreservesProjectMemberHashes(t *testing.T) {
	srv := newTestServer(&mockStore{
		organizationInsightsFn: func(context.Context, time.Time, time.Time, int64) (*store.OrganizationInsights, error) {
			return &store.OrganizationInsights{
				Available: true, ActiveUsers: 2, MinimumUsers: 2,
				Projects: []store.OrganizationProjectInsight{{
					ProjectHash: "h-main", ProjectHashes: []string{"h-main", "h-worktree"},
					ContributorCount: 2, SessionCount: 2, TotalTokens: 300,
				}},
			}, nil
		},
	}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/organization-insights?since=2026-08-01T00:00:00Z&until=2026-08-08T00:00:00Z", nil)
	rec := httptest.NewRecorder()
	srv.handleOrganizationInsights(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Projects []struct {
			ProjectHash   string   `json:"project_hash"`
			ProjectHashes []string `json:"project_hashes"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Projects) != 1 || len(response.Projects[0].ProjectHashes) != 2 {
		t.Fatalf("projects = %+v, want explicit root member hashes", response.Projects)
	}
	if response.Projects[0].ProjectHash != "h-main" || response.Projects[0].ProjectHashes[1] != "h-worktree" {
		t.Fatalf("project response = %+v, want representative plus full member set", response.Projects[0])
	}
}
