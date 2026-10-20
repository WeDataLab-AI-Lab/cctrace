package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

type overviewDeadlineStore struct {
	*mockStore
	t       *testing.T
	readCtx context.Context
	auxRead bool
}

func (s *overviewDeadlineStore) checkContext(ctx context.Context) {
	s.t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 5*time.Second {
		s.t.Error("overview database reads must have a deadline of at most five seconds")
	}
	if s.readCtx != nil && ctx != s.readCtx {
		s.t.Error("auxiliary reads must share the first read's remaining budget")
	}
	s.readCtx = ctx
}

func (s *overviewDeadlineStore) PersonalSessionOverviewValues(ctx context.Context, _ int64, _, _ string, _ []string) (map[string]store.PersonalOverviewValues, error) {
	s.checkContext(ctx)
	s.auxRead = true
	return nil, nil
}

func (s *overviewDeadlineStore) SessionOverviewClassificationCounts(ctx context.Context, _ store.SessionOverviewFilter) (map[string]int, error) {
	s.checkContext(ctx)
	s.auxRead = true
	return map[string]int{"unsorted": 1}, nil
}

func TestSessionOverviewBoundsEntireRead(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/api/session-overview", "/api/session-overview/count"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			s := &overviewDeadlineStore{mockStore: &mockStore{}, t: t}
			s.listSessionOverviewsFn = func(ctx context.Context, _ store.SessionOverviewFilter) ([]*store.SessionOverview, error) {
				s.checkContext(ctx)
				return []*store.SessionOverview{{SessionID: "overview-deadline"}}, nil
			}
			s.countSessionOverviewsFn = func(ctx context.Context, _ store.SessionOverviewFilter) (int, error) {
				s.checkContext(ctx)
				return 1, nil
			}
			server := newTestServer(s, nil)
			server.useUserIDAccessControl = true
			r := httptest.NewRequest(http.MethodGet, path, nil)
			r = r.WithContext(auth.WithUser(r.Context(), &auth.DashboardUser{ID: 7, Role: "user", CctraceUserID: "owner"}))
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, r)
			if response.Code != http.StatusOK || !s.auxRead || s.readCtx == nil {
				t.Fatalf("status=%d auxiliary=%v context=%v body=%s", response.Code, s.auxRead, s.readCtx, response.Body.String())
			}
			if s.readCtx.Err() != context.Canceled {
				t.Error("completed overview must release its read context")
			}
			if timing := response.Header().Get("Server-Timing"); !strings.Contains(timing, ";dur=") {
				t.Error("overview must expose read-stage timing for first-load diagnosis")
			}
		})
	}
}

func TestSessionOverviewKeepsEarlierCallerDeadline(t *testing.T) {
	t.Parallel()
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	want, _ := parent.Deadline()
	m := &mockStore{listSessionOverviewsFn: func(ctx context.Context, _ store.SessionOverviewFilter) ([]*store.SessionOverview, error) {
		if deadline, ok := ctx.Deadline(); !ok || !deadline.Equal(want) {
			t.Error("overview must not extend the caller's deadline")
		}
		return []*store.SessionOverview{}, nil
	}}
	r := httptest.NewRequest(http.MethodGet, "/api/session-overview", nil).WithContext(parent)
	response := httptest.NewRecorder()
	newTestServer(m, nil).Handler().ServeHTTP(response, r)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
