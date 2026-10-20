package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

func TestSessionOverviewClassificationUnavailableIsNotZero(t *testing.T) {
	for _, tc := range []struct {
		name      string
		user      *auth.DashboardUser
		uidAccess bool
		available bool
	}{
		{name: "no dashboard user"},
		{name: "no dashboard ID", user: &auth.DashboardUser{Email: "owner@example.com", CctraceUserID: "uid-owner", Role: "admin"}, uidAccess: true},
		{name: "missing legacy email", user: &auth.DashboardUser{ID: 14, Role: "user"}},
		{name: "no UID access", user: &auth.DashboardUser{ID: 14, Email: "owner@example.com", Role: "user"}, uidAccess: true},
		{name: "owned scope with zero matches", user: &auth.DashboardUser{ID: 14, Email: "owner@example.com", CctraceUserID: "uid-owner", Role: "user"}, uidAccess: true, available: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var bucketCalls int
			m := &mockStore{
				countSessionOverviewsFn: func(_ context.Context, f store.SessionOverviewFilter) (int, error) {
					if tc.name == "no UID access" && f.UserID != noAccessSentinel {
						t.Errorf("missing UID must retain overview sentinel: %+v", f)
					}
					return 0, nil
				},
				sessionOverviewClassificationCountsFn: func(_ context.Context, _ store.SessionOverviewFilter) (map[string]int, error) {
					bucketCalls++
					return map[string]int{"unsorted": 0, "consulting": 0, "working": 0, "later": 0, "done": 0}, nil
				},
			}
			srv := newTestServer(m, nil)
			srv.useUserIDAccessControl = tc.uidAccess
			rec := httptest.NewRecorder()
			srv.handleCountSessionOverview(rec, personalAPIRequest(http.MethodGet, "/api/session-overview/count", "", tc.user, false))
			if rec.Code != http.StatusOK {
				t.Fatalf("count status=%d body=%s", rec.Code, rec.Body.String())
			}
			var result map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			_, hasBuckets := result["classification_counts"]
			if hasBuckets != tc.available || bucketCalls != 0 && !tc.available || bucketCalls != 1 && tc.available {
				t.Fatalf("availability=%t bucket calls=%d response=%s", hasBuckets, bucketCalls, rec.Body.String())
			}
			if tc.available {
				var buckets map[string]int
				if err := json.Unmarshal(result["classification_counts"], &buckets); err != nil || len(buckets) != 5 || buckets["working"] != 0 {
					t.Fatalf("valid empty scope buckets=%v err=%v", buckets, err)
				}
			}
		})
	}
}

func TestSessionOverviewClassificationFilterAndBuckets(t *testing.T) {
	user := &auth.DashboardUser{ID: 14, Email: "owner@example.com", Role: "user", CctraceUserID: "uid-owner"}
	var listed, counted, bucketed int
	m := &mockStore{
		listSessionOverviewsFn: func(_ context.Context, f store.SessionOverviewFilter) ([]*store.SessionOverview, error) {
			listed++
			if f.Classification != "working" || f.DashboardUserID != user.ID || f.PersonalUserID != "uid-owner" || f.UserID != "uid-owner" || !f.FoldLineage || f.Source != "interactive" || f.Agent != "codex" || len(f.ProjectHashes) != 1 || f.ProjectHashes[0] != "-project" {
				t.Errorf("list filter: %+v", f)
			}
			return []*store.SessionOverview{{SessionID: "owned"}}, nil
		},
		countSessionOverviewsFn: func(_ context.Context, f store.SessionOverviewFilter) (int, error) {
			counted++
			if f.Classification != "working" || f.DashboardUserID != user.ID || f.PersonalUserID != "uid-owner" || f.UserID != "uid-owner" || !f.FoldLineage || f.Source != "interactive" || f.Agent != "codex" || len(f.ProjectHashes) != 1 || f.ProjectHashes[0] != "-project" {
				t.Errorf("count filter: %+v", f)
			}
			return 1, nil
		},
		sessionOverviewClassificationCountsFn: func(_ context.Context, f store.SessionOverviewFilter) (map[string]int, error) {
			bucketed++
			if f.Classification != "" || f.DashboardUserID != user.ID || f.PersonalUserID != "uid-owner" || f.UserID != "uid-owner" || !f.FoldLineage || f.Source != "interactive" || f.Agent != "codex" || len(f.ProjectHashes) != 1 || f.ProjectHashes[0] != "-project" {
				t.Errorf("bucket filter: %+v", f)
			}
			return map[string]int{"unsorted": 2, "consulting": 0, "working": 1, "later": 0, "done": 0}, nil
		},
	}
	srv := newTestServer(m, nil)
	srv.useUserIDAccessControl = true
	for _, test := range []struct {
		path string
		want int
	}{
		{"/api/session-overview?classification=working&assembled=1&source=interactive&agent=codex&project_hashes=project", http.StatusOK},
		{"/api/session-overview/count?classification=working&assembled=1&source=interactive&agent=codex&project_hashes=project", http.StatusOK},
		{"/api/session-overview?classification=invalid", http.StatusBadRequest},
		{"/api/session-overview/count?classification=invalid", http.StatusBadRequest},
	} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, personalAPIRequest(http.MethodGet, test.path, "", user, false))
		if rec.Code != test.want {
			t.Fatalf("%s status=%d want=%d", test.path, rec.Code, test.want)
		}
		if test.want == http.StatusOK && strings.HasPrefix(test.path, "/api/session-overview/count") {
			var result struct {
				Count, Unattributed  int
				ClassificationCounts map[string]int `json:"classification_counts"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || result.Count != 1 || result.ClassificationCounts["unsorted"] != 2 || result.ClassificationCounts["working"] != 1 {
				t.Fatalf("count response: %+v, %v", result, err)
			}
		}
	}
	if listed != 1 || counted != 1 || bucketed != 1 {
		t.Fatalf("calls list=%d count=%d buckets=%d", listed, counted, bucketed)
	}
}
