package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSyncOnce_SendsResolvedIdentityMetadata(t *testing.T) {
	repo := transportGitRepo(t, "https://example.com/org/claude-transport.git")
	var got SyncPayload
	srv := transportCaptureServer(t, &got, false)

	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-claude-transport")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := filepath.Join(sessionDir, "session.jsonl")
	line := fmt.Sprintf(`{"type":"user","timestamp":"2026-08-26T00:00:00Z","sessionId":"claude-transport-session","cwd":%s,"message":{"role":"user","content":"hello"}}`, strconvQuote(repo))
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	state.SetOffset(path, 0)
	if err := state.Save(); err != nil {
		t.Fatalf("save state: %v", err)
	}

	s := New(claudeDir, "profile@example.com", "claude-user", state, NewClient(srv.URL, "", ""), nil)
	if _, err := s.syncFile(context.Background(), path, false); err != nil {
		t.Fatalf("syncFile: %v", err)
	}
	if got.RepositoryID != "example.com/org/claude-transport" {
		t.Fatalf("repository_id = %q, want example.com/org/claude-transport", got.RepositoryID)
	}
	if got.RepositoryIDSource != "resolved" || !got.RepoSubpathPresent {
		t.Fatalf("identity = source:%q present:%v, want resolved,true", got.RepositoryIDSource, got.RepoSubpathPresent)
	}
	if got.RepoSubpath != "" {
		t.Fatalf("repo_subpath = %q, want root", got.RepoSubpath)
	}
}

func transportGitRepo(t *testing.T, remote string) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "transport@example.test")
	runGit(t, repo, "config", "user.name", "Transport Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("transport\n"), 0o644); err != nil {
		t.Fatalf("write git fixture: %v", err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "transport fixture")
	runGit(t, repo, "remote", "add", "origin", remote)
	return repo
}

func transportCaptureServer(t *testing.T, got *SyncPayload, reenrich bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sync/capabilities":
			_ = json.NewEncoder(w).Encode(syncCapabilities{Reenrich: true})
		case "/api/sync":
			if err := json.NewDecoder(r.Body).Decode(got); err != nil {
				t.Errorf("decode sync request: %v", err)
			}
			if reenrich {
				_ = json.NewEncoder(w).Encode(syncResponse{Updated: 1})
			} else {
				_ = json.NewEncoder(w).Encode(syncResponse{Inserted: 1})
			}
		case "/api/project-rules":
			_ = json.NewEncoder(w).Encode(map[string]int{"inserted_rules": 0})
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}
