package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

	err := runSyncReenrich(claudeDir, "", "", "", false, false)
	if !errors.Is(err, syncer.ErrReenrichUnsupported) {
		t.Fatalf("runSyncReenrich error = %v, want ErrReenrichUnsupported", err)
	}
}

// --include-content says what it is about to do, once per run however many
// files and batches follow, naming the redaction options the content goes out
// under. Without the flag nothing is said, and no content field is sent.
func TestRunSyncReenrich_IncludeContentWarnsOnce(t *testing.T) {
	for _, includeContent := range []bool{false, true} {
		home := useTempSyncHome(t)
		claudeDir := filepath.Join(home, ".claude")
		state, err := syncer.LoadState(syncer.DefaultStatePath())
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"a", "b"} {
			dir := filepath.Join(claudeDir, "projects", "-tmp-"+name)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			// A queued prompt: content, and redacted only by the prompt policy,
			// so under the tool policy set below it reaches the wire exactly
			// when the flag asks for content.
			line := `{"type":"attachment","uuid":"` + name + `-1","timestamp":"2026-10-01T00:00:00Z","sessionId":"s-` + name + `",` +
				`"attachment":{"type":"queued_command","commandMode":"prompt","prompt":"QUEUED-` + name + `"}}` + "\n"
			file := filepath.Join(dir, "s.jsonl")
			if err := os.WriteFile(file, []byte(line), 0o600); err != nil {
				t.Fatal(err)
			}
			state.SetOffset(file, int64(len(line)))
		}
		if err := state.Save(); err != nil {
			t.Fatal(err)
		}
		var mu sync.Mutex
		var bodies []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/sync/capabilities":
				_, _ = w.Write([]byte(`{"reenrich":true}`))
			case "/api/sync":
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				bodies = append(bodies, string(body))
				mu.Unlock()
				_, _ = w.Write([]byte(`{"updated":1}`))
			default:
				_, _ = w.Write([]byte(`{"accounts":[]}`))
			}
		}))
		now := time.Now().UTC()
		if err := profile.Save(&profile.Profile{
			Version:         1,
			User:            profile.UserInfo{ID: "u1", Email: "user@example.com"},
			Server:          profile.ServerInfo{Endpoint: srv.URL, SyncEndpoint: srv.URL, AuthToken: "tok"},
			Options:         profile.ProfileOptions{SyncEnabled: true, RedactToolDetails: true},
			ClaudeConfigDir: claudeDir,
			CreatedAt:       now,
			UpdatedAt:       now,
		}); err != nil {
			t.Fatalf("save profile: %v", err)
		}

		var runErr error
		_, stderr := captureOutput(t, func() { runErr = runSyncReenrich(claudeDir, "", "", "", false, includeContent) })
		srv.Close()
		if runErr != nil {
			t.Fatalf("includeContent=%v: runSyncReenrich: %v", includeContent, runErr)
		}
		mu.Lock()
		sent := strings.Join(bodies, "\n")
		mu.Unlock()
		if len(bodies) != 2 {
			t.Fatalf("includeContent=%v: %d batches, want one per file", includeContent, len(bodies))
		}
		warnings := strings.Count(stderr, "Warning: --include-content")
		if !strings.Contains(sent, `"type":"queued_command"`) {
			t.Errorf("includeContent=%v: the attachment's kind did not reach the wire:\n%s", includeContent, sent)
		}
		if !includeContent {
			if warnings != 0 || strings.Contains(sent, "QUEUED-") {
				t.Errorf("without the flag: %d warnings, prompt sent=%v; want neither", warnings, strings.Contains(sent, "QUEUED-"))
			}
			continue
		}
		if warnings != 1 {
			t.Errorf("warned %d times, want once:\n%s", warnings, stderr)
		}
		for _, want := range []string{"records already on the server", "background task", "session titles and agent names", "redaction was on", "redact_user_prompts=false", "redact_tool_details=true", "never replaced"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("warning does not say %q:\n%s", want, stderr)
			}
		}
		if !strings.Contains(sent, "QUEUED-a") || !strings.Contains(sent, "QUEUED-b") {
			t.Errorf("with the flag: queued prompts not sent:\n%s", sent)
		}
	}
}
