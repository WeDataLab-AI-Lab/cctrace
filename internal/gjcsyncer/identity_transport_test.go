package gjcsyncer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"cctrace/internal/syncer"
)

func TestGjcSyncer_SyncOnce_SendsResolvedIdentityAuthority(t *testing.T) {
	repo := initGjcIdentityRepo(t)
	srv, captured := newTestServer(t)
	state := newTestState(t)
	if err := state.Save(); err != nil {
		t.Fatalf("save state: %v", err)
	}

	gjcDir := t.TempDir()
	sessionID := "019f0000-0000-7000-8000-000000000001"
	writeGjcFixture(t, gjcDir, "identity", sessionID, []string{
		headerLine(sessionID, repo),
		userLine("m1", "", "2026-08-26T00:00:01.000Z", "hello"),
	})

	gs := New([]string{gjcDir}, "profile@example.com", "gjc-user", state, syncer.NewClient(srv.URL, "", ""), nil)
	if _, err := gs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if len(*captured) != 1 {
		t.Fatalf("sync requests = %d, want 1", len(*captured))
	}
	payload := (*captured)[0]
	if payload["repository_id"] != "example.com/org/gjc" {
		t.Fatalf("repository_id = %v, want example.com/org/gjc", payload["repository_id"])
	}
	if payload["repository_id_source"] != "resolved" {
		t.Fatalf("repository_id_source = %v, want resolved", payload["repository_id_source"])
	}
	if present, ok := payload["repo_subpath_present"].(bool); !ok || !present {
		t.Fatalf("repo_subpath_present = %v (present=%v), want true", payload["repo_subpath_present"], ok)
	}
}

func initGjcIdentityRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("gjc identity fixture\n"), 0o644); err != nil {
		t.Fatalf("write git fixture: %v", err)
	}
	runGjcGit(t, repo, "init", "-b", "main")
	runGjcGit(t, repo, "config", "user.email", "gjc@example.test")
	runGjcGit(t, repo, "config", "user.name", "GJC Test")
	runGjcGit(t, repo, "add", "README.md")
	runGjcGit(t, repo, "commit", "-m", "identity fixture")
	runGjcGit(t, repo, "remote", "add", "origin", "https://example.com/org/gjc.git")
	return repo
}

func runGjcGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, string(out))
	}
}
