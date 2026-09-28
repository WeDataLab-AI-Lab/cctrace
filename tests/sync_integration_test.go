package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"cctrace/internal/api"
	"cctrace/internal/auth"
	"cctrace/internal/store"

	tc "github.com/testcontainers/testcontainers-go"
	pgmod "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"cctrace/internal/containertest"
)

// --- shared container for sync tests ---

var (
	syncPgOnce    sync.Once
	syncPgStore   *store.PgStore
	syncPgInitErr error
)

// inVMDockerSocket is the path Ryuk mounts to reach the Docker API from inside
// a container. It is deliberately NOT the DOCKER_HOST path: under Colima,
// DOCKER_HOST points at a socket on the macOS side
// (~/.colima/default/docker.sock) that cannot be bind-mounted into a container
// running in the VM ("operation not supported"). The daemon inside the VM
// exposes the same API at the standard path, so that is what Ryuk gets.
//
// Setting these to the same value is what previously made Ryuk fail to start,
// which is why the reaper used to be disabled and test containers accumulated.
const inVMDockerSocket = "/var/run/docker.sock"

func ensureSyncDockerHost() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	sock := filepath.Join(home, ".colima", "default", "docker.sock")
	if _, err := os.Stat(sock); err != nil {
		return
	}
	host := os.Getenv("DOCKER_HOST")
	if host == "" {
		os.Setenv("DOCKER_HOST", "unix://"+sock)
	} else if host != "unix://"+sock {
		// Pointing somewhere else entirely (remote daemon, Docker Desktop):
		// leave the reaper mount alone, the default is right for that setup.
		return
	}
	// Set this even when DOCKER_HOST was already exported. The testing guide
	// tells developers to export it, and that path used to skip the override,
	// which put the un-mountable macOS socket back in front of Ryuk.
	if os.Getenv("TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE") == "" {
		os.Setenv("TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE", inVMDockerSocket)
	}
}

func acquireSyncStore(t *testing.T) *store.PgStore {
	t.Helper()
	ensureSyncDockerHost()
	syncPgOnce.Do(func() {
		ctx := context.Background()
		// Migrate() creates the timescaledb extension, so plain postgres cannot
		// run this suite. It used to be pointed at postgres:16-alpine, which made
		// every test here skip on a migration error while the package still
		// reported ok — the tests had not actually run in a long time.
		ctr, err := pgmod.Run(ctx,
			"timescale/timescaledb:latest-pg16",
			pgmod.WithDatabase("syncdb"),
			pgmod.WithUsername("test"),
			pgmod.WithPassword("test"),
			tc.WithWaitStrategy(
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2).
					WithStartupTimeout(60*time.Second),
			),
		)
		if err != nil {
			syncPgInitErr = err
			return
		}
		dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			syncPgInitErr = err
			return
		}
		s, err := store.NewPgStore(ctx, dsn)
		if err != nil {
			syncPgInitErr = err
			return
		}
		if err := s.Migrate(ctx); err != nil {
			syncPgInitErr = err
			return
		}
		syncPgStore = s
	})
	if syncPgInitErr != nil {
		containertest.SkipOrFail(t, "postgres container", syncPgInitErr)
	}
	return syncPgStore
}

func truncateSyncTables(t *testing.T, s *store.PgStore) {
	t.Helper()
	if _, err := s.Pool().Exec(context.Background(), "TRUNCATE session_records"); err != nil {
		t.Fatalf("truncate session_records: %v", err)
	}
}

// --- helpers ---

func newSyncServer(t *testing.T, s *store.PgStore) (*httptest.Server, string) {
	t.Helper()
	jwtMgr, err := auth.NewJWTManager("sync-integration-test-secret-at-least-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwtMgr.GenerateAccessToken(1, "admin@example.com", "admin", "Admin", "", false)
	if err != nil {
		t.Fatal(err)
	}
	srv := api.NewServer(s, jwtMgr)
	return httptest.NewServer(srv.Handler()), token
}

func mustJSON(t *testing.T, v interface{}) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return bytes.NewBuffer(b)
}

// --- tests ---

func TestSyncAndList(t *testing.T) {
	s := acquireSyncStore(t)
	truncateSyncTables(t, s)

	ts, token := newSyncServer(t, s)
	defer ts.Close()

	now := time.Now().UTC().Truncate(time.Millisecond)
	records := []*store.SessionRecord{
		{
			Ts:           now,
			SessionID:    "sess-001",
			RecordType:   "user",
			Model:        "claude-sonnet-4-6",
			ProfileEmail: "alice@example.com",
			Raw:          json.RawMessage(`{"role":"user","content":"hello"}`),
		},
		{
			Ts:           now.Add(time.Second),
			SessionID:    "sess-001",
			RecordType:   "assistant",
			Model:        "claude-sonnet-4-6",
			ProfileEmail: "alice@example.com",
			Raw:          json.RawMessage(`{"role":"assistant","content":"hi there"}`),
		},
		{
			Ts:           now.Add(2 * time.Second),
			SessionID:    "sess-002",
			RecordType:   "user",
			Model:        "claude-opus-4-6",
			ProfileEmail: "bob@example.com",
			Raw:          json.RawMessage(`{"role":"user","content":"world"}`),
		},
	}

	body := mustJSON(t, map[string]interface{}{
		"profile_email": "alice@example.com",
		"project_hash":  "abc123",
		"records":       records,
	})

	resp, err := http.Post(ts.URL+"/api/sync", "application/json", body)
	if err != nil {
		t.Fatalf("POST /api/sync: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var syncResp map[string]int
	if err := json.NewDecoder(resp.Body).Decode(&syncResp); err != nil {
		t.Fatalf("decode sync response: %v", err)
	}
	if syncResp["inserted"] != 3 {
		t.Errorf("expected inserted:3, got %d", syncResp["inserted"])
	}

	// GET /api/sessions should return 3 records
	listReq, err := http.NewRequest(http.MethodGet, ts.URL+"/api/sessions", nil)
	if err != nil {
		t.Fatal(err)
	}
	listReq.AddCookie(&http.Cookie{Name: "cctrace_token", Value: token})
	listResp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatalf("GET /api/sessions: %v", err)
	}
	defer listResp.Body.Close()

	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", listResp.StatusCode)
	}

	var listed []*store.SessionRecord
	if err := json.NewDecoder(listResp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listed) != 3 {
		t.Errorf("expected 3 records, got %d", len(listed))
	}
}

func TestSyncEmpty(t *testing.T) {
	s := acquireSyncStore(t)
	truncateSyncTables(t, s)

	ts, _ := newSyncServer(t, s)
	defer ts.Close()

	body := mustJSON(t, map[string]interface{}{
		"profile_email": "test@example.com",
		"records":       []*store.SessionRecord{},
	})

	resp, err := http.Post(ts.URL+"/api/sync", "application/json", body)
	if err != nil {
		t.Fatalf("POST /api/sync: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var syncResp map[string]int
	if err := json.NewDecoder(resp.Body).Decode(&syncResp); err != nil {
		t.Fatalf("decode sync response: %v", err)
	}
	if syncResp["inserted"] != 0 {
		t.Errorf("expected inserted:0, got %d", syncResp["inserted"])
	}
}

func TestSyncMalformedJSON(t *testing.T) {
	s := acquireSyncStore(t)

	ts, _ := newSyncServer(t, s)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/sync", "application/json", bytes.NewBufferString(`{bad json`))
	if err != nil {
		t.Fatalf("POST /api/sync: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Errorf("expected non-200 status for malformed JSON, got %d", resp.StatusCode)
	}
}
