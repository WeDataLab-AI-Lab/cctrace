package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

func personalAPIRequest(method, path, body string, user *auth.DashboardUser, csrf bool) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if csrf {
		r.Header.Set("Origin", "http://example.com")
		r.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	if user != nil {
		r = r.WithContext(auth.WithUser(r.Context(), user))
	}
	return r
}

func TestPersonalRoutesPrivateCSRFAndRevision(t *testing.T) {
	user := &auth.DashboardUser{ID: 13, Email: "owner@example.com", Role: "user", CctraceUserID: "uid-owner"}
	var called int
	m := &mockStore{personalSessionPutFn: func(id int64, sid, email, uid string, class, note *string, rev int64) (*store.PersonalSessionState, error) {
		called++
		if id != 13 || sid != "mine" || uid != "uid-owner" || email != "" || rev != 0 || note == nil || *note != "<script>text</script>" {
			t.Errorf("unexpected personal write %d %s %s %s %d %v", id, sid, email, uid, rev, note)
		}
		return &store.PersonalSessionState{SessionID: sid, Classification: "unsorted", Revision: 1, Note: &store.PersonalNote{Text: *note, UpdatedAt: time.Now()}}, nil
	}, personalSessionGetFn: func(id int64, sid, email, uid string) (*store.PersonalSessionState, error) {
		return nil, store.ErrPersonalNotFound
	}}
	srv := newTestServer(m, nil)
	for _, tc := range []struct {
		method, path, body string
		principal          *auth.DashboardUser
		csrf               bool
		want               int
	}{
		{http.MethodPut, "/api/sessions/mine/personal", `{"note":"<script>text</script>","expected_revision":0}`, user, true, http.StatusOK},
		{http.MethodPut, "/api/sessions/mine/personal", `{"note":"x","expected_revision":0}`, user, false, http.StatusForbidden},
		{http.MethodPut, "/api/sessions/mine/personal", `{"note":"x"}`, user, true, http.StatusBadRequest},
		{http.MethodPut, "/api/sessions/mine/personal", `{"note":"x","expected_revision":0,"dashboard_user_id":14}`, user, true, http.StatusBadRequest},
		{http.MethodGet, "/api/sessions/mine/personal", "", nil, false, http.StatusUnauthorized},
		{http.MethodGet, "/api/sessions/unknown/personal", "", user, false, http.StatusNotFound},
	} {
		req := personalAPIRequest(tc.method, tc.path, tc.body, tc.principal, tc.csrf)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s %s: status=%d want=%d body=%s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
	if called != 1 {
		t.Fatalf("personal writes=%d want=1", called)
	}
}

func TestPersonalOverviewRouteScopesCallerAndReturnsActualRow(t *testing.T) {
	owner := &auth.DashboardUser{ID: 14, Email: "owner@example.com", Role: "user", CctraceUserID: "uid-owner"}
	for _, test := range []struct {
		name, email, uid string
		userIDMode       bool
	}{
		{"personal-token", "", "uid-owner", true},
		{"legacy-profile", "owner@example.com", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := &mockStore{personalOverviewGetFn: func(id int64, sid, email, uid string) (*store.SessionOverview, error) {
				if id != 14 || email != test.email || uid != test.uid {
					t.Errorf("overview scope %d %q %q %q", id, sid, email, uid)
				}
				if sid != "outside-page" {
					return nil, store.ErrPersonalNotFound
				}
				start := time.Date(2026, time.October, 8, 10, 0, 0, 0, time.UTC)
				return &store.SessionOverview{SessionID: sid, Agent: "codex", InputTokens: 42, HasSync: true,
					StartTime: start, EndTime: start.Add(time.Hour)}, nil
			}}
			srv := newTestServer(m, nil)
			srv.useUserIDAccessControl = test.userIDMode
			for _, check := range []struct {
				user *auth.DashboardUser
				path string
				want int
			}{
				{nil, "/api/sessions/outside-page/personal/overview", http.StatusUnauthorized},
				{owner, "/api/sessions/missing/personal/overview", http.StatusNotFound},
				{owner, "/api/sessions/outside-page/personal/overview", http.StatusOK},
			} {
				rec := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rec, personalAPIRequest(http.MethodGet, check.path, "", check.user, false))
				if rec.Code != check.want {
					t.Fatalf("status %d, want %d: %s", rec.Code, check.want, rec.Body.String())
				}
				if check.want == http.StatusOK && (!strings.Contains(rec.Body.String(), `"input_tokens":42`) ||
					!strings.Contains(rec.Body.String(), `"agent":"codex"`) ||
					!strings.Contains(rec.Body.String(), `"start_time":"2026-10-08T10:00:00Z"`) ||
					!strings.Contains(rec.Body.String(), `"end_time":"2026-10-08T11:00:00Z"`)) {
					t.Fatalf("overview did not return actual row: %s", rec.Body.String())
				}
			}
		})
	}
}

