package gitctx

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The tests in this file change PATH or HOME with t.Setenv, so none of them can
// run in parallel.

// fakeGit puts a POSIX shell script named git first on PATH. The script body
// runs with the arguments git would have received ("-C", cwd, ...).
//
// It also widens commandTimeout. The first exec of a just-written script can
// take seconds on a loaded machine (measured: 2.00s under the full -race unit
// run), and a fake git that times out turns every case into "uncertain" --
// failing the tests that expect an answer and passing the ones that expect
// uncertainty without testing them.
func fakeGit(t *testing.T, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake git is a POSIX shell script")
	}
	prev := commandTimeout
	commandTimeout = time.Minute
	t.Cleanup(func() { commandTimeout = prev })
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// git's stderr is classified by text, so it has to arrive untranslated. The
// locale is appended rather than the environment replaced: HOME and the git
// config it points at carry safe.directory, and losing them turns every
// repository owned by someone else into a failure.
func TestRunGitResult_appendsCLocaleAndKeepsEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LC_ALL", "ko_KR.UTF-8")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "gitconfig"))
	fakeGit(t, `printf '%s|%s|%s' "$LC_ALL" "$HOME" "$GIT_CONFIG_GLOBAL"`)

	r := runGitResult(t.TempDir(), "status")

	want := "C|" + home + "|" + filepath.Join(home, "gitconfig")
	if !r.ok || r.out != want {
		t.Fatalf("git saw %q (ok=%v), want %q", r.out, r.ok, want)
	}
}

// ceilingTempDir returns a fresh directory that git discovery cannot climb out
// of, so an enclosing checkout never answers for it.
func ceilingTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	return dir
}

func checkedGitInit(t *testing.T, dir string) {
	t.Helper()
	gitTestCommand(t, dir, "init", "-q", "-b", "main")
}

// The classification reads nothing but the outcome of the git command. It is
// handed no path, so no branch of it -- the timeout one least of all -- can
// touch the filesystem again: a stat of a cwd on a hung mount has no deadline
// and would stop the whole pass.
func TestRootLookupDefinitive(t *testing.T) {
	const discovery = "fatal: not a git repository (or any of the parent directories): .git"
	cases := []struct {
		name string
		run  gitRun
		want bool
	}{
		{"git not installed", gitRun{exitCode: -1, notFound: true}, true},
		{"timed out or killed, whatever it had printed", gitRun{exitCode: -1, stderr: discovery}, false},
		{"could not start", gitRun{exitCode: -1}, false},
		{"discovery found no repository", gitRun{exitCode: 128, stderr: discovery}, true},
		{"discovery stopped at a mount point", gitRun{exitCode: 128, stderr: "fatal: not a git repository (or any parent up to mount point /)\nStopping at filesystem boundary (GIT_DISCOVERY_ACROSS_FILESYSTEM not set)."}, true},
		{"bare repository or inside .git", gitRun{exitCode: 128, stderr: "fatal: this operation must be run in a work tree"}, true},
		{"cwd is gone", gitRun{exitCode: 128, stderr: "fatal: cannot change to '/work/gone': No such file or directory"}, true},
		{"cwd is a file", gitRun{exitCode: 128, stderr: "fatal: cannot change to '/work/file': Not a directory"}, true},
		{"cwd not searchable", gitRun{exitCode: 128, stderr: "fatal: cannot change to '/work/a': Permission denied"}, false},
		{"path that quotes another errno", gitRun{exitCode: 128, stderr: "fatal: cannot change to '/work/x': No such file or directory': Permission denied"}, false},
		{"broken gitfile", gitRun{exitCode: 128, stderr: "fatal: not a git repository: /work/a/.git/worktrees/gone"}, false},
		{"broken gitfile whose path quotes discovery", gitRun{exitCode: 128, stderr: "fatal: not a git repository: /work/(or any"}, false},
		{"dubious ownership", gitRun{exitCode: 128, stderr: "fatal: detected dubious ownership in repository at '/work/a'"}, false},
		{"any other fatal", gitRun{exitCode: 128, stderr: "fatal: unable to read config file"}, false},
		{"discovery text under another exit code", gitRun{exitCode: 1, stderr: discovery}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rootLookupDefinitive(tc.run); got != tc.want {
				t.Fatalf("rootLookupDefinitive(%+v) = %v, want %v", tc.run, got, tc.want)
			}
		})
	}
}

