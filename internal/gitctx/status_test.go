package gitctx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The work tree of a directory is what git names, symlinks resolved. A
// directory git answers about with a failure -- outside any work tree, inside
// a .git directory -- is "" without an error.
func TestWorkTreeRoot(t *testing.T) {
	repo := t.TempDir()
	gitTestCommand(t, repo, "init", "-b", "main")
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := WorkTreeRoot(t.Context(), sub); err != nil || got != real {
		t.Errorf("WorkTreeRoot(sub) = %q, %v; want %q", got, err, real)
	}
	for name, dir := range map[string]string{"outside": t.TempDir(), "inside .git": filepath.Join(repo, ".git")} {
		if got, err := WorkTreeRoot(t.Context(), dir); err != nil || got != "" {
			t.Errorf("WorkTreeRoot(%s) = %q, %v; want no work tree and no error", name, got, err)
		}
	}
}

// A failure git answers with is told from no answer at all.
func TestNoAnswer(t *testing.T) {
	_, notFound := exec.Command(filepath.Join(t.TempDir(), "no-such-git")).Output()
	_, exit128 := exec.Command("git", "-C", filepath.Join(t.TempDir(), "missing"), "status").Output()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, cancelled := exec.CommandContext(ctx, "git", "--version").Output()
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"none", nil, false},
		{"not started", notFound, true},
		{"cancelled", cancelled, true},
		{"deadline", fmt.Errorf("wrapped: %w", context.DeadlineExceeded), true},
		{"exit 128", exit128, false},
		{"other", errors.New("broken pipe"), true},
	} {
		if got := NoAnswer(tc.err); got != tc.want {
			t.Errorf("NoAnswer(%s: %v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}

// Each asked path with a change not yet committed is reported -- modified,
// staged, deleted, untracked, a rename by its destination -- and nothing
// committed and unchanged. Every pathspec is literal: a leading colon or a glob
// character in a name means itself, and a name a glob would match is not
// reported unless asked. Pathspecs past one call's budget go in further calls.
func TestUncommittedPaths(t *testing.T) {
	repo := t.TempDir()
	gitTestCommand(t, repo, "init", "-b", "main")
	gitTestCommand(t, repo, "config", "user.email", "status@example.test")
	gitTestCommand(t, repo, "config", "user.name", "Status Test")
	write := func(name, body string) {
		t.Helper()
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"clean.txt", "modified.txt", "staged.txt", "deleted.txt", "old name.txt", ":colon.txt", "a[1].txt"} {
		write(name, name+"\n")
	}
	gitTestCommand(t, repo, "add", ".")
	gitTestCommand(t, repo, "commit", "-m", "fixture")
	write("modified.txt", "changed\n")
	write("staged.txt", "changed\n")
	gitTestCommand(t, repo, "add", "staged.txt")
	if err := os.Remove(filepath.Join(repo, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, repo, "mv", "old name.txt", "new name.txt")
	write("dir/untracked.txt", "new\n")
	write(":colon.txt", "changed\n")
	write("a[1].txt", "changed\n")
	write("a1.txt", "a glob would match me\n")
	var many []string
	for i := 0; i < 300; i++ {
		name := fmt.Sprintf("many/%03d-%s.txt", i, strings.Repeat("n", 80))
		write(name, "x\n")
		many = append(many, name)
	}

	asked := append([]string{"clean.txt", "modified.txt", "staged.txt", "deleted.txt", "old name.txt", "new name.txt",
		"dir/untracked.txt", ":colon.txt", "a[1].txt"}, many...)
	got, err := UncommittedPaths(t.Context(), repo, asked)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"modified.txt": true, "staged.txt": true, "deleted.txt": true, "new name.txt": true,
		"dir/untracked.txt": true, ":colon.txt": true, "a[1].txt": true}
	for _, name := range many {
		want[name] = true
	}
	if len(got) != len(want) {
		t.Errorf("%d paths reported, want %d", len(got), len(want))
	}
	for path := range want {
		if !got[path] {
			t.Errorf("UncommittedPaths lacks %q", path)
		}
	}
	for _, path := range []string{"clean.txt", "old name.txt", "a1.txt"} {
		if got[path] {
			t.Errorf("UncommittedPaths reports %q", path)
		}
	}
	if got, err := UncommittedPaths(t.Context(), repo, nil); err != nil || len(got) != 0 {
		t.Errorf("asking about nothing: %v, %v", got, err)
	}
	if _, err := UncommittedPaths(t.Context(), filepath.Join(repo, "missing"), []string{"x"}); err == nil || NoAnswer(err) {
		t.Errorf("a status git refused: err %v, want a failure git answered with", err)
	}
}

// A git call returns soon after its deadline even when a process it started
// keeps its output open past it.
func TestGitCallReturnsWhenAChildKeepsItsOutputOpen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in git is a shell script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\nsleep 20 &\nsleep 20\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	start := time.Now()
	_, err := gitOutput(t.Context(), t.TempDir(), "status")
	if elapsed := time.Since(start); err == nil || elapsed > 10*time.Second {
		t.Errorf("the call returned after %s with err %v; want an error soon after its deadline", elapsed.Round(time.Millisecond), err)
	}
}

// RepositoryID is the id Resolve gives a work tree -- its origin's, or the
// local fallback without one -- and an error git gave no answer with.
func TestRepositoryID(t *testing.T) {
	withOrigin, without := t.TempDir(), t.TempDir()
	gitTestCommand(t, withOrigin, "init", "-b", "main")
	gitTestCommand(t, withOrigin, "remote", "add", "origin", "https://github.com/org/repo.git")
	gitTestCommand(t, without, "init", "-b", "main")
	for _, dir := range []string{withOrigin, without} {
		root, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := RepositoryID(t.Context(), root); err != nil || got == "" || got != Resolve(root).RepositoryID {
			t.Errorf("RepositoryID(%s) = %q, %v; want Resolve's %q", filepath.Base(root), got, err, Resolve(root).RepositoryID)
		}
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := RepositoryID(t.Context(), withOrigin); !NoAnswer(err) {
		t.Errorf("without git: err %v, want no answer", err)
	}
}
