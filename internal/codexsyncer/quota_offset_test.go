package codexsyncer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"cctrace/internal/syncer"
)

// quotaFailingServer answers everything but the quota-history route, which gets
// the status the caller asks for.
func quotaFailingServer(t *testing.T, quotaStatus int, quotaHits *atomic.Int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/quota-samples") {
			quotaHits.Add(1)
			w.WriteHeader(quotaStatus)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"inserted":1,"skipped":0}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// codexHomeWithQuotaSession writes a logged-in home holding one session whose
// new bytes carry a rate-limit reading.
func codexHomeWithQuotaSession(t *testing.T) (home, sessionPath string) {
	t.Helper()
	home = t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"tokens":{"account_id":"acct-quota"}}`), 0600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}
	sessDir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessDir, 0755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	sessionPath = writeCodexFixture(t, sessDir, "rollout-2026-08-24T09-00-00-019db82c-66c5-7160-a6a7-b76dc2dd72c5.jsonl", []string{
		`{"type":"session_meta","timestamp":"2026-08-24T09:00:00.000Z","payload":{"cwd":"/Users/alice/myproject","model_provider":"openai","originator":"codex-tui"}}`,
		`{"type":"turn_context","timestamp":"2026-08-24T09:00:01.000Z","payload":{"cwd":"/Users/alice/myproject","model":"gpt-5"}}`,
	})
	return home, sessionPath
}

// appendQuotaReading adds the bytes that carry a rate-limit window. It is appended
// after the first pass because a file already on disk at first sync is skipped --
// installing cctrace does not collect history backwards.
func appendQuotaReading(t *testing.T, sessionPath string) {
	t.Helper()
	f, err := os.OpenFile(sessionPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	defer f.Close()
	lines := `{"type":"response_item","timestamp":"2026-08-24T09:00:02.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}` + "\n" +
		`{"type":"event_msg","timestamp":"2026-08-24T09:00:03.000Z","payload":{"type":"token_count","info":null,"rate_limits":{"primary":{"used_percent":22.0,"window_minutes":10080,"resets_at":1786850006},"secondary":null,"plan_type":"pro"}}}` + "\n"
	if _, err := f.WriteString(lines); err != nil {
		t.Fatalf("append: %v", err)
	}
}

// The rate-limit reading exists only in these bytes: nothing else on disk can
// reconstruct the window the server rejected. Advancing the offset past a failed
// send drops that reading for good and takes it out of ordinary retry too, while
// the session send in the same function has always left the range unacknowledged
// on failure. The two halves of one pass had opposite contracts (#580).
func TestSyncOnce_quotaSendFailureLeavesOffsetUnadvanced(t *testing.T) {
	var quotaHits atomic.Int64
	srv := quotaFailingServer(t, http.StatusInternalServerError, &quotaHits)
	statePath := filepath.Join(t.TempDir(), "state.json")
	state, _ := syncer.LoadState(statePath)
	home, sessionPath := codexHomeWithQuotaSession(t)

	cs := New([]string{home}, "user@example.com", "uid-001", state, syncer.NewClient(srv.URL, "token", ""), nil)
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("first SyncOnce: %v", err)
	}
	settled := state.GetOffset(sessionPath)
	appendQuotaReading(t, sessionPath)

	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if got := quotaHits.Load(); got != 1 {
		t.Fatalf("quota requests = %d, want 1", got)
	}
	if got := state.GetOffset(sessionPath); got != settled {
		t.Fatalf("offset advanced %d -> %d after a failed quota send; the reading is now unreachable", settled, got)
	}

	// The point of holding the offset is that the next pass reads the same bytes
	// again and retries the send.
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("retry SyncOnce: %v", err)
	}
	if got := quotaHits.Load(); got != 2 {
		t.Fatalf("quota requests after retry = %d, want 2", got)
	}

	// And across a restart, which is where the original report measured the loss:
	// the offset is durable, so an in-process retry proves nothing about a daemon
	// that exits between passes.
	reloaded, err := syncer.LoadState(statePath)
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	if got := reloaded.GetOffset(sessionPath); got != settled {
		t.Fatalf("persisted offset = %d, want %d -- a restart would skip the reading", got, settled)
	}
	restarted := New([]string{home}, "user@example.com", "uid-001", reloaded, syncer.NewClient(srv.URL, "token", ""), nil)
	if _, err := restarted.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce after restart: %v", err)
	}
	if got := quotaHits.Load(); got != 3 {
		t.Fatalf("quota requests after restart = %d, want 3", got)
	}
}

// A 404 is not a failed send: an older server has no endpoint to fail. That case
// latches and must keep advancing, or one old server would stall collection.
func TestSyncOnce_quotaUnsupportedStillAdvancesOffset(t *testing.T) {
	var quotaHits atomic.Int64
	srv := quotaFailingServer(t, http.StatusNotFound, &quotaHits)
	statePath := filepath.Join(t.TempDir(), "state.json")
	state, _ := syncer.LoadState(statePath)
	home, sessionPath := codexHomeWithQuotaSession(t)

	cs := New([]string{home}, "user@example.com", "uid-001", state, syncer.NewClient(srv.URL, "token", ""), nil)
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("first SyncOnce: %v", err)
	}
	settled := state.GetOffset(sessionPath)
	appendQuotaReading(t, sessionPath)

	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if got := state.GetOffset(sessionPath); got == settled {
		t.Fatalf("offset held at %d for an unsupported endpoint; an old server would stall collection", got)
	}

	// Latched: the second pass must not ask again.
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("second SyncOnce: %v", err)
	}
	if got := quotaHits.Load(); got != 1 {
		t.Fatalf("quota requests = %d, want 1 (the 404 latches)", got)
	}
}
