package syncer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestReenrichFile_SendsResolvedIdentityAuthority(t *testing.T) {
	repo := transportGitRepo(t, "https://example.com/org/claude-reenrich.git")
	got, wire, srv := reenrichIdentityCaptureServer(t)
	defer srv.Close()

	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-claude-reenrich")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	sessionPath := filepath.Join(sessionDir, "session.jsonl")
	line := `{"type":"user","timestamp":"2026-08-26T00:00:00Z","sessionId":"reenrich-resolved","uuid":"turn-1","cwd":` + strconvQuote(repo) + `,"message":{"role":"user","content":"hello"}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(line), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	s := New(claudeDir, "profile@example.com", "claude-user", state, NewClient(srv.URL, "", ""), nil)
	if _, err := s.ReenrichOnce(context.Background()); err != nil {
		t.Fatalf("ReenrichOnce: %v", err)
	}
	if got.RepositoryID != "example.com/org/claude-reenrich" {
		t.Fatalf("repository_id = %q, want example.com/org/claude-reenrich", got.RepositoryID)
	}
	if got.RepositoryIDSource != "resolved" || !got.RepoSubpathPresent {
		t.Fatalf("identity = source:%q present:%v, want resolved,true", got.RepositoryIDSource, got.RepoSubpathPresent)
	}
	if present, ok := wire["repo_subpath_present"]; !ok || string(present) != "true" {
		t.Fatalf("repo_subpath_present wire value = %s (present=%v), want true", present, ok)
	}
}

func TestReenrichFile_SendsFallbackIdentityForDeletedCWD(t *testing.T) {
	cwd := t.TempDir()
	got, wire, srv := reenrichIdentityCaptureServer(t)
	defer srv.Close()

	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-claude-reenrich-deleted")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	sessionPath := filepath.Join(sessionDir, "session.jsonl")
	line := `{"type":"user","timestamp":"2026-08-26T00:00:00Z","sessionId":"reenrich-deleted","uuid":"turn-1","cwd":` + strconvQuote(cwd) + `,"message":{"role":"user","content":"hello"}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(line), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	if err := os.RemoveAll(cwd); err != nil {
		t.Fatalf("remove deleted cwd fixture: %v", err)
	}
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	s := New(claudeDir, "profile@example.com", "claude-user", state, NewClient(srv.URL, "", ""), nil)
	if _, err := s.ReenrichOnce(context.Background()); err != nil {
		t.Fatalf("ReenrichOnce: %v", err)
	}
	if len(got.RepositoryID) < len("local:") || got.RepositoryID[:len("local:")] != "local:" {
		t.Fatalf("repository_id = %q, want local fallback", got.RepositoryID)
	}
	if got.RepositoryIDSource != "fallback" {
		t.Fatalf("repository_id_source = %q, want fallback", got.RepositoryIDSource)
	}
	present, ok := wire["repo_subpath_present"]
	if !ok {
		t.Fatal("repo_subpath_present missing from fallback wire payload")
	}
	var gotPresent bool
	if err := json.Unmarshal(present, &gotPresent); err != nil {
		t.Fatalf("decode repo_subpath_present: %v", err)
	}
	if gotPresent {
		t.Fatal("repo_subpath_present = true for deleted CWD, want false")
	}
}

func reenrichIdentityCaptureServer(t *testing.T) (*SyncPayload, map[string]json.RawMessage, *httptest.Server) {
	t.Helper()
	var got SyncPayload
	wire := make(map[string]json.RawMessage)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sync/capabilities":
			_ = json.NewEncoder(w).Encode(syncCapabilities{Reenrich: true})
		case "/api/sync":
			if r.Method != http.MethodPost {
				t.Errorf("sync method = %s, want POST", r.Method)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read sync request: %v", err)
				return
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Errorf("decode sync request: %v", err)
			}
			if err := json.Unmarshal(body, &wire); err != nil {
				t.Errorf("decode sync wire: %v", err)
			}
			_ = json.NewEncoder(w).Encode(syncResponse{Updated: 1})
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	return &got, wire, srv
}
