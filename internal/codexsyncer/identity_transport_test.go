package codexsyncer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"cctrace/internal/syncer"
)

func TestCodexSyncer_SyncOnce_SendsResolvedIdentityAuthority(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("codex transport fixture\n"), 0o644); err != nil {
		t.Fatalf("write git fixture: %v", err)
	}
	initGitRepo(t, repo)
	got, wire, srv := codexIdentityCaptureServer(t, false)
	defer srv.Close()

	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	writeCodexFixture(t, sessDir, "rollout-2026-08-26T00-00-00-resolved.jsonl", []string{
		`{"type":"session_meta","timestamp":"2026-08-26T00:00:00.000Z","payload":{"cwd":` + quoteJSON(repo) + `,"model_provider":"openai"}}`,
		`{"type":"turn_context","timestamp":"2026-08-26T00:00:01.000Z","payload":{"cwd":` + quoteJSON(repo) + `,"model":"gpt-5"}}`,
		`{"type":"response_item","timestamp":"2026-08-26T00:00:02.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
	})
	state := newTestState(t)
	if err := state.Save(); err != nil {
		t.Fatalf("save state: %v", err)
	}

	cs := New([]string{codexDir}, "profile@example.com", "codex-user", state, syncer.NewClient(srv.URL, "", ""), nil)
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if got.RepositoryID != "github.com/org/codex-rules" {
		t.Fatalf("repository_id = %q, want github.com/org/codex-rules", got.RepositoryID)
	}
	if got.RepositoryIDSource != "resolved" || !got.RepoSubpathPresent {
		t.Fatalf("identity = source:%q present:%v, want resolved,true", got.RepositoryIDSource, got.RepoSubpathPresent)
	}
	if present, ok := wire["repo_subpath_present"]; !ok || string(present) != "true" {
		t.Fatalf("repo_subpath_present wire value = %s (present=%v), want true", present, ok)
	}
}

func TestCodexSyncer_ReenrichOnce_SendsResolvedIdentityAuthority(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("codex reenrich fixture\n"), 0o644); err != nil {
		t.Fatalf("write git fixture: %v", err)
	}
	initGitRepo(t, repo)
	got, wire, srv := codexIdentityCaptureServer(t, true)
	defer srv.Close()

	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	writeCodexFixture(t, sessDir, "rollout-2026-08-26T00-00-00-reenrich-resolved.jsonl", []string{
		`{"type":"session_meta","timestamp":"2026-08-26T00:00:00.000Z","payload":{"cwd":` + quoteJSON(repo) + `,"model_provider":"openai"}}`,
		`{"type":"turn_context","timestamp":"2026-08-26T00:00:01.000Z","payload":{"cwd":` + quoteJSON(repo) + `,"model":"gpt-5"}}`,
		`{"type":"response_item","timestamp":"2026-08-26T00:00:02.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
	})

	cs := New([]string{codexDir}, "profile@example.com", "codex-user", newTestState(t), syncer.NewClient(srv.URL, "", ""), nil)
	if _, err := cs.ReenrichOnce(context.Background()); err != nil {
		t.Fatalf("ReenrichOnce: %v", err)
	}
	if got.RepositoryID != "github.com/org/codex-rules" {
		t.Fatalf("repository_id = %q, want github.com/org/codex-rules", got.RepositoryID)
	}
	if got.RepositoryIDSource != "resolved" || !got.RepoSubpathPresent {
		t.Fatalf("identity = source:%q present:%v, want resolved,true", got.RepositoryIDSource, got.RepoSubpathPresent)
	}
	if present, ok := wire["repo_subpath_present"]; !ok || string(present) != "true" {
		t.Fatalf("repo_subpath_present wire value = %s (present=%v), want true", present, ok)
	}
}

func TestCodexSyncer_ReenrichOnce_SendsFallbackIdentityForDeletedCWD(t *testing.T) {
	cwd := t.TempDir()
	got, wire, srv := codexIdentityCaptureServer(t, true)
	defer srv.Close()

	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	writeCodexFixture(t, sessDir, "rollout-2026-08-26T00-00-00-deleted.jsonl", []string{
		`{"type":"session_meta","timestamp":"2026-08-26T00:00:00.000Z","payload":{"cwd":` + quoteJSON(cwd) + `,"model_provider":"openai"}}`,
		`{"type":"turn_context","timestamp":"2026-08-26T00:00:01.000Z","payload":{"cwd":` + quoteJSON(cwd) + `,"model":"gpt-5"}}`,
		`{"type":"response_item","timestamp":"2026-08-26T00:00:02.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
	})
	if err := os.RemoveAll(cwd); err != nil {
		t.Fatalf("remove deleted cwd fixture: %v", err)
	}

	cs := New([]string{codexDir}, "profile@example.com", "codex-user", newTestState(t), syncer.NewClient(srv.URL, "", ""), nil)
	if _, err := cs.ReenrichOnce(context.Background()); err != nil {
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

func codexIdentityCaptureServer(t *testing.T, reenrich bool) (*syncer.SyncPayload, map[string]json.RawMessage, *httptest.Server) {
	t.Helper()
	var got syncer.SyncPayload
	wire := make(map[string]json.RawMessage)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sync/capabilities":
			_ = json.NewEncoder(w).Encode(map[string]bool{"reenrich": true})
		case "/api/sync":
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
			if reenrich {
				_ = json.NewEncoder(w).Encode(map[string]int{"inserted": 0, "updated": 1})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]int{"inserted": 1})
			}
		case "/api/project-rules":
			_ = json.NewEncoder(w).Encode(map[string]int{"inserted_rules": 0, "inserted_versions": 0})
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	return &got, wire, srv
}
