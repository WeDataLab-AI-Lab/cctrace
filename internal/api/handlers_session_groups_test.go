package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tc "github.com/testcontainers/testcontainers-go"
	pgmod "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"cctrace/internal/containertest"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

func TestSessionGroupRoutesAuthenticateFilterAndAck(t *testing.T) {
	user := &auth.DashboardUser{ID: 8, Email: "owner@example.com", Role: "user", CctraceUserID: "uid-owner"}
	var reads, writes int
	m := &mockStore{getSessionGroupsFn: func(_ context.Context, f store.SessionOverviewFilter) (*store.SessionGroups, error) {
		reads++
		if f.DashboardUserID != 8 || f.PersonalUserID != "uid-owner" || f.UserID != "uid-owner" || f.Classification != "working" || f.Offset != 0 || f.ProjectHashes[0] != "-project-a" {
			t.Errorf("scoped GET filter: %+v", f)
		}
		return &store.SessionGroups{Scope: "filtered_total", GroupingVersion: 1}, nil
	}, updateSessionGroupDismissalsFn: func(_ context.Context, f store.SessionOverviewFilter, items []store.SessionGroupDismissal, dismiss bool) error {
		writes++
		if f.DashboardUserID != 8 || len(items) != 1 || items[0].SessionID != "one" || !dismiss {
			t.Errorf("scoped PUT: %+v %+v %t", f, items, dismiss)
		}
		return nil
	}}
	srv := newTestServer(m, nil)
	srv.useUserIDAccessControl = true
	get := func(method, path, body string, principal *auth.DashboardUser, csrf bool) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, personalAPIRequest(method, path, body, principal, csrf))
		return rec
	}
	path := "/api/me/session-groups?classification=working&project_hashes=project-a"
	if rec := get(http.MethodGet, path, "", nil, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous GET: %d %s", rec.Code, rec.Body.String())
	}
	if rec := get(http.MethodGet, path, "", &auth.DashboardUser{ID: 9, Email: "no-uid@example.com", Role: "user"}, false); rec.Code != http.StatusNotFound {
		t.Fatalf("missing UID may not imply empty groups: %d %s", rec.Code, rec.Body.String())
	}
	if rec := get(http.MethodGet, path, "", user, false); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"scope":"filtered_total"`) {
		t.Fatalf("scoped GET: %d %s", rec.Code, rec.Body.String())
	}
	at := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	body := `{"grouping_version":1,"items":[{"session_id":"one","ended_at":"` + at + `"}]}`
	for _, tc := range []struct {
		method, path, body string
		csrf               bool
		want               int
	}{
		{http.MethodPut, "/api/me/session-groups/dismiss?classification=working&project_hashes=project-a", body, false, http.StatusForbidden},
		{http.MethodPut, "/api/me/session-groups/dismiss?classification=working&project_hashes=project-a", body, true, http.StatusOK},
		{http.MethodPut, "/api/me/session-groups/dismiss?classification=working&project_hashes=project-a", `{"grouping_version":2,"items":[]}`, true, http.StatusBadRequest},
		{http.MethodDelete, "/api/me/session-groups/dismiss?classification=working&project_hashes=project-a", `{"grouping_version":1,"items":[{"session_id":"one"}]}`, true, http.StatusBadRequest},
		{http.MethodGet, path + "&unexpected=1", "", false, http.StatusBadRequest},
	} {
		rec := get(tc.method, tc.path, tc.body, user, tc.csrf)
		if rec.Code != tc.want {
			t.Fatalf("%s %s: %d want %d %s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
		if tc.want == http.StatusOK && !strings.Contains(rec.Body.String(), `"acknowledged":true`) {
			t.Fatalf("missing ACK: %s", rec.Body.String())
		}
	}
	if reads != 1 || writes != 1 {
		t.Fatalf("unexpected store calls GET=%d writes=%d", reads, writes)
	}
}

func TestSessionGroupRoutesPassAnalysisAndPeriodFilters(t *testing.T) {
	user := &auth.DashboardUser{ID: 8, Email: "owner@example.com", Role: "user", CctraceUserID: "uid-owner"}
	var reads int
	m := &mockStore{getSessionGroupsFn: func(_ context.Context, f store.SessionOverviewFilter) (*store.SessionGroups, error) {
		reads++
		if !f.IncludeAnalysis || !f.AnalysisOwner || f.AnalysisQuery != "calendar" || f.AnalysisPlan != "file" ||
			f.AnalysisCategory != "running" || f.AnalysisSort != "cost" || f.UserID != "uid-owner" ||
			f.PersonalUserID != "uid-owner" || f.Classification != "working" || f.Source != "interactive" ||
			f.Agent != "claude" || !f.FoldLineage || len(f.ProjectHashes) != 1 || f.ProjectHashes[0] != "-project-a" ||
			f.Since == nil || f.Since.Format(time.RFC3339) != "2026-10-01T00:00:00Z" ||
			f.Until == nil || f.Until.Format(time.RFC3339) != "2026-10-08T00:00:00Z" {
			t.Errorf("analysis group filter did not preserve the overview scope: %+v", f)
		}
		return &store.SessionGroups{Scope: "filtered_total", GroupingVersion: 1}, nil
	}}
	srv := newTestServer(m, nil).WithSessionAnalysisEnabled(true)
	srv.useUserIDAccessControl = true
	path := "/api/me/session-groups?include=analysis&q=calendar&plan=file&category=running&sort=cost" +
		"&since=2026-10-01T00:00:00Z&until=2026-10-08T00:00:00Z&source=interactive" +
		"&project_hashes=project-a&agent=claude&assembled=1&classification=working"
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, personalAPIRequest(http.MethodGet, path, "", user, false))
	if rec.Code != http.StatusOK || reads != 1 {
		t.Fatalf("analysis scope GET: %d reads=%d body=%s", rec.Code, reads, rec.Body.String())
	}
}

func TestSessionGroupsPostgresHTTPTotalsAndDismissBeyondCap(t *testing.T) {
	if home, err := os.UserHomeDir(); err == nil {
		sock := filepath.Join(home, ".colima", "default", "docker.sock")
		if _, err := os.Stat(sock); err == nil {
			host := "unix://" + sock
			if os.Getenv("DOCKER_HOST") == "" {
				t.Setenv("DOCKER_HOST", host)
			}
			if os.Getenv("DOCKER_HOST") == host && os.Getenv("TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE") == "" {
				t.Setenv("TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE", "/var/run/docker.sock")
			}
		}
	}
	ctx := context.Background()
	ctr, err := pgmod.Run(ctx, "timescale/timescaledb:latest-pg16", pgmod.WithDatabase("testdb"), pgmod.WithUsername("test"), pgmod.WithPassword("test"), tc.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(time.Minute)))
	if err != nil {
		containertest.SkipOrFail(t, "postgres container", err)
	}
	t.Cleanup(func() {
		if err := ctr.Terminate(context.Background()); err != nil {
			t.Error(err)
		}
	})
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.NewPgStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	owner, err := db.CreateDashboardUser(ctx, &store.DashboardUser{Email: "groups@example.test", PasswordHash: "fixture", Role: "user", CctraceUserID: "group-owner"})
	if err != nil {
		t.Fatal(err)
	}
	user := &auth.DashboardUser{ID: owner.ID, Email: owner.Email, Role: "user", CctraceUserID: "group-owner"}
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	records := make([]*store.SessionRecord, 501)
	events := make([]*store.SessionObservationEvent, 501)
	for i := range records {
		id := fmt.Sprintf("group-%04d", i)
		records[i] = &store.SessionRecord{SessionID: id, UUID: id, RecordType: "user", UserID: user.CctraceUserID, ProfileEmail: user.Email, Ts: base.Add(-time.Minute)}
		events[i] = &store.SessionObservationEvent{SessionID: id, UserID: user.CctraceUserID, ProfileEmail: user.Email, EventID: "end", Kind: "session_end", ObservedAt: base, IdentityProvenance: "personal_token"}
	}
	if err = db.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	if _, err = db.IngestSessionObservations(ctx, store.SessionObservationBatch{Events: events}); err != nil {
		t.Fatal(err)
	}
	jwt, err := auth.NewJWTManager(strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.GenerateAccessToken(user.ID, user.Email, "user", "", user.CctraceUserID, false)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(db, jwt)
	srv.useUserIDAccessControl = true
	request := func(method, path, body string, csrf bool) *http.Request {
		r := personalAPIRequest(method, path, body, nil, csrf)
		r.AddCookie(&http.Cookie{Name: "cctrace_token", Value: token})
		return r
	}
	get := func() store.SessionGroups {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, request(http.MethodGet, "/api/me/session-groups", "", false))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %d: %s", rec.Code, rec.Body.String())
		}
		var result store.SessionGroups
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	got := get()
	if got.Scope != "filtered_total" || !got.Truncated || got.Counts.Ended != 501 || got.Counts.EndedGroups != 1 || len(got.Ended) != 1 || len(got.Ended[0].Items) != 500 {
		t.Fatalf("full totals and bounded response: %+v", got)
	}
	shown := make(map[string]bool)
	for _, item := range got.Ended[0].Items {
		shown[item.SessionID] = true
	}
	outside := ""
	for _, record := range records {
		if !shown[record.SessionID] {
			outside = record.SessionID
			break
		}
	}
	body := fmt.Sprintf(`{"grouping_version":1,"items":[{"session_id":%q,"ended_at":%q}]}`, outside, base.Format(time.RFC3339Nano))
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, request(method, "/api/me/session-groups/dismiss", body, true))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"acknowledged":true`) {
			t.Fatalf("outside-cap %s %d: %s", method, rec.Code, rec.Body.String())
		}
	}
	key, cursor := got.Ended[0].Key, ""
	var members []store.SessionGroupDismissal
	for {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, request(http.MethodGet, "/api/me/session-groups/members?group_key="+key+"&limit=100&cursor="+cursor+"&login_email=foreign@example.test", "", false))
		if rec.Code != http.StatusOK {
			t.Fatalf("page HTTP %d: %s", rec.Code, rec.Body.String())
		}
		var page store.SessionGroupMemberPage
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if page.Total != 501 || len(page.Items) > 100 {
			t.Fatalf("page total/cap: %+v", page)
		}
		for _, item := range page.Items {
			members = append(members, store.SessionGroupDismissal{SessionID: item.SessionID, EndedAt: item.EndedAt})
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(members) != 501 {
		t.Fatalf("HTTP paged=%d", len(members))
	}
	for offset := 0; offset < len(members); offset += 100 {
		batch, _ := json.Marshal(map[string]any{"grouping_version": 1, "group_key": key, "items": members[offset:min(offset+100, len(members))]})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, request(http.MethodPut, "/api/me/session-groups/dismiss", string(batch), true))
		if rec.Code != http.StatusOK {
			t.Fatalf("HTTP batch %d: %s", rec.Code, rec.Body.String())
		}
	}
	if _, err := db.IngestSessionObservations(ctx, store.SessionObservationBatch{Events: []*store.SessionObservationEvent{{SessionID: members[250].SessionID, UserID: user.CctraceUserID, ProfileEmail: user.Email, EventID: "new-end", Kind: "session_end", ObservedAt: base.Add(time.Second), IdentityProvenance: "personal_token"}}}); err != nil {
		t.Fatal(err)
	}
	batch, _ := json.Marshal(map[string]any{"grouping_version": 1, "group_key": key, "items": members[:100]})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, request(http.MethodDelete, "/api/me/session-groups/dismiss", string(batch), true))
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), `"acknowledged":true`) {
		t.Fatalf("stale key HTTP %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSessionGroupMembersValidationAndAccountPolicy(t *testing.T) {
	user := &auth.DashboardUser{ID: 8, Email: "owner@example.com", Role: "user", CctraceUserID: "uid-owner"}
	calls := 0
	m := &mockStore{getSessionGroupMembersFn: func(_ context.Context, f store.SessionOverviewFilter, key, cursor string, limit int) (*store.SessionGroupMemberPage, error) {
		calls++
		want := ""
		if calls == 2 {
			want = "selected@example.com"
		}
		if f.LoginEmail != want || f.PersonalUserID != "uid-owner" || key != "key" || limit != 100 {
			t.Fatalf("scoped page: %+v %s %d", f, key, limit)
		}
		return &store.SessionGroupMemberPage{GroupKey: key, Total: 2, Items: []store.SessionGroupItem{}}, nil
	}}
	srv := newTestServer(m, nil)
	srv.useUserIDAccessControl = true
	path := "/api/me/session-groups/members?group_key=key&login_email=selected@example.com"
	for _, role := range []string{"user", "admin"} {
		user.Role = role
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, personalAPIRequest(http.MethodGet, path, "", user, false))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", role, rec.Code)
		}
	}
	for _, query := range []string{"?group_key=key&limit=101", "?group_key=key&limit=0", "?group_key=key&limit=no", "?group_key=key&group_key=other", "?limit=100"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, personalAPIRequest(http.MethodGet, "/api/me/session-groups/members"+query, "", user, false))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid query %s: %d", query, rec.Code)
		}
	}
	if calls != 2 {
		t.Fatalf("store calls=%d", calls)
	}
}

func TestSessionGroupRouteDoesNotAcknowledgeFailedStoreWrite(t *testing.T) {
	user := &auth.DashboardUser{ID: 8, Email: "owner@example.com", Role: "user", CctraceUserID: "uid-owner"}
	m := &mockStore{updateSessionGroupDismissalsFn: func(_ context.Context, _ store.SessionOverviewFilter, _ []store.SessionGroupDismissal, _ bool) error {
		return errors.New("storage unavailable")
	}}
	srv := newTestServer(m, nil)
	srv.useUserIDAccessControl = true
	at := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, personalAPIRequest(http.MethodPut, "/api/me/session-groups/dismiss",
		`{"grouping_version":1,"items":[{"session_id":"one","ended_at":"`+at+`"}]}`, user, true))
	if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), `"acknowledged":true`) {
		t.Fatalf("failed persistence must not ACK: %d %s", rec.Code, rec.Body.String())
	}
}
