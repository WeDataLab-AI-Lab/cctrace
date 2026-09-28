package syncer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSyncer_ReenrichOnce_returnsCapabilityRateLimitError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/sync/capabilities" {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/sync" {
			t.Fatal("rate-limited capability probe must not receive reenrich POST")
		}
	}))
	defer srv.Close()

	cwd := t.TempDir()
	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-repo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	sessionPath := filepath.Join(sessionDir, "session.jsonl")
	line := `{"type":"user","timestamp":"2026-07-06T10:00:00Z","sessionId":"legacy-s1","uuid":"turn-1","cwd":` + strconvQuote(cwd) + `,"message":{"role":"user","content":"hi"}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(line), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	s := New(claudeDir, "user@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)
	_, err = s.ReenrichOnce(context.Background())
	var retryErr *RetryableError
	if !errors.As(err, &retryErr) {
		t.Fatalf("ReenrichOnce error = %v, want RetryableError", err)
	}
	if retryErr.RetryAfter != 7*time.Second {
		t.Fatalf("RetryAfter = %v, want 7s", retryErr.RetryAfter)
	}
}
