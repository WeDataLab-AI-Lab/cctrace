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

func TestListSessionsPostgresKeepsRecordsWithNullMessageID(t *testing.T) {
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

	const sessionID = "fixture-p4-task-actions"
	const ownerUID = "p4-null-message-owner"
	const ownerEmail = "p4-null-message@example.test"
	owner, err := db.CreateDashboardUser(ctx, &store.DashboardUser{
		Email: ownerEmail, PasswordHash: "fixture", Role: "user", CctraceUserID: ownerUID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSessionRecords(ctx, []*store.SessionRecord{{
		Ts: time.Now().UTC(), SessionID: sessionID, Agent: "claude", RecordType: "user", UUID: "input-null-message-id",
		UserID: ownerUID, ProfileEmail: ownerEmail, PromptSource: "typed",
		Raw: json.RawMessage(`{"message":{"role":"user","content":"keep this valid input"}}`),
	}}); err != nil {
		t.Fatal(err)
	}
	var messageIDIsNull bool
	if err := db.Pool().QueryRow(ctx, `SELECT message_id IS NULL FROM session_records WHERE session_id=$1 AND uuid=$2`, sessionID, "input-null-message-id").Scan(&messageIDIsNull); err != nil {
		t.Fatal(err)
	}
	if !messageIDIsNull {
		t.Fatal("fixture source row must retain its valid NULL message_id")
	}
	listed, err := db.ListSessionRecords(ctx, store.SessionRecordFilter{SessionID: sessionID, UserID: ownerUID, Limit: 1000, Order: "asc"})
	var listedMessageID string
	if len(listed) > 0 {
		listedMessageID = listed[0].MessageID
	}
	if err != nil || len(listed) != 1 || listedMessageID != "" {
		t.Fatalf("ListSessionRecords rows=%d message_id=%q err=%v", len(listed), listedMessageID, err)
	}

	jwt, err := auth.NewJWTManager(strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.GenerateAccessToken(owner.ID, ownerEmail, "user", "", ownerUID, false)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(db, jwt)
	srv.useUserIDAccessControl = true
	req := httptest.NewRequest(http.MethodGet, "/api/sessions?session_id=fixture-p4-task-actions&lineage=1&limit=1000&offset=0", nil)
	req.AddCookie(&http.Cookie{Name: "cctrace_token", Value: token})
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/sessions status=%d body=%s", response.Code, response.Body.String())
	}
	var body []struct {
		UUID string          `json:"uuid"`
		View json.RawMessage `json:"view"`
		Raw  json.RawMessage `json:"raw"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode session response: %v; body=%s", err, response.Body.String())
	}
	if len(body) != 1 || body[0].UUID != "input-null-message-id" || len(body[0].Raw) == 0 {
		t.Fatalf("full record response = %s", response.Body.String())
	}
	if strings.Contains(response.Body.String(), `"message_id"`) {
		t.Fatalf("unknown message_id was serialized as an identifier: %s", response.Body.String())
	}
	var view struct {
		Kind     string `json:"kind"`
		Role     string `json:"role"`
		Text     string `json:"text"`
		AnchorID string `json:"anchor_id"`
	}
	if err := json.Unmarshal(body[0].View, &view); err != nil {
		t.Fatalf("decode normalized view: %v; view=%s", err, body[0].View)
	}
	if view.Kind != "message" || view.Role != "user" || view.Text != "keep this valid input" || view.AnchorID != "input-null-message-id" {
		t.Fatalf("normalized view = %+v; raw view=%s", view, body[0].View)
	}
}
