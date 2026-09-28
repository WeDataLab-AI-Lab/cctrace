package codexsyncer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cctrace/internal/syncer"
)

func TestCodexSyncer_ReenrichOnce_ReturnsServerError(t *testing.T) {
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/sync/capabilities" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]bool{"reenrich": true})
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/sync" {
			posts++
			http.Error(w, "unavailable", http.StatusInternalServerError)
			return
		}
		t.Fatalf("unexpected request = %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	writeCodexFixture(t, sessDir, "rollout-2026-04-23T11-30-10-cccccccc-0000-0000-0000-000000000001.jsonl", []string{
		`{"type":"session_meta","timestamp":"2026-04-23T11:30:10.000Z","payload":{"cwd":"/Users/alice/myproject","model_provider":"openai"}}`,
		`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
	})

	cs := New([]string{codexDir}, "user@example.com", "uid-001", newTestState(t), syncer.NewClient(srv.URL, "", ""), nil)
	_, err := cs.ReenrichOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "server returned 500") {
		t.Fatalf("ReenrichOnce error = %v, want server returned 500", err)
	}
	if posts != 1 {
		t.Fatalf("reenrich POSTs = %d, want 1", posts)
	}
}
