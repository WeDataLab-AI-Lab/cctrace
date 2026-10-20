package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

func TestSessionOverviewAnalysisHTTPFiltersAndGate(t *testing.T) {
	user := &auth.DashboardUser{ID: 14, Role: "user", CctraceUserID: "owner", Email: "owner@example.com"}
	var listed, counted int
	m := &mockStore{
		listSessionOverviewsFn: func(_ context.Context, f store.SessionOverviewFilter) ([]*store.SessionOverview, error) {
			if !f.IncludeAnalysis {
				return []*store.SessionOverview{}, nil
			}
			listed++
			if !f.AnalysisOwner || f.AnalysisCategory != "recent" || f.AnalysisPlan != "file" || f.AnalysisQuery != "Match" || f.AnalysisSort != "requests" || f.UserID != "owner" || f.DashboardUserID != 14 {
				t.Errorf("analysis list filter=%+v", f)
			}
			return []*store.SessionOverview{}, nil
		},
		countSessionOverviewsFn: func(_ context.Context, f store.SessionOverviewFilter) (int, error) {
			counted++
			if !f.IncludeAnalysis || !f.AnalysisOwner || f.AnalysisCategory != "recent" || f.AnalysisPlan != "file" || f.AnalysisQuery != "Match" || f.AnalysisSort != "requests" {
				t.Errorf("analysis count filter=%+v", f)
			}
			return 12, nil
		},
	}
	srv := newTestServer(m, nil)
	srv.useUserIDAccessControl = true
	req := func(path string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		return r.WithContext(auth.WithUser(r.Context(), user))
	}
	query := "?include=analysis&category=recent&plan=file&q=Match&sort=requests"
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req("/api/session-overview"+query))
	if rec.Code != http.StatusOK || listed != 0 {
		t.Fatalf("feature off list=%d calls=%d body=%s", rec.Code, listed, rec.Body.String())
	}
	srv.WithSessionAnalysisEnabled(true)
	for _, path := range []string{"/api/session-overview" + query, "/api/session-overview/count" + query} {
		rec = httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req(path))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	if listed != 1 || counted != 1 {
		t.Fatalf("list calls=%d count calls=%d", listed, counted)
	}
	for _, path := range []string{"/api/session-overview?include=analysis&category=unknown", "/api/session-overview/count?include=analysis&sort=wrong", "/api/session-overview?include=analysis&plan=wrong"} {
		rec = httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req(path))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestSessionOverviewAnalysisHTTPUsesSnapshotPersonalValues(t *testing.T) {
	class, firstLine := "working", "snapshot note"
	m := &mockStore{
		listSessionOverviewsFn: func(_ context.Context, f store.SessionOverviewFilter) ([]*store.SessionOverview, error) {
			return []*store.SessionOverview{{SessionID: "owned", Classification: &class, FirstLine: &firstLine}}, nil
		},
		personalOverviewValuesFn: func(_ int64, _, _ string, _ []string) (map[string]store.PersonalOverviewValues, error) {
			t.Error("analysis response must not fetch personal values after its snapshot")
			return nil, nil
		},
	}
	srv := newTestServer(m, nil)
	srv.useUserIDAccessControl = true
	srv.WithSessionAnalysisEnabled(true)
	r := httptest.NewRequest(http.MethodGet, "/api/session-overview?include=analysis", nil)
	r = r.WithContext(auth.WithUser(r.Context(), &auth.DashboardUser{ID: 7, Role: "user", CctraceUserID: "owner"}))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, r)
	var rows []store.SessionOverview
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil || rec.Code != http.StatusOK || len(rows) != 1 || rows[0].Classification == nil || *rows[0].Classification != class || rows[0].FirstLine == nil || *rows[0].FirstLine != firstLine {
		t.Fatalf("snapshot response status=%d body=%s err=%v", rec.Code, rec.Body.String(), err)
	}
}

