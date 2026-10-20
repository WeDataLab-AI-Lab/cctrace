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

// The shared mock implements the newly added Store read without modifying unrelated fixtures.
func (m *mockStore) ReadSessionAnalysis(_ context.Context, id string, access store.SessionAnalysisAccess) (map[string]interface{}, error) {
	if m.sessionAnalysisReadFn != nil {
		return m.sessionAnalysisReadFn(id, access)
	}
	return nil, store.ErrSessionAnalysisNotFound
}

func TestSessionAnalysisRouteGateAndScope(t *testing.T) {
	var got store.SessionAnalysisAccess
	m := &mockStore{sessionAnalysisReadFn: func(id string, a store.SessionAnalysisAccess) (map[string]interface{}, error) {
		got = a
		return map[string]interface{}{"session_id": id, "state": "building"}, nil
	}}
	s := newTestServer(m, nil)
	user := &auth.DashboardUser{ID: 7, Role: "user", Email: "owner@example.com", CctraceUserID: "uid-owner"}
	request := func() *http.Request {
		r := httptest.NewRequest("GET", "/api/sessions/session-1/analysis?profile_email=other@example.com", nil)
		return r.WithContext(auth.WithUser(r.Context(), user))
	}
	off := httptest.NewRecorder()
	s.Handler().ServeHTTP(off, request())
	if off.Code != http.StatusNotFound {
		t.Fatalf("disabled route status=%d", off.Code)
	}
	s.WithSessionAnalysisEnabled(true)
	on := httptest.NewRecorder()
	s.Handler().ServeHTTP(on, request())
	if on.Code != http.StatusOK {
		t.Fatalf("enabled route status=%d, body=%s", on.Code, on.Body.String())
	}
	if !got.User || got.UserID != "uid-owner" || got.ProfileEmail != "" || got.DashboardUserID != 7 || got.RepoStatusOnly {
		t.Fatalf("user read escaped caller scope: %+v", got)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(on.Body.Bytes(), &body); err != nil || body["session_id"] != "session-1" {
		t.Fatalf("response=%v, err=%v", body, err)
	}
}

func TestSessionAnalysisAdminFilterAndMissing(t *testing.T) {
	m := &mockStore{sessionAnalysisReadFn: func(_ string, a store.SessionAnalysisAccess) (map[string]interface{}, error) {
		if a.User || a.ProfileEmail != "chosen@example.com" || a.LoginEmail != "login@example.com" {
			t.Fatalf("admin scope=%+v", a)
		}
		return nil, store.ErrSessionAnalysisNotFound
	}}
	s := newTestServer(m, nil).WithSessionAnalysisEnabled(true)
	user := &auth.DashboardUser{ID: 11, Role: "admin", Email: "admin@example.com"}
	r := httptest.NewRequest("GET", "/api/sessions/missing/analysis?profile_email=chosen@example.com&login_email=login@example.com", nil)
	r = r.WithContext(auth.WithUser(r.Context(), user))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing route status=%d, body=%s", w.Code, w.Body.String())
	}
}

func TestSessionAnalysisRepoStatusViewReturnsOnlyOwnerSignal(t *testing.T) {
	status := map[string]interface{}{"branch": "feature/p4", "uncommitted_edited_files": 2, "uncommitted_state": "observed"}
	m := &mockStore{sessionAnalysisReadFn: func(id string, access store.SessionAnalysisAccess) (map[string]interface{}, error) {
		if !access.RepoStatusOnly {
			t.Fatal("repo status requested the full analysis path")
		}
		return map[string]interface{}{
			"session_id": id,
			"owner_view": true,
			"state":      "ready",
			"header":     map[string]interface{}{"repo_status": status},
			"turns":      []string{"private analysis detail"},
		}, nil
	}}
	s := newTestServer(m, nil).WithSessionAnalysisEnabled(true)
	r := httptest.NewRequest("GET", "/api/sessions/session-1/analysis?view=repo-status", nil)
	r = r.WithContext(auth.WithUser(r.Context(), &auth.DashboardUser{ID: 7, Role: "user", Email: "owner@example.com"}))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("repo status view status=%d body=%s", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["session_id"] != "session-1" || body["owner_view"] != true || body["repo_status"] == nil {
		t.Fatalf("repo status response=%v", body)
	}
	if _, ok := body["turns"]; ok {
		t.Fatalf("repo status response exposed unrelated analysis data: %v", body)
	}
	m.sessionAnalysisReadFn = func(id string, access store.SessionAnalysisAccess) (map[string]interface{}, error) {
		if !access.RepoStatusOnly {
			t.Fatal("admin repo status requested the full analysis path")
		}
		return map[string]interface{}{
			"session_id": id,
			"owner_view": false,
			"header":     map[string]interface{}{"repo_status": map[string]interface{}{"branch": "private-branch"}},
		}, nil
	}
	adminRequest := httptest.NewRequest("GET", "/api/sessions/session-1/analysis?view=repo-status", nil)
	adminRequest = adminRequest.WithContext(auth.WithUser(adminRequest.Context(), &auth.DashboardUser{ID: 8, Role: "admin", Email: "admin@example.com"}))
	adminResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(adminResponse, adminRequest)
	var adminBody map[string]interface{}
	if err := json.Unmarshal(adminResponse.Body.Bytes(), &adminBody); err != nil {
		t.Fatal(err)
	}
	if adminResponse.Code != http.StatusOK || adminBody["repo_status"] != nil {
		t.Fatalf("non-owner admin repo status response=%v status=%d", adminBody, adminResponse.Code)
	}
}
