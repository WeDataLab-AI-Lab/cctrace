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

type weeklyInsightsMockStore struct {
	*mockStore
	fn func(context.Context, time.Time, time.Time, string, string, string) (*store.WeeklyInsights, error)
}

func (m *weeklyInsightsMockStore) WeeklyInsights(ctx context.Context, since, until time.Time, profileEmail, userID, tz string) (*store.WeeklyInsights, error) {
	return m.fn(ctx, since, until, profileEmail, userID, tz)
}

func TestOpenAPIWeeklyInsightsUsesCallerScope(t *testing.T) {
	called := false
	base := &mockStore{}
	srv := newTestServer(&weeklyInsightsMockStore{mockStore: base, fn: func(_ context.Context, since, until time.Time, profileEmail, userID, tz string) (*store.WeeklyInsights, error) {
		called = true
		if profileEmail != "" || userID != "caller" {
			t.Fatalf("scope = %q %q", profileEmail, userID)
		}
		if !since.Before(until) {
			t.Fatal("invalid time range")
		}
		if tz != "Asia/Seoul" {
			t.Fatalf("tz = %q, want Asia/Seoul (query param must thread through)", tz)
		}
		return &store.WeeklyInsights{Tasks: []store.WeeklyInsightTask{{TaskType: "testing", PromptCount: 2}}}, nil
	}}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/open/v1/weekly-insights?since=2026-08-01T00:00:00Z&until=2026-08-08T00:00:00Z&tz=Asia/Seoul", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.DashboardUser{Email: "caller@example.com", CctraceUserID: "caller", Role: "user"}))
	rec := httptest.NewRecorder()
	srv.handleOpenAPIWeeklyInsights(rec, req)
	if !called || rec.Code != http.StatusOK {
		t.Fatalf("called=%v status=%d body=%s", called, rec.Code, rec.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if _, ok := response["tasks"]; !ok {
		t.Fatalf("response uses non-API field names: %s", rec.Body.String())
	}
}

func TestDashboardWeeklyInsightsUsesSignedInUserScope(t *testing.T) {
	called := false
	base := &mockStore{}
	srv := newTestServer(&weeklyInsightsMockStore{mockStore: base, fn: func(_ context.Context, _, _ time.Time, profileEmail, userID, tz string) (*store.WeeklyInsights, error) {
		called = true
		if profileEmail != "" || userID != "signed-in-user" {
			t.Fatalf("scope = %q %q", profileEmail, userID)
		}
		if tz != "" {
			t.Fatalf("tz = %q, want empty when no query param given", tz)
		}
		return &store.WeeklyInsights{}, nil
	}}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/weekly-insights", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.DashboardUser{Email: "user@example.com", CctraceUserID: "signed-in-user", Role: "user"}))
	rec := httptest.NewRecorder()
	srv.handleDashboardWeeklyInsights(rec, req)
	if !called || rec.Code != http.StatusOK {
		t.Fatalf("called=%v status=%d body=%s", called, rec.Code, rec.Body.String())
	}
}

func TestOpenAPISessionRecordIncludesOnlyDerivedTaskLabel(t *testing.T) {
	dto := newOpenAPISessionRecordDTO(&store.SessionRecord{TaskType: "testing"})
	if dto.TaskType != "testing" {
		t.Fatalf("task type = %q", dto.TaskType)
	}
}