func TestSessionOverviewListBulkPersonalValues(t *testing.T) {
	owner := &auth.DashboardUser{ID: 14, Email: "owner@example.com", Role: "user", CctraceUserID: "uid-owner"}
	admin := &auth.DashboardUser{ID: 15, Email: "admin@example.com", Role: "admin"}
	for _, test := range []struct {
		name, email, uid string
		userIDMode       bool
	}{
		{"uid", "", "uid-owner", true},
		{"legacy", "owner@example.com", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var listCalls, bulkCalls int
			m := &mockStore{listSessionOverviewsFn: func(_ context.Context, f store.SessionOverviewFilter) ([]*store.SessionOverview, error) {
				listCalls++
				if f.Limit != 2 || f.Offset != 1 || (listCalls == 1 && (f.ProfileEmail != test.email || f.UserID != test.uid)) {
					t.Errorf("list filter: %+v", f)
				}
				return []*store.SessionOverview{{SessionID: "owned", InputTokens: 42}, {SessionID: "other", InputTokens: 10}}, nil
			}, personalOverviewValuesFn: func(id int64, email, uid string, ids []string) (map[string]store.PersonalOverviewValues, error) {
				bulkCalls++
				if len(ids) != 2 || ids[0] != "owned" || ids[1] != "other" {
					t.Errorf("bulk page IDs: %v", ids)
				}
				if id == admin.ID {
					return nil, nil
				}
				if id != owner.ID || email != test.email || uid != test.uid {
					t.Errorf("bulk scope: id=%d email=%q uid=%q", id, email, uid)
				}
				first := "<script>visible as text</script>"
				return map[string]store.PersonalOverviewValues{"owned": {Classification: "working", FirstLine: &first}}, nil
			}}
			srv := newTestServer(m, nil)
			srv.useUserIDAccessControl = test.userIDMode
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, personalAPIRequest(http.MethodGet, "/api/session-overview?limit=2&offset=1", "", owner, false))
			if rec.Code != http.StatusOK || listCalls != 1 || bulkCalls != 1 {
				t.Fatalf("calls list=%d bulk=%d status=%d body=%s", listCalls, bulkCalls, rec.Code, rec.Body.String())
			}
			var rows []map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil || len(rows) != 2 || rows[0]["classification"] != "working" || rows[0]["first_line"] != "<script>visible as text</script>" || rows[0]["input_tokens"] != float64(42) {
				t.Fatalf("owned response: %v %+v", err, rows)
			}
			if _, exists := rows[1]["classification"]; exists {
				t.Fatalf("other row exposed classification: %+v", rows[1])
			}
			if _, exists := rows[1]["first_line"]; exists {
				t.Fatalf("other row exposed first line: %+v", rows[1])
			}
			bulkCalls = 0
			rec = httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, personalAPIRequest(http.MethodGet, "/api/session-overview?limit=2&offset=1", "", admin, false))
			if rec.Code != http.StatusOK || bulkCalls != 1 {
				t.Fatalf("admin status=%d bulk=%d: %s", rec.Code, bulkCalls, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), `"classification"`) || strings.Contains(rec.Body.String(), `"first_line"`) {
				t.Fatalf("admin received another account's private values: %s", rec.Body.String())
			}
		})
	}
}

func TestPersonalGlobalAndListRoutes(t *testing.T) {
	user := &auth.DashboardUser{ID: 14, Email: "owner@example.com", Role: "user", CctraceUserID: "uid-owner"}
	m := &mockStore{personalGlobalPutFn: func(id int64, text string, rev int64, del bool) (*store.PersonalGlobalNote, error) {
		if id != 14 || rev != 0 || text != "private" || del {
			t.Errorf("global write %d %q %d %t", id, text, rev, del)
		}
		return &store.PersonalGlobalNote{Text: text, Revision: 1}, nil
	}, personalNotesFn: func(id int64, email, uid string, limit int, cursor string) (*store.PersonalNotePage, error) {
		if id != 14 || email != "" || uid != "uid-owner" || limit != 10 || cursor != "next-page" {
			t.Errorf("list args %d %q %q %d %q", id, email, uid, limit, cursor)
		}
		return &store.PersonalNotePage{Items: []store.PersonalSessionNoteSummary{}}, nil
	}, personalCountsFn: func(id int64, email, uid string, filter store.PersonalCountFilter) (*store.PersonalClassificationCounts, error) {
		if id != 14 || email != "" || uid != "uid-owner" || filter.Agent != "other" ||
			filter.Source != "headless" || filter.Classification != "working" || !filter.FoldLineage ||
			len(filter.ProjectHashes) != 1 || filter.ProjectHashes[0] != "-project-a" ||
			filter.Since == nil || filter.Until == nil {
			t.Errorf("unexpected count filter: %+v", filter)
		}
		return &store.PersonalClassificationCounts{Scope: "owned", Total: 1}, nil
	}}
	srv := newTestServer(m, nil)
	for _, tc := range []struct {
		method, path, body string
		csrf               bool
		want               int
	}{
		{http.MethodPut, "/api/me/note", `{"text":"private","expected_revision":0}`, true, http.StatusOK},
		{http.MethodGet, "/api/me/session-notes?limit=10&cursor=next-page", "", false, http.StatusOK},
		{http.MethodGet, "/api/me/session-notes?limit=101", "", false, http.StatusBadRequest},
		{http.MethodGet, "/api/me/session-classifications/counts?not-supported=1", "", false, http.StatusBadRequest},
		{http.MethodGet, "/api/me/session-classifications/counts?agent=other&source=headless&classification=working&assembled=1&project_hashes=project-a&since=2026-10-01T00:00:00Z&until=2026-10-08T00:00:00Z", "", false, http.StatusOK},
		{http.MethodGet, "/api/me/session-classifications/counts?source=invalid", "", false, http.StatusBadRequest},
		{http.MethodGet, "/api/me/session-classifications/counts?since=invalid", "", false, http.StatusBadRequest},
	} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, personalAPIRequest(tc.method, tc.path, tc.body, user, tc.csrf))
		if rec.Code != tc.want {
			t.Errorf("%s %s: status=%d want=%d body=%s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
}
