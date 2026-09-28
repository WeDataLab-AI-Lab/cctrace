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

type taskSegmentsMockStore struct {
	*mockStore
	fn func(ctx context.Context, since, until time.Time, taskType, profileEmail, userID string) (*store.TaskSegmentPage, error)
}

func (m *taskSegmentsMockStore) TaskSegmentsByType(ctx context.Context, since, until time.Time, taskType, profileEmail, userID string) (*store.TaskSegmentPage, error) {
	return m.fn(ctx, since, until, taskType, profileEmail, userID)
}

func TestTaskSegmentsUsesSignedInUserScopeAndTaskType(t *testing.T) {
	called := false
	base := &mockStore{}
	srv := newTestServer(&taskSegmentsMockStore{mockStore: base, fn: func(_ context.Context, since, until time.Time, taskType, profileEmail, userID string) (*store.TaskSegmentPage, error) {
		called = true
		if profileEmail != "" || userID != "signed-in-user" {
			t.Fatalf("scope = %q %q", profileEmail, userID)
		}
		if taskType != "testing" {
			t.Fatalf("task_type = %q, want testing", taskType)
		}
		if !since.Before(until) {
			t.Fatal("invalid time range")
		}
		return &store.TaskSegmentPage{Segments: []*store.TaskSegment{{SessionID: "s1", StartTs: since}}, Total: 1}, nil
	}}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/task-segments?since=2026-08-01T00:00:00Z&until=2026-08-08T00:00:00Z&task_type=testing", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.DashboardUser{Email: "user@example.com", CctraceUserID: "signed-in-user", Role: "user"}))
	rec := httptest.NewRecorder()
	srv.handleTaskSegments(rec, req)
	if !called || rec.Code != http.StatusOK {
		t.Fatalf("called=%v status=%d body=%s", called, rec.Code, rec.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	segments, ok := response["segments"].([]any)
	if !ok || len(segments) != 1 {
		t.Fatalf("response = %s", rec.Body.String())
	}
}

func TestTaskSegmentsRequiresTaskType(t *testing.T) {
	base := &mockStore{}
	srv := newTestServer(&taskSegmentsMockStore{mockStore: base, fn: func(context.Context, time.Time, time.Time, string, string, string) (*store.TaskSegmentPage, error) {
		t.Fatal("store should not be called without task_type")
		return &store.TaskSegmentPage{}, nil
	}}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/task-segments?since=2026-08-01T00:00:00Z&until=2026-08-08T00:00:00Z", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.DashboardUser{Email: "user@example.com", CctraceUserID: "signed-in-user", Role: "user"}))
	rec := httptest.NewRecorder()
	srv.handleTaskSegments(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestTaskSegmentsRequiresAuth(t *testing.T) {
	base := &mockStore{}
	srv := newTestServer(&taskSegmentsMockStore{mockStore: base, fn: func(context.Context, time.Time, time.Time, string, string, string) (*store.TaskSegmentPage, error) {
		t.Fatal("store should not be called without a signed-in user")
		return &store.TaskSegmentPage{}, nil
	}}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/task-segments?since=2026-08-01T00:00:00Z&until=2026-08-08T00:00:00Z&task_type=testing", nil)
	rec := httptest.NewRecorder()
	srv.handleTaskSegments(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
