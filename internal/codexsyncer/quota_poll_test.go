package codexsyncer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"cctrace/internal/store"
	"cctrace/internal/syncer"
)

// codexHomeWithAccount writes the auth.json a logged-in Codex home has.
func codexHomeWithAccount(t *testing.T, accountID string) string {
	t.Helper()
	dir := t.TempDir()
	body := `{"tokens":{"account_id":"` + accountID + `"}}`
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(body), 0600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}
	return dir
}

// recordingServer captures the quota rows the syncer posts.
func recordingServer(t *testing.T) (*httptest.Server, *atomic.Value, *atomic.Int64) {
	t.Helper()
	var last atomic.Value
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var rows []*store.QuotaSample
		_ = json.NewDecoder(r.Body).Decode(&rows)
		last.Store(rows)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &last, &hits
}

// A home that has never logged in has no account, so the reading cannot be
// filed. It must cost nothing rather than being posted unattributed.
func TestPollAppServerQuota_skipsHomeWithNoAccount(t *testing.T) {
	srv, _, hits := recordingServer(t)
	s := New([]string{t.TempDir()}, "me@example.test", "u1", &syncer.State{},
		syncer.NewClient(srv.URL, "token", ""), nil)

	s.PollAppServerQuota(context.Background())

	if got := hits.Load(); got != 0 {
		t.Fatalf("%d requests issued, want 0", got)
	}
}

// Once the server has said it has no history route, asking again every interval
// for the life of the daemon is pure waste — and spawning a process to build
// the request first is worse than waste.
func TestPollAppServerQuota_stopsOnceUnsupported(t *testing.T) {
	srv, _, hits := recordingServer(t)
	s := New([]string{codexHomeWithAccount(t, "acct-1")}, "me@example.test", "u1", &syncer.State{},
		syncer.NewClient(srv.URL, "token", ""), nil)
	s.quotaUnsupported = true

	s.PollAppServerQuota(context.Background())

	if got := hits.Load(); got != 0 {
		t.Fatalf("%d requests issued, want 0", got)
	}
}

// No configured home is not an error, and must not reach for a process.
func TestPollAppServerQuota_noHomesIsQuiet(t *testing.T) {
	srv, _, hits := recordingServer(t)
	s := New(nil, "me@example.test", "u1", &syncer.State{},
		syncer.NewClient(srv.URL, "token", ""), nil)

	s.PollAppServerQuota(context.Background())

	if got := hits.Load(); got != 0 {
		t.Fatalf("%d requests issued, want 0", got)
	}
}