func TestSessionOverviewAnalysisHTTPUsesSnapshotCounts(t *testing.T) {
	for _, role := range []string{"admin", "user"} {
		t.Run(role, func(t *testing.T) {
			calls := 0
			m := &mockStore{
				sessionOverviewAnalysisCountFn: func(_ context.Context, f store.SessionOverviewFilter, includeUnattributed bool) (int, map[string]int, int, error) {
					calls++
					if !f.IncludeAnalysis || includeUnattributed != (role == "admin") {
						t.Errorf("snapshot count scope=%+v unattributed=%t", f, includeUnattributed)
					}
					unattributed := 0
					if includeUnattributed {
						unattributed = 3
					}
					return 7, map[string]int{"working": 4}, unattributed, nil
				},
				countSessionOverviewsFn: func(_ context.Context, _ store.SessionOverviewFilter) (int, error) {
					t.Error("analysis count response must not query outside its snapshot")
					return 0, nil
				},
			}
			srv := newTestServer(m, nil)
			srv.useUserIDAccessControl = true
			srv.WithSessionAnalysisEnabled(true)
			r := httptest.NewRequest(http.MethodGet, "/api/session-overview/count?include=analysis&login_email=selected@example.com", nil)
			r = r.WithContext(auth.WithUser(r.Context(), &auth.DashboardUser{ID: 7, Role: role, CctraceUserID: "owner"}))
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, r)
			var body struct {
				Count        int            `json:"count"`
				Unattributed int            `json:"unattributed"`
				Classes      map[string]int `json:"classification_counts"`
			}
			wantUnattributed := 0
			if role == "admin" {
				wantUnattributed = 3
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK || calls != 1 || body.Count != 7 || body.Classes["working"] != 4 || body.Unattributed != wantUnattributed {
				t.Fatalf("snapshot count status=%d body=%s calls=%d err=%v", rec.Code, rec.Body.String(), calls, err)
			}
		})
	}
}

func TestSessionOverviewCategoriesHTTPAuthorizationAndPopulation(t *testing.T) {
	owner := &auth.DashboardUser{ID: 7, Role: "user", CctraceUserID: "owner", Email: "owner@example.com"}
	var calls int
	var sources []string
	m := &mockStore{sessionOverviewCategoryCountsFn: func(_ context.Context, f store.SessionOverviewFilter) (map[string]int, error) {
		calls++
		sources = append(sources, f.Source)
		if !f.IncludeAnalysis || !f.AnalysisOwner || f.DashboardUserID != 7 || f.PersonalUserID != "owner" || f.UserID != "owner" || f.Source != "interactive" && f.Source != "headless" && f.Source != "all" || f.Limit != 0 || f.Offset != 0 {
			t.Errorf("category population=%+v", f)
		}
		return map[string]int{"recent": 312, "waiting": 2}, nil
	}}
	srv := newTestServer(m, nil)
	srv.useUserIDAccessControl = true
	request := func(path string, user *auth.DashboardUser) *http.Request {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		return r.WithContext(auth.WithUser(r.Context(), user))
	}
	path := "/api/session-overview/categories?limit=1&offset=99"
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, request(path, owner))
	if rec.Code != http.StatusNotFound || calls != 0 {
		t.Fatalf("gate status=%d calls=%d", rec.Code, calls)
	}
	srv.WithSessionAnalysisEnabled(true)
	for _, p := range []string{path, path + "&source=all", path + "&source=headless"} {
		rec = httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, request(p, owner))
		var body map[string]int
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK || body["recent"] != 312 {
			t.Fatalf("%s status=%d body=%s err=%v", p, rec.Code, rec.Body.String(), err)
		}
	}
	if calls != 3 {
		t.Fatalf("count calls=%d", calls)
	}
	for i, want := range []string{"interactive", "all", "headless"} {
		if sources[i] != want {
			t.Fatalf("category source[%d]=%q want=%q", i, sources[i], want)
		}
	}
	for _, tc := range []struct {
		path string
		user *auth.DashboardUser
		want int
	}{
		{path, &auth.DashboardUser{ID: 9, Role: "admin"}, http.StatusForbidden},
		{path, &auth.DashboardUser{ID: 9, Role: "user", Email: "other@example.com"}, http.StatusNotFound},
		{path + "&source=invalid", owner, http.StatusBadRequest},
		{path + "&classification=invalid", owner, http.StatusBadRequest},
	} {
		rec = httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, request(tc.path, tc.user))
		if rec.Code != tc.want {
			t.Fatalf("%s status=%d want=%d body=%s", tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
	if calls != 3 {
		t.Fatalf("unauthorized/invalid category call count=%d", calls)
	}
}
