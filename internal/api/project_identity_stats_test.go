package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cctrace/internal/store"
)

type statsCaptureStore struct {
	*mockStore
	filters map[string]store.EventFilter
}

func newStatsCaptureStore() *statsCaptureStore {
	return &statsCaptureStore{mockStore: &mockStore{}, filters: make(map[string]store.EventFilter)}
}

func (s *statsCaptureStore) capture(name string, f store.EventFilter) {
	s.filters[name] = f
}

func (s *statsCaptureStore) TimeSeriesStats(_ context.Context, f store.EventFilter, _ string) ([]*store.DailyStat, error) {
	s.capture("timeseries", f)
	return nil, nil
}

func (s *statsCaptureStore) TimeSeriesStatsByModel(_ context.Context, f store.EventFilter, _ string) ([]*store.ModelDailyStat, error) {
	s.capture("timeseries-by-model", f)
	return nil, nil
}

func (s *statsCaptureStore) TimeSeriesStatsByUser(_ context.Context, f store.EventFilter, _ string) ([]*store.UserDailyStat, error) {
	s.capture("timeseries-by-user", f)
	return nil, nil
}

func (s *statsCaptureStore) LatestActivityTs(_ context.Context, f store.EventFilter) (time.Time, bool, error) {
	s.capture("latest-activity", f)
	return time.Time{}, false, nil
}

func TestStatsHandlersPassRepairedProjectHashMembers(t *testing.T) {
	paths := []struct {
		name string
		path string
	}{
		{name: "timeseries", path: "/api/stats/timeseries"},
		{name: "timeseries-by-model", path: "/api/stats/timeseries-by-model"},
		{name: "timeseries-by-user", path: "/api/stats/timeseries-by-user"},
		{name: "latest-activity", path: "/api/stats/latest-activity"},
	}

	for _, tc := range paths {
		t.Run(tc.name, func(t *testing.T) {
			m := newStatsCaptureStore()
			srv := newTestServer(m, nil)
			req := httptest.NewRequest(http.MethodGet,
				tc.path+"?project_hashes=users-alice-my_app,C--Work-App&project_hash=legacy-project",
				nil)
			rec := httptest.NewRecorder()

			srv.mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}

			got, ok := m.filters[tc.name]
			if !ok {
				t.Fatalf("store filter was not captured")
			}
			wantMembers := []string{"-users-alice-my-app", "c--work-app"}
			if len(got.ProjectHashes) != len(wantMembers) {
				t.Fatalf("project hashes = %v, want %v", got.ProjectHashes, wantMembers)
			}
			if !got.ProjectHashesPresent {
				t.Fatal("ProjectHashesPresent = false, want true for an explicit member query")
			}
			for i, want := range wantMembers {
				if got.ProjectHashes[i] != want {
					t.Fatalf("project hashes = %v, want %v", got.ProjectHashes, wantMembers)
				}
			}
			if got.ProjectHash != "-legacy-project" {
				t.Fatalf("scalar project hash = %q, want repaired legacy-project", got.ProjectHash)
			}
		})
	}
}

func TestStatsHandlersTreatExplicitEmptyMemberListAsNoMatches(t *testing.T) {
	m := newStatsCaptureStore()
	srv := newTestServer(m, nil)
	req := httptest.NewRequest(http.MethodGet,
		"/api/stats/timeseries?project_hashes=&project_hash=legacy-project", nil)
	rec := httptest.NewRecorder()

	srv.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	got := m.filters["timeseries"]
	if len(got.ProjectHashes) != 0 {
		t.Fatalf("empty member list = %v, want empty list", got.ProjectHashes)
	}
	if !got.ProjectHashesPresent {
		t.Fatal("ProjectHashesPresent = false, want true for an explicit empty member query")
	}
	if got.ProjectHash != "-legacy-project" {
		t.Fatalf("scalar project hash = %q, want repaired legacy-project", got.ProjectHash)
	}
}

func TestStatsHandlersKeepScalarProjectHashWhenMemberListAbsent(t *testing.T) {
	paths := []struct {
		name string
		path string
	}{
		{name: "timeseries-by-model", path: "/api/stats/timeseries-by-model"},
		{name: "timeseries-by-user", path: "/api/stats/timeseries-by-user"},
	}

	for _, tc := range paths {
		t.Run(tc.name, func(t *testing.T) {
			m := newStatsCaptureStore()
			srv := newTestServer(m, nil)
			req := httptest.NewRequest(http.MethodGet, tc.path+"?project_hash=legacy-project", nil)
			rec := httptest.NewRecorder()

			srv.mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			got := m.filters[tc.name]
			if got.ProjectHashesPresent {
				t.Fatal("ProjectHashesPresent = true, want false when project_hashes is absent")
			}
			if len(got.ProjectHashes) != 0 {
				t.Fatalf("project hashes = %v, want absent member list", got.ProjectHashes)
			}
			if got.ProjectHash != "-legacy-project" {
				t.Fatalf("scalar project hash = %q, want repaired legacy-project", got.ProjectHash)
			}
		})
	}
}
