package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/profile"
	"cctrace/internal/syncer"
)

func TestRunSyncReenrich_ReturnsCodexUnsupportedServerError(t *testing.T) {
	home := useTempSyncHome(t)
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(filepath.Join(claudeDir, "projects"), 0o700); err != nil {
		t.Fatalf("mkdir claude dir: %v", err)
	}
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(filepath.Join(codexDir, "sessions"), 0o700); err != nil {
		t.Fatalf("mkdir codex dir: %v", err)
	}
	t.Setenv("CODEX_CONFIG_DIR", codexDir)
	t.Setenv("CCTRACE_CODEX_SYNC", "")

	codexCWD := filepath.ToSlash(home)
	codexLog := strings.Join([]string{
		`{"type":"session_meta","timestamp":"2026-04-23T11:30:10.000Z","payload":{"cwd":"` + codexCWD + `","model_provider":"openai"}}`,
		`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(codexDir, "sessions", "rollout-2026-04-23T11-30-10-cccccccc-0000-0000-0000-000000000001.jsonl"), []byte(codexLog), 0o600); err != nil {
		t.Fatalf("write codex log: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/sync/capabilities" {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/sync" {
			t.Fatal("unsupported server must not receive reenrich POST")
		}
	}))
	defer srv.Close()

	now := time.Now().UTC()
	p := &profile.Profile{
		Version: 1,
		User: profile.UserInfo{
			ID:    "u1",
			Email: "user@example.com",
		},
		Server: profile.ServerInfo{
			Endpoint:     srv.URL,
			SyncEndpoint: srv.URL,
			AuthToken:    "tok",
		},
		Options: profile.ProfileOptions{
			SyncEnabled:      true,
			CodexSyncEnabled: true,
		},
		ClaudeConfigDir: claudeDir,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := profile.Save(p); err != nil {
		t.Fatalf("save profile: %v", err)
	}

	err := runSyncReenrich(claudeDir, "", "", "", false)
	if !errors.Is(err, syncer.ErrReenrichUnsupported) {
		t.Fatalf("runSyncReenrich error = %v, want ErrReenrichUnsupported", err)
	}
}
