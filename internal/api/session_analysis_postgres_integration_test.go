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

	tc "github.com/testcontainers/testcontainers-go"
	pgmod "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"cctrace/internal/auth"
	"cctrace/internal/containertest"
	"cctrace/internal/store"
)

func TestSessionAnalysisPostgresHTTP(t *testing.T) {
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

	const ownerUID, ownerEmail = "analysis-owner", "analysis-owner@example.test"
	const otherUID, otherEmail = "analysis-other", "analysis-other@example.test"
	owner, err := db.CreateDashboardUser(ctx, &store.DashboardUser{Email: ownerEmail, PasswordHash: "fixture", Role: "user", CctraceUserID: ownerUID})
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateDashboardUser(ctx, &store.DashboardUser{Email: otherEmail, PasswordHash: "fixture", Role: "user", CctraceUserID: otherUID})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := db.CreateDashboardUser(ctx, &store.DashboardUser{Email: "analysis-admin@example.test", PasswordHash: "fixture", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	jwt, err := auth.NewJWTManager(strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	ownerToken, err := jwt.GenerateAccessToken(owner.ID, ownerEmail, "user", "", ownerUID, false)
	if err != nil {
		t.Fatal(err)
	}
	otherToken, err := jwt.GenerateAccessToken(other.ID, otherEmail, "user", "", otherUID, false)
	if err != nil {
		t.Fatal(err)
	}
	adminToken, err := jwt.GenerateAccessToken(admin.ID, admin.Email, "admin", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(db, jwt)
	srv.useUserIDAccessControl = true
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	client := httpServer.Client()
	read := func(token, id, query string, want int) map[string]json.RawMessage {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, httpServer.URL+"/api/sessions/"+id+"/analysis"+query, nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.AddCookie(&http.Cookie{Name: "cctrace_token", Value: token})
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("analysis HTTP status for %s: got %d want %d", id, resp.StatusCode, want)
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("analysis JSON decode for %s: %v", id, err)
		}
		return body
	}
	state := func(body map[string]json.RawMessage, want string) {
		t.Helper()
		if string(body["state"]) != `"`+want+`"` {
			t.Fatalf("analysis state=%s want=%s", body["state"], want)
		}
	}
	absent := func(body map[string]json.RawMessage, keys ...string) {
		t.Helper()
		for _, key := range keys {
			if _, ok := body[key]; ok {
				t.Fatalf("analysis response exposed %s", key)
			}
		}
	}

	const fullID, pendingID, unsupportedID, mixedID = "analysis-full-owner", "analysis-building-owner", "analysis-unsupported", "analysis-mixed-identity"
	base := time.Now().UTC().Truncate(time.Second)
	inputTokens := 42
	insertRecord := func(id, uid, email, agent, uuid string, at time.Time, raw string) {
		t.Helper()
		if err := db.InsertSessionRecords(ctx, []*store.SessionRecord{{
			Ts: at, SessionID: id, Agent: agent, RecordType: "user", UUID: uuid,
			UserID: uid, ProfileEmail: email, Raw: json.RawMessage(raw),
		}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.InsertSessionRecords(ctx, []*store.SessionRecord{{
		Ts: base, SessionID: fullID, Agent: "claude", RecordType: "user", UUID: "analysis-user-1",
		UserID: ownerUID, ProfileEmail: ownerEmail, PromptSource: "typed",
		Raw: json.RawMessage(`{"promptId":"analysis-prompt","origin":{"kind":"human"},"message":{"role":"user","content":"synthetic prompt"}}`),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSessionRecords(ctx, []*store.SessionRecord{{
		Ts: base.Add(time.Second), SessionID: fullID, Agent: "claude", RecordType: "assistant",
		UUID: "analysis-question", ParentUUID: "analysis-user-1", UserID: ownerUID, ProfileEmail: ownerEmail,
		InputTokens: &inputTokens,
		Raw:         json.RawMessage(`{"message":{"role":"assistant","content":[{"type":"tool_use","id":"analysis-tool","name":"AskUserQuestion","input":{"questions":[{"header":"Fixture","question":"Choose synthetic option","options":[{"label":"Yes","description":"Synthetic choice"}]}]}}]}}`),
	}}); err != nil {
		t.Fatal(err)
	}
	insertRecord(pendingID, ownerUID, ownerEmail, "claude", "analysis-pending", base, `{"message":{"role":"user","content":"pending"}}`)
	insertRecord(unsupportedID, ownerUID, ownerEmail, "gjc", "analysis-unsupported-record", base, `{"message":{"role":"user","content":"unsupported"}}`)
	insertRecord(mixedID, ownerUID, ownerEmail, "claude", "analysis-mixed-owner", base, `{"message":{"role":"user","content":"mixed"}}`)
	insertRecord(mixedID, otherUID, otherEmail, "claude", "analysis-mixed-foreign", base.Add(time.Second), `{"message":{"role":"user","content":"foreign"}}`)
	if err := db.InsertEvents(ctx, []*store.OtelEvent{{
		Ts: base.Add(2 * time.Second), EventName: "api_request", SessionID: fullID,
		UserID: ownerUID, ProfileEmail: ownerEmail, Agent: "claude", PromptID: "analysis-prompt",
	}}); err != nil {
		t.Fatal(err)
	}

	if got := read(ownerToken, fullID, "", http.StatusNotFound); string(got["error"]) != `"session_analysis_disabled"` {
		t.Fatal("disabled analysis gate did not return explicit 404")
	}
	srv.WithSessionAnalysisEnabled(true)
	read("", fullID, "", http.StatusUnauthorized)
	pending := read(ownerToken, pendingID, "", http.StatusOK)
	state(pending, "building")
	if string(pending["analysis_pending"]) != "true" || string(pending["pending_since"]) == "null" {
		t.Fatalf("first detail request did not return pending status: %v", pending)
	}
	state(read(ownerToken, fullID, "", http.StatusOK), "building")
	state(read(ownerToken, unsupportedID, "", http.StatusOK), "unsupported")
	read(otherToken, fullID, "", http.StatusNotFound)
	read(ownerToken, mixedID, "", http.StatusNotFound)
	state(read(adminToken, mixedID, "", http.StatusOK), "building")
	read(ownerToken, "analysis-absent", "", http.StatusNotFound)

	if _, err := db.Pool().Exec(ctx, `INSERT INTO schema_backfills(name) VALUES ('request_set_v1') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	workerCtx, stopWorker := context.WithCancel(ctx)
	finished := make(chan struct{})
	runs := make(chan store.SessionFactRun, 8)
	workerErrors := make(chan error, 8)
	t.Cleanup(func() {
		stopWorker()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("worker did not stop")
		}
	})
	go func() {
		defer close(finished)
		db.RunSessionFactWorker(workerCtx, func(time.Duration) <-chan time.Time { return time.After(time.Millisecond) }, func(run store.SessionFactRun, err error) {
			if err != nil {
				select {
				case workerErrors <- err:
				default:
				}
			} else if run.SessionID == fullID || run.SessionID == mixedID {
				select {
				case runs <- run:
				default:
				}
			}
		})
	}()
	published := map[string]bool{}
	deadline := time.After(20 * time.Second)
	for len(published) < 2 {
		select {
		case run := <-runs:
			if run.Outcome != "written" {
				t.Fatalf("worker fact for %s outcome=%s want written", run.SessionID, run.Outcome)
			}
			published[run.SessionID] = true
		case err := <-workerErrors:
			t.Fatalf("worker fact publication: %v", err)
		case <-deadline:
			t.Fatal("worker did not publish computed facts")
		}
	}
	stopWorker()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop")
	}
	classification, privateNote := "working", "synthetic private analysis note"
	if _, err := db.PutPersonalSession(ctx, owner.ID, fullID, "", ownerUID, &classification, &privateNote, 0); err != nil {
		t.Fatal(err)
	}
	full := read(ownerToken, fullID, "?profile_email="+otherEmail, http.StatusOK)
	state(full, "ready")
	if string(full["classification"]) != `"working"` {
		t.Fatal("verified owner classification missing")
	}
	var note struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(full["note"], &note); err != nil || note.Text != privateNote {
		t.Fatalf("verified owner note missing: %v", err)
	}
	if string(full["owner_view"]) != "true" {
		t.Fatal("owned analysis did not have owner view")
	}
	var header struct {
		Tokens struct {
			Input int64 `json:"input"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(full["header"], &header); err != nil || header.Tokens.Input != int64(inputTokens) {
		t.Fatalf("computed request-head input tokens=%d err=%v", header.Tokens.Input, err)
	}
	absent(full, "identities")
	for _, key := range []string{"header", "turns", "inputs", "questions", "plans", "recaps", "outputs", "signals", "truncated"} {
		if _, ok := full[key]; !ok {
			t.Fatalf("computed analysis lacks %s", key)
		}
	}
	var questions []struct {
		Items []struct {
			Question string `json:"question"`
		} `json:"items"`
	}
	if err := json.Unmarshal(full["questions"], &questions); err != nil || len(questions) != 1 || len(questions[0].Items) != 1 || questions[0].Items[0].Question != "Choose synthetic option" {
		t.Fatalf("computed question source batch: count=%d err=%v", len(questions), err)
	}
	read(otherToken, fullID, "", http.StatusNotFound)
	read(ownerToken, mixedID, "", http.StatusNotFound)
	mixed := read(adminToken, mixedID, "", http.StatusOK)
	state(mixed, "ready")
	if string(mixed["owner_view"]) != "false" {
		t.Fatal("mixed identity analysis received owner view")
	}
	absent(mixed, "signals", "note", "classification")
	adminBody := read(adminToken, fullID, "", http.StatusOK)
	state(adminBody, "ready")
	if string(adminBody["owner_view"]) != "false" {
		t.Fatal("nonowner admin received owner view")
	}
	absent(adminBody, "signals", "note", "classification")
	if _, ok := adminBody["identities"]; !ok {
		t.Fatal("admin identities missing")
	}
	read(adminToken, fullID, "?user_id="+otherUID, http.StatusNotFound)
	read(adminToken, fullID, "?profile_email="+otherEmail, http.StatusNotFound)
	read(adminToken, fullID, "?login_email="+otherEmail, http.StatusNotFound)

	if _, err := db.Pool().Exec(ctx, `UPDATE session_analysis_facts SET coverage='truncated' WHERE session_id=$1`, fullID); err != nil {
		t.Fatal(err)
	}
	truncated := read(ownerToken, fullID, "", http.StatusOK)
	state(truncated, "truncated")
	absent(truncated, "turns", "questions", "plans", "recaps", "signals", "identities")
}
