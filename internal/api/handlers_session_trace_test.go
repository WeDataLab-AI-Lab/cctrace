package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

type traceAPIMock struct {
	*mockStore
	reads, demands int
	access         store.SessionAnalysisAccess
}

func (m *traceAPIMock) TraceDemand(_ context.Context, id string, a store.SessionAnalysisAccess, d store.TraceDemandRequest) (store.TraceDemandResponse, error) {
	m.demands++
	m.access = a
	return store.TraceDemandResponse{SessionID: id, State: "building", LeaseID: "lease"}, nil
}
func (m *traceAPIMock) TraceRelease(context.Context, string, store.SessionAnalysisAccess, string) error {
	return nil
}
func (m *traceAPIMock) TraceOverview(_ context.Context, id string, a store.SessionAnalysisAccess, snapshot string) (store.TraceOverviewResponse, error) {
	m.reads++
	m.access = a
	return store.TraceOverviewResponse{SessionID: id, State: "building"}, nil
}
func (m *traceAPIMock) TraceEvents(context.Context, string, store.SessionAnalysisAccess, store.TraceEventsRequest) (store.TraceEventsResponse, error) {
	return store.TraceEventsResponse{State: "building"}, nil
}
func (m *traceAPIMock) TraceContext(context.Context, string, store.SessionAnalysisAccess, store.TraceContextRequest) (store.TraceContextResponse, error) {
	return store.TraceContextResponse{}, nil
}

func TestTraceAPIReadDoesNotDemandAndScopesCaller(t *testing.T) {
	t.Parallel()
	m := &traceAPIMock{mockStore: &mockStore{}}
	s := newTestServer(m, nil).WithSessionAnalysisEnabled(true)
	user := &auth.DashboardUser{ID: 7, Role: "user", Email: "owner@example.test", CctraceUserID: "owner"}
	for _, endpoint := range []string{"overview", "events?from=2026-01-01T00:00:00Z&to=2026-01-01T01:00:00Z", "context?event_id=evt"} {
		r := httptest.NewRequest("GET", "/api/sessions/session/trace/"+endpoint, nil)
		r = r.WithContext(auth.WithUser(r.Context(), user))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", endpoint, w.Code, w.Body.String())
		}
	}
	if m.demands != 0 || m.reads != 1 || m.access.UserID != "owner" || !m.access.User {
		t.Fatalf("read escaped scope/demand: %+v", m)
	}
	r := httptest.NewRequest("POST", "/api/sessions/session/trace/demand", strings.NewReader(`{"mode":"overview"}`))
	r = r.WithContext(auth.WithUser(r.Context(), user))
	r.Header.Set("Origin", "http://"+r.Host)
	r.Header.Set("X-Requested-With", "XMLHttpRequest")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusAccepted || m.demands != 1 {
		t.Fatalf("demand=%d status=%d", m.demands, w.Code)
	}
}
func TestTraceAPIBadRangeAndGate(t *testing.T) {
	t.Parallel()
	m := &traceAPIMock{mockStore: &mockStore{}}
	s := newTestServer(m, nil)
	r := httptest.NewRequest("GET", "/api/sessions/session/trace/overview", nil)
	r = r.WithContext(auth.WithUser(r.Context(), &auth.DashboardUser{ID: 1, Role: "user", CctraceUserID: "u"}))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("gate=%d", w.Code)
	}
	s.WithSessionAnalysisEnabled(true)
	for _, body := range []string{`{"mode":"range"}`, `{"mode":"range","from":"2026-01-02T00:00:00Z","to":"2026-01-01T00:00:00Z"}`, `{"mode":"invalid"}`} {
		q := httptest.NewRequest("POST", "/api/sessions/session/trace/demand", strings.NewReader(body))
		q = q.WithContext(r.Context())
		q.Header.Set("Origin", "http://"+q.Host)
		q.Header.Set("X-Requested-With", "XMLHttpRequest")
		out := httptest.NewRecorder()
		s.Handler().ServeHTTP(out, q)
		if out.Code != 400 {
			t.Fatalf("body=%s status=%d", body, out.Code)
		}
	}
	if m.demands != 0 {
		t.Fatal("invalid requests created demand")
	}
}
