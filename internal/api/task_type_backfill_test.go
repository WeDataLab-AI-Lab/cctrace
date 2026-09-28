package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"cctrace/internal/auth"
)

func TestTaskTypeBackfillRequiresAdminAndCapsLimit(t *testing.T) {
	srv := newTestServer(&mockStore{backfillTaskTypesFn: func(_ context.Context, limit int) (int, error) {
		if limit != 3 {
			t.Fatalf("limit = %d, want 3", limit)
		}
		return 2, nil
	}}, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/task-types/backfill?limit=3", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.DashboardUser{Role: "admin"}))
	rec := httptest.NewRecorder()
	srv.handleTaskTypeBackfill(rec, req)
	// relabelled is 0 here because mockStore implements only the backfill half:
	// a store without the reclassification pass is left alone rather than failing
	// the request, which is what keeps the endpoint working against an older store.
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"relabelled\":0,\"updated\":2}\n" {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
}

func TestTaskTypeBackfillRejectsNonAdmin(t *testing.T) {
	called := false
	srv := newTestServer(&mockStore{backfillTaskTypesFn: func(_ context.Context, _ int) (int, error) {
		called = true
		return 0, nil
	}}, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/task-types/backfill", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.DashboardUser{Role: "user"}))
	rec := httptest.NewRecorder()
	srv.handleTaskTypeBackfill(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if called {
		t.Fatal("BackfillTaskTypes ran for a non-admin caller")
	}
}

// A store that can do both runs both. Leaving the second pass to a separate
// request would mean a chart that mixes two rule sets until somebody remembers
// the endpoint exists (#429).
type bothPassesStore struct {
	mockStore
	backfilled  int
	reclassify  int
	reclassifed bool
}

func (s *bothPassesStore) BackfillTaskTypes(context.Context, int) (int, error) {
	return s.backfilled, nil
}

func (s *bothPassesStore) ReclassifyStaleTaskTypes(context.Context) (int, error) {
	s.reclassifed = true
	return s.reclassify, nil
}

func TestTaskTypeBackfillAlsoRelabelsStaleRows(t *testing.T) {
	store := &bothPassesStore{backfilled: 4, reclassify: 7}
	srv := newTestServer(store, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/task-types/backfill", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.DashboardUser{Role: "admin"}))
	rec := httptest.NewRecorder()
	srv.handleTaskTypeBackfill(rec, req)

	if !store.reclassifed {
		t.Error("the reclassification pass never ran; a rule bump would leave old verdicts in place")
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"relabelled\":7,\"updated\":4}\n" {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
}
