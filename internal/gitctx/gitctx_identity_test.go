package gitctx

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const issue382RemoteURL = "https://example.com/org/issue-382.git"

func TestResolve_ActiveRootMarksEmptySubpathAuthoritative(t *testing.T) {
	root, _ := issue382GitFixture(t)

	got := Resolve(root)

	if got.RepositoryID != "example.com/org/issue-382" {
		t.Fatalf("RepositoryID = %q, want example.com/org/issue-382", got.RepositoryID)
	}
	if got.RepositoryName != "issue-382" {
		t.Fatalf("RepositoryName = %q, want issue-382", got.RepositoryName)
	}
	if got.GitRemoteURL != issue382RemoteURL {
		t.Fatalf("GitRemoteURL = %q, want %q", got.GitRemoteURL, issue382RemoteURL)
	}
	if got.RepoSubpath != "" {
		t.Fatalf("RepoSubpath = %q, want root", got.RepoSubpath)
	}
	if got.RepositoryIDSource != "resolved" {
		t.Fatalf("RepositoryIDSource = %q, want resolved", got.RepositoryIDSource)
	}
	if !got.RepoSubpathPresent {
		t.Fatal("RepoSubpathPresent = false, want true for an authoritative root")
	}
}

func TestResolve_ActiveWorktreeUsesRootIdentity(t *testing.T) {
	root, worktree := issue382GitFixture(t)

	mainMeta := Resolve(root)
	worktreeMeta := Resolve(worktree)

	if worktreeMeta.RepositoryID != mainMeta.RepositoryID {
		t.Fatalf("worktree RepositoryID = %q, want %q", worktreeMeta.RepositoryID, mainMeta.RepositoryID)
	}
	if worktreeMeta.RepoSubpath != "" || !worktreeMeta.RepoSubpathPresent {
		t.Fatalf("worktree subpath = %q (present=%v), want authoritative empty root subpath", worktreeMeta.RepoSubpath, worktreeMeta.RepoSubpathPresent)
	}
	if worktreeMeta.RepositoryIDSource != "resolved" {
		t.Fatalf("worktree RepositoryIDSource = %q, want resolved", worktreeMeta.RepositoryIDSource)
	}
}

func TestResolve_RealMonorepoSubpathStaysSeparate(t *testing.T) {
	root, _ := issue382GitFixture(t)
	nested := filepath.Join(root, "internal", "store")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	got := Resolve(nested)

	if got.RepositoryID != "example.com/org/issue-382" {
		t.Fatalf("RepositoryID = %q, want example.com/org/issue-382", got.RepositoryID)
	}
	if got.RepoSubpath != "internal/store/" {
		t.Fatalf("RepoSubpath = %q, want internal/store/", got.RepoSubpath)
	}
	if !got.RepoSubpathPresent || got.RepositoryIDSource != "resolved" {
		t.Fatalf("metadata authority = source %q, present %v; want resolved, true", got.RepositoryIDSource, got.RepoSubpathPresent)
	}
}

func TestResolve_DeletedWorktreeIsLowConfidence(t *testing.T) {
	_, worktree := issue382GitFixture(t)
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	got := Resolve(worktree)

	if !strings.HasPrefix(got.RepositoryID, "local:") {
		t.Fatalf("RepositoryID = %q, want local fallback", got.RepositoryID)
	}
	if got.RepositoryIDSource != "fallback" {
		t.Fatalf("RepositoryIDSource = %q, want fallback", got.RepositoryIDSource)
	}
	if got.RepoSubpathPresent {
		t.Fatal("RepoSubpathPresent = true for deleted CWD")
	}
}

func TestResolve_EmptyCwdReturnsUnknownWithoutPII(t *testing.T) {
	got := Resolve("")
	if got != (Context{}) {
		t.Fatalf("Resolve(\"\") = %#v, want zero context", got)
	}
}

func issue382GitFixture(t *testing.T) (root, worktree string) {
	t.Helper()
	root = t.TempDir()
	gitTestCommand(t, root, "init", "-b", "main")
	gitTestCommand(t, root, "config", "user.email", "issue-382@example.test")
	gitTestCommand(t, root, "config", "user.name", "Issue 382")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	gitTestCommand(t, root, "add", "README.md")
	gitTestCommand(t, root, "commit", "-m", "fixture")
	gitTestCommand(t, root, "remote", "add", "origin", issue382RemoteURL)
	worktree = filepath.Join(t.TempDir(), "issue-382-worktree")
	gitTestCommand(t, root, "worktree", "add", "--detach", worktree, "main")
	return root, worktree
}

func gitTestCommand(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, fmt.Sprint(string(out)))
	}
}
