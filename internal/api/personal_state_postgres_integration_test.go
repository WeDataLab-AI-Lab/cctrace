package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	tc "github.com/testcontainers/testcontainers-go"
	pgmod "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"cctrace/internal/auth"
	"cctrace/internal/containertest"
	"cctrace/internal/store"
)

func TestPersonalStatePostgresHTTPRoundTrip(t *testing.T) {
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
	ctr, err := pgmod.Run(ctx, "timescale/timescaledb:latest-pg16",
		pgmod.WithDatabase("testdb"), pgmod.WithUsername("test"), pgmod.WithPassword("test"),
		tc.WithCmdArgs("-c", "synchronous_commit=off", "-c", "full_page_writes=off"),
		tc.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second)))
	if err != nil {
		containertest.SkipOrFail(t, "postgres container", err)
	}
	t.Cleanup(func() {
		if err := ctr.Terminate(context.Background()); err != nil {
			t.Errorf("terminate test container: %v", err)
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
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.BackfillSessionOverviewRollups(ctx); err != nil {
		t.Fatal(err)
	}
	const email, uid, sessionID = "fixture-owner@example.test", "fixture-user", "fixture-outside-page"
	user, err := db.CreateDashboardUser(ctx, &store.DashboardUser{Email: email, PasswordHash: "fixture", Role: "user", CctraceUserID: uid})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSessionRecords(ctx, []*store.SessionRecord{{
		Ts: time.Now().UTC().Truncate(time.Second), SessionID: sessionID, Agent: "codex",
		RecordType: "user", UserID: uid, ProfileEmail: email, ProjectHash: "-fixture-project",
		UUID: "fixture-record", Raw: json.RawMessage(`{"text":"fixture prompt"}`),
	}}); err != nil {
		t.Fatal(err)
	}
	jwt, err := auth.NewJWTManager(strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.GenerateAccessToken(user.ID, email, "user", "", uid, false)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(db, jwt)
	srv.useUserIDAccessControl = true
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	client := httpServer.Client()

	request := func(method, path, body string, authenticated bool, want int, target any) {
		t.Helper()
		req, err := http.NewRequest(method, httpServer.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if authenticated {
			req.AddCookie(&http.Cookie{Name: "cctrace_token", Value: token})
		}
		if method != http.MethodGet {
			req.Header.Set("Origin", httpServer.URL)
			req.Header.Set("X-Requested-With", "XMLHttpRequest")
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s %s status=%d want=%d", method, path, resp.StatusCode, want)
		}
		if target != nil {
			if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
				t.Fatalf("%s %s decode: %v", method, path, err)
			}
		}
	}
	path := "/api/sessions/" + sessionID + "/personal"
	request(http.MethodGet, path, "", false, http.StatusUnauthorized, nil)
	var state store.PersonalSessionState
	request(http.MethodGet, path, "", true, http.StatusOK, &state)
	if state.Revision != 0 || state.Note != nil {
		t.Fatalf("initial state: %+v", state)
	}
	request(http.MethodGet, path+"/overview", "", true, http.StatusNotFound, nil)
	const note = "first line\nprivate detail"
	request(http.MethodPut, path, `{"classification":"working","note":"first line\nprivate detail","expected_revision":0}`, true, http.StatusOK, &state)
	if state.Revision != 1 || state.Note == nil || state.Note.Text != note || state.Classification != "working" {
		t.Fatalf("saved state: %+v", state)
	}
	var reread store.PersonalSessionState
	request(http.MethodGet, path, "", true, http.StatusOK, &reread)
	if reread.Revision != 1 || reread.Note == nil || reread.Note.Text != note {
		t.Fatalf("HTTP reread: %+v", reread)
	}
	fresh, err := store.NewPgStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	persisted, err := fresh.GetPersonalSession(ctx, user.ID, sessionID, "", uid)
	if err != nil || persisted.Revision != 1 || persisted.Note == nil || persisted.Note.Text != note {
		t.Fatalf("independent DB reread: %+v, %v", persisted, err)
	}
	other, err := db.CreateDashboardUser(ctx, &store.DashboardUser{
		Email: "fixture-other@example.test", PasswordHash: "fixture", Role: "user", CctraceUserID: "fixture-other",
	})
	if err != nil {
		t.Fatal(err)
	}
	otherToken, err := jwt.GenerateAccessToken(other.ID, other.Email, "user", "", other.CctraceUserID, false)
	if err != nil {
		t.Fatal(err)
	}
	otherReq, err := http.NewRequest(http.MethodGet, httpServer.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	otherReq.AddCookie(&http.Cookie{Name: "cctrace_token", Value: otherToken})
	otherResp, err := client.Do(otherReq)
	if err != nil {
		t.Fatal(err)
	}
	otherResp.Body.Close()
	if otherResp.StatusCode != http.StatusNotFound {
		t.Fatalf("another owner accessed personal state: status=%d", otherResp.StatusCode)
	}
	const otherSessionID = "fixture-other-session"
	if err := db.InsertSessionRecords(ctx, []*store.SessionRecord{{
		Ts: time.Now().UTC().Truncate(time.Second).Add(time.Second), SessionID: otherSessionID,
		Agent: "codex", RecordType: "user", UserID: other.CctraceUserID,
		ProfileEmail: other.Email, UUID: "fixture-other-record",
		Raw: json.RawMessage(`{"text":"another fixture prompt"}`),
	}}); err != nil {
		t.Fatal(err)
	}
	var global store.PersonalGlobalNote
	request(http.MethodGet, "/api/me/note", "", true, http.StatusOK, &global)
	if global.Revision != 0 || global.Text != "" {
		t.Fatalf("initial global note: %+v", global)
	}
	request(http.MethodPut, "/api/me/note", `{"text":"global private note","expected_revision":0}`, true, http.StatusOK, &global)
	if global.Revision != 1 || global.Text != "global private note" {
		t.Fatalf("saved global note: %+v", global)
	}
	request(http.MethodGet, "/api/me/note", "", true, http.StatusOK, &global)
	if global.Revision != 1 || global.Text != "global private note" {
		t.Fatalf("reread global note: %+v", global)
	}
	storedGlobal, err := fresh.GetPersonalGlobalNote(ctx, user.ID)
	if err != nil || storedGlobal.Revision != 1 || storedGlobal.Text != "global private note" {
		t.Fatalf("independent DB global reread: %+v, %v", storedGlobal, err)
	}
	var conflict struct {
		Current store.PersonalSessionState `json:"current"`
	}
	request(http.MethodPut, path, `{"note":"stale","expected_revision":0}`, true, http.StatusConflict, &conflict)
	if conflict.Current.Revision != 1 || conflict.Current.Note == nil || conflict.Current.Note.Text != note {
		t.Fatalf("conflict did not return current revision: %+v", conflict.Current)
	}
	var page store.PersonalNotePage
	request(http.MethodGet, "/api/me/session-notes?limit=1", "", true, http.StatusOK, &page)
	if len(page.Items) != 1 || page.Items[0].SessionID != sessionID || page.Items[0].FirstLine != "first line" {
		t.Fatalf("note list: %+v", page)
	}
	var overview store.SessionOverview
	request(http.MethodGet, path+"/overview", "", true, http.StatusOK, &overview)
	if overview.SessionID != sessionID || overview.Agent != "codex" || !overview.HasSync || overview.ProjectHash != "-fixture-project" {
		t.Fatalf("actual overview: %+v", overview)
	}
	listPath := "/api/session-overview?limit=2"
	readList := func(accessToken string) []map[string]json.RawMessage {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, httpServer.URL+listPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(&http.Cookie{Name: "cctrace_token", Value: accessToken})
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("session overview list status=%d", resp.StatusCode)
		}
		var rows []map[string]json.RawMessage
		if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
			t.Fatalf("session overview list decode: %v", err)
		}
		return rows
	}
	listed := readList(token)
	if len(listed) != 1 || string(listed[0]["session_id"]) != `"`+sessionID+`"` ||
		string(listed[0]["classification"]) != `"working"` || string(listed[0]["first_line"]) != `"first line"` {
		t.Fatalf("owner list did not include bounded personal values")
	}
	if _, present := listed[0]["note"]; present {
		t.Fatal("owner overview list exposed note body")
	}
	otherList := readList(otherToken)
	if len(otherList) != 1 || string(otherList[0]["session_id"]) != `"`+otherSessionID+`"` {
		t.Fatalf("other user overview list did not retain its own source row")
	}
	if _, present := otherList[0]["classification"]; present {
		t.Fatal("other user overview list exposed absent classification")
	}
	if _, present := otherList[0]["first_line"]; present {
		t.Fatal("other user overview list exposed absent first line")
	}
	admin, err := db.CreateDashboardUser(ctx, &store.DashboardUser{
		Email: "fixture-admin@example.test", PasswordHash: "fixture", Role: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	adminToken, err := jwt.GenerateAccessToken(admin.ID, admin.Email, "admin", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	adminList := readList(adminToken)
	if len(adminList) != 2 {
		t.Fatalf("admin overview list source count=%d", len(adminList))
	}
	seen := map[string]bool{}
	for _, row := range adminList {
		var id string
		if err := json.Unmarshal(row["session_id"], &id); err != nil {
			t.Fatalf("admin session id decode: %v", err)
		}
		seen[id] = true
		if _, present := row["classification"]; present {
			t.Fatal("admin overview list exposed another owner's classification")
		}
		if _, present := row["first_line"]; present {
			t.Fatal("admin overview list exposed another owner's first line")
		}
	}
	if !seen[sessionID] || !seen[otherSessionID] {
		t.Fatal("admin overview list omitted source rows")
	}

	base := time.Now().UTC().Truncate(time.Second)
	for _, fixture := range []struct {
		id string
		ts time.Time
	}{
		{"fixture-unsorted-new", base.Add(3 * time.Second)},
		{"fixture-unsorted-old", base.Add(-2 * time.Second)},
	} {
		if err := db.InsertSessionRecords(ctx, []*store.SessionRecord{{
			Ts: fixture.ts, SessionID: fixture.id, Agent: "codex", RecordType: "user",
			UserID: uid, ProfileEmail: email, UUID: fixture.id + "-record",
			Raw: json.RawMessage(`{"text":"classification fixture"}`),
		}}); err != nil {
			t.Fatal(err)
		}
	}
	const observationID = "fixture-observation-only"
	if _, err := db.IngestSessionObservations(ctx, store.SessionObservationBatch{Snapshots: []*store.SessionSnapshot{{
		SessionID: observationID, UserID: uid, ProfileEmail: email,
		IdentityProvenance: "personal_token", ObservedAt: base,
		Snapshot: json.RawMessage(`{}`),
	}}}); err != nil {
		t.Fatal(err)
	}
	working := "working"
	if _, err := db.PutPersonalSession(ctx, user.ID, observationID, "", uid, &working, nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertEvents(ctx, []*store.OtelEvent{{
		Ts: base, EventName: "api_request", SessionID: observationID,
		UserID: uid, ProfileEmail: email, Agent: "codex",
	}}); err != nil {
		t.Fatal(err)
	}

	readOverview := func(accessToken, endpoint string, target any, want int) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, httpServer.URL+endpoint, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(&http.Cookie{Name: "cctrace_token", Value: accessToken})
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("GET %s status=%d want=%d", endpoint, resp.StatusCode, want)
		}
		if target != nil {
			if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
				t.Fatalf("GET %s decode: %v", endpoint, err)
			}
		}
	}
	assertRows := func(accessToken, query string, ids ...string) {
		t.Helper()
		var rows []map[string]json.RawMessage
		readOverview(accessToken, "/api/session-overview?"+query, &rows, http.StatusOK)
		if len(rows) != len(ids) {
			t.Fatalf("overview query %s rows=%d want=%d", query, len(rows), len(ids))
		}
		for i, id := range ids {
			if string(rows[i]["session_id"]) != `"`+id+`"` {
				t.Fatalf("overview query %s row %d did not match expected session", query, i)
			}
			if _, present := rows[i]["note"]; present {
				t.Fatal("classified overview exposed note body")
			}
		}
	}
	assertCount := func(accessToken, query string, count int, buckets map[string]int, available bool) {
		t.Helper()
		var result struct {
			Count  int            `json:"count"`
			Counts map[string]int `json:"classification_counts"`
		}
		readOverview(accessToken, "/api/session-overview/count?"+query, &result, http.StatusOK)
		if result.Count != count || (result.Counts != nil) != available {
			t.Fatalf("overview count query %s count=%d buckets_available=%t; want=%d/%t", query, result.Count, result.Counts != nil, count, available)
		}
		for classification, want := range buckets {
			if got, present := result.Counts[classification]; !present || got != want {
				t.Fatalf("overview count query %s bucket %s=%d present=%t want=%d", query, classification, got, present, want)
			}
		}
	}
	for _, period := range []string{"", "&since=" + base.Add(-time.Hour).Format(time.RFC3339) + "&until=" + base.Add(time.Hour).Format(time.RFC3339)} {
		assertRows(token, "classification=unsorted&limit=1"+period, "fixture-unsorted-new")
		assertRows(token, "classification=unsorted&limit=1&offset=1"+period, "fixture-unsorted-old")
		assertCount(token, "classification=unsorted"+period, 2, map[string]int{
			"unsorted": 2, "working": 1, "consulting": 0, "later": 0, "done": 0,
		}, true)
		assertRows(token, "classification=working&limit=1"+period, sessionID)
		assertCount(token, "classification=working"+period, 1, map[string]int{
			"unsorted": 2, "working": 1,
		}, true)
		assertRows(token, "classification=done"+period)
		assertCount(token, "classification=done"+period, 0, map[string]int{
			"unsorted": 2, "working": 1, "done": 0,
		}, true)
		assertRows(token, "classification=unsorted&project_hashes=fixture-project"+period)
		assertCount(token, "classification=working&project_hashes=fixture-project"+period, 1, map[string]int{
			"unsorted": 0, "working": 1,
		}, true)
	}
	assertRows(otherToken, "classification=working")
	assertCount(otherToken, "classification=working", 0, map[string]int{"unsorted": 1, "working": 0}, true)
	assertRows(adminToken, "classification=working")
	assertCount(adminToken, "classification=working", 0, nil, false)
	readOverview(token, "/api/session-overview?classification=invalid", nil, http.StatusBadRequest)
	readOverview(token, "/api/session-overview/count?classification=invalid", nil, http.StatusBadRequest)

	request(http.MethodDelete, path, `{"expected_revision":0}`, true, http.StatusConflict, &conflict)
	if conflict.Current.Revision != 1 {
		t.Fatalf("delete conflict current revision=%d", conflict.Current.Revision)
	}
	request(http.MethodDelete, path, `{"expected_revision":1}`, true, http.StatusOK, &state)
	if state.Revision != 2 || state.Note != nil || state.Classification != "unsorted" {
		t.Fatalf("deleted state: %+v", state)
	}
	request(http.MethodGet, path, "", true, http.StatusOK, &reread)
	if reread.Revision != 2 || reread.Note != nil {
		t.Fatalf("deleted HTTP reread: %+v", reread)
	}
	request(http.MethodGet, path+"/overview", "", true, http.StatusNotFound, nil)
	request(http.MethodDelete, "/api/me/note", `{"expected_revision":0}`, true, http.StatusConflict, &struct {
		Current store.PersonalGlobalNote `json:"current"`
	}{})
	request(http.MethodDelete, "/api/me/note", `{"expected_revision":1}`, true, http.StatusOK, &global)
	if global.Revision != 2 || global.Text != "" {
		t.Fatalf("deleted global note: %+v", global)
	}
	request(http.MethodGet, "/api/me/note", "", true, http.StatusOK, &global)
	if global.Revision != 2 || global.Text != "" {
		t.Fatalf("deleted global reread: %+v", global)
	}
	request(http.MethodGet, "/api/me/session-notes?limit=1", "", true, http.StatusOK, &page)
	if len(page.Items) != 0 {
		t.Fatalf("deleted note listed: %+v", page)
	}
	var dbNote string
	if err := db.Pool().QueryRow(ctx, `SELECT note FROM session_personal_state WHERE dashboard_user_id=$1 AND session_id=$2`, user.ID, sessionID).Scan(&dbNote); err != nil && err != pgx.ErrNoRows {
		t.Fatal(err)
	}
	if dbNote != "" {
		t.Fatalf("deleted note still stored")
	}
}
