package omosyncer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"cctrace/internal/syncer"
)

func TestOmoSyncer_SyncOnce_SendsResolvedIdentityAuthority(t *testing.T) {
	repo := initOmoIdentityRepo(t)
	srv, captured := newTestServer(t)
	state := newTestState(t)
	if err := state.Save(); err != nil {
		t.Fatalf("save state: %v", err)
	}

	omoDir := t.TempDir()
	sessionID := "019f0000-0000-7000-8000-000000000002"
	writeOmoFixture(t, omoDir, "identity", "2026-08-26T00-00-00-000Z_"+sessionID+".jsonl", []string{
		sessionHeader(repo, "identity session"),
		userMessage("m1", "", "2026-08-26T00:00:01.000Z", "hello"),
	})

	s := New([]string{omoDir}, "profile@example.com", "omo-user", state, syncer.NewClient(srv.URL, "", ""), nil)
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if len(*captured) != 1 {
		t.Fatalf("sync requests = %d, want 1", len(*captured))
	}
	payload := (*captured)[0]
	if payload["repository_id"] != "example.com/org/omo" {
		t.Fatalf("repository_id = %v, want example.com/org/omo", payload["repository_id"])
	}
	if payload["repository_id_source"] != "resolved" {
		t.Fatalf("repository_id_source = %v, want resolved", payload["repository_id_source"])
	}
	if present, ok := payload["repo_subpath_present"].(bool); !ok || !present {
		t.Fatalf("repo_subpath_present = %v (present=%v), want true", payload["repo_subpath_present"], ok)
	}
}

func initOmoIdentityRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("omo identity fixture\n"), 0o644); err != nil {
		t.Fatalf("write git fixture: %v", err)
	}
	runOmoGit(t, repo, "init", "-b", "main")
	runOmoGit(t, repo, "config", "user.email", "omo@example.test")
	runOmoGit(t, repo, "config", "user.name", "OMO Test")
	runOmoGit(t, repo, "add", "README.md")
	runOmoGit(t, repo, "commit", "-m", "identity fixture")
	runOmoGit(t, repo, "remote", "add", "origin", "https://example.com/org/omo.git")
	return repo
}

func runOmoGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, string(out))
	}
}