// A definitive answer is one a later lookup would repeat: the directory is not a
// repository, or has no origin, or is gone. Holding on any of these would hold
// forever, so each must come back without ErrUncertain and with the same
// Context Resolve returns.
func TestResolveChecked_definitive(t *testing.T) {
	cases := map[string]func(t *testing.T) string{
		"not a repository": func(t *testing.T) string {
			return ceilingTempDir(t)
		},
		"repository without origin": func(t *testing.T) string {
			dir := ceilingTempDir(t)
			checkedGitInit(t, dir)
			return dir
		},
		"missing directory": func(t *testing.T) string {
			return filepath.Join(ceilingTempDir(t), "gone")
		},
		"cwd is a file": func(t *testing.T) string {
			p := filepath.Join(ceilingTempDir(t), "file")
			if err := os.WriteFile(p, nil, 0o644); err != nil {
				t.Fatalf("write file: %v", err)
			}
			return p
		},
		"inside .git": func(t *testing.T) string {
			dir := ceilingTempDir(t)
			checkedGitInit(t, dir)
			return filepath.Join(dir, ".git")
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			cwd := setup(t)
			got, err := ResolveChecked(cwd)
			if err != nil {
				t.Fatalf("ResolveChecked: %v, want a definitive answer", err)
			}
			if got != Resolve(cwd) {
				t.Errorf("ResolveChecked = %+v, Resolve = %+v", got, Resolve(cwd))
			}
			if !strings.HasPrefix(got.RepositoryID, "local:") || got.RepositoryIDSource != "fallback" {
				t.Errorf("identity = %q (%s), want a local fallback", got.RepositoryID, got.RepositoryIDSource)
			}
		})
	}
}

// A machine without git cannot learn anything by asking again either.
func TestResolveChecked_gitNotInstalledIsDefinitive(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	got, err := ResolveChecked(cwd)
	if err != nil {
		t.Fatalf("ResolveChecked: %v, want a definitive answer", err)
	}
	if got.RepositoryID != anonymousLocalID(cwd) {
		t.Errorf("RepositoryID = %q, want the anonymous fallback", got.RepositoryID)
	}
}

// An uncertain lookup is one that did not hear git's answer about this
// directory. It still returns Resolve's fallback Context -- callers that ignore
// the error see no change -- and says, through ErrUncertain, that the fallback
// is not a fact about the directory.
func TestResolveChecked_uncertain(t *testing.T) {
	cases := map[string]string{
		"git killed": `kill -9 $$`,
		"fatal origin lookup": `case "$3" in
rev-parse) [ "$4" = --show-toplevel ] && { printf '%s\n' "$2"; exit 0; }; exit 128 ;;
remote) echo "fatal: unable to read config file" >&2; exit 128 ;;
esac
exit 0`,
		"dubious ownership": `echo "fatal: detected dubious ownership in repository at '$2'" >&2; exit 128`,
		"permission denied": `echo "fatal: cannot change to '$2': Permission denied" >&2; exit 128`,
	}
	for name, script := range cases {
		t.Run(name, func(t *testing.T) {
			fakeGit(t, script)
			cwd := t.TempDir()
			got, err := ResolveChecked(cwd)
			if !errors.Is(err, ErrUncertain) {
				t.Fatalf("ResolveChecked error = %v, want ErrUncertain", err)
			}
			if got != Resolve(cwd) {
				t.Errorf("ResolveChecked = %+v, Resolve = %+v", got, Resolve(cwd))
			}
			if !strings.HasPrefix(got.RepositoryID, "local:") || got.RepositoryIDSource != "fallback" {
				t.Errorf("identity = %q (%s), want the non-empty local fallback", got.RepositoryID, got.RepositoryIDSource)
			}
		})
	}
}

// A git that outlives commandTimeout is killed, and that is uncertain end to
// end: nothing was heard about the directory.
func TestResolveChecked_timeoutIsUncertain(t *testing.T) {
	fakeGit(t, `exec sleep 30`)
	// Short enough that the test does not wait, and correct at any length: a
	// script that has not even started when the bound passes is killed the
	// same way.
	commandTimeout = 100 * time.Millisecond
	if _, err := ResolveChecked(t.TempDir()); !errors.Is(err, ErrUncertain) {
		t.Fatalf("ResolveChecked error = %v, want ErrUncertain", err)
	}
}

// A .git file pointing at a gitdir that is gone is a broken worktree or
// submodule, not a non-repository: git says "not a git repository: <gitdir>",
// without the "(or any" of a discovery that found nothing.
func TestResolveChecked_brokenGitfileIsUncertain(t *testing.T) {
	dir := ceilingTempDir(t)
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+filepath.Join(dir, "missing-gitdir")+"\n"), 0o644); err != nil {
		t.Fatalf("write gitfile: %v", err)
	}
	if _, err := ResolveChecked(dir); !errors.Is(err, ErrUncertain) {
		t.Fatalf("ResolveChecked error = %v, want ErrUncertain", err)
	}
}

func TestResolveChecked_emptyCwd(t *testing.T) {
	got, err := ResolveChecked("  ")
	if err != nil || got != (Context{}) {
		t.Fatalf("ResolveChecked(blank) = %+v, %v; want zero context, nil", got, err)
	}
}
