package gitctx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// commandTimeout bounds one git command. Var, not const, so tests that run a
// fake git can widen it: starting a freshly written script under a loaded test
// run can take longer than the bound, and a test that times out would pass or
// fail for the wrong reason.
var commandTimeout = 2 * time.Second

// Context describes repository metadata for a working directory.
type Context struct {
	RepositoryRoot string
	RepositoryName string
	GitRemoteURL   string
	RepositoryID   string
	// RepositoryIDSource records how RepositoryID was established. "resolved"
	// means Git resolved a remote repository identity; "fallback" means the
	// value is local-only and must not replace a previously known remote identity.
	// An empty value is retained for compatibility with callers that construct a
	// Context themselves and means unknown/legacy.
	RepositoryIDSource string
	RepoSubpath        string
	// RepoSubpathPresent distinguishes an authoritative Git root (whose value is
	// legitimately empty) from an older or unavailable client that omitted the
	// field altogether.
	RepoSubpathPresent bool
	CommitSHA          string
	Branch             string
}

// ErrUncertain marks a lookup that did not hear git's answer about the
// directory: git timed out or was killed, could not start, or failed in a way
// that says nothing about whether cwd is a repository (a broken gitfile,
// permission denied, dubious ownership). The Context returned with it is the
// same local fallback Resolve returns, and is not a fact about cwd.
var ErrUncertain = errors.New("git lookup uncertain")

// Resolve reads Git metadata for cwd. It returns best-effort values and never
// shells out outside the provided cwd/root.
func Resolve(cwd string) Context {
	ctx, _ := ResolveChecked(cwd)
	return ctx
}

// ResolveChecked is Resolve that also reports whether the identity it returns
// can be trusted. It returns the same Context as Resolve in every case, and an
// error wrapping ErrUncertain when the repository root or origin lookup failed
// for a reason a later lookup might not repeat. A definitive answer -- not a
// repository, no origin, cwd missing or not a directory, git not installed --
// returns a nil error even though its identity is a local fallback.
func ResolveChecked(cwd string) (Context, error) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return Context{}, nil
	}

	ctx := Context{}

	top := runGitResult(cwd, "rev-parse", "--show-toplevel")
	root := top.out
	if !top.ok || root == "" {
		// git not detected — do NOT expose cwd basename (may contain client/project PII).
		// Emit hash-only id so the record is still groupable without leaking the path.
		ctx.RepositoryID = anonymousLocalID(cwd)
		ctx.RepositoryIDSource = "fallback"
		if !top.ok && !rootLookupDefinitive(top) {
			return ctx, uncertainLookup("rev-parse --show-toplevel", top)
		}
		return ctx, nil
	}

	var err error
	ctx.RepositoryRoot = root
	ctx.RepositoryName = filepath.Base(root)
	origin := runGitResult(root, "remote", "get-url", "origin")
	// Exit 2 is git's "no such remote": a repository without an origin.
	if !origin.ok && origin.exitCode != 2 {
		err = uncertainLookup("remote get-url origin", origin)
	}
	rawRemote := origin.out
	// Strip embedded credentials before the URL is ever stored or transmitted.
	ctx.GitRemoteURL = SanitizeRemoteURL(rawRemote)
	ctx.RepositoryID = NormalizeRemoteURL(rawRemote)
	if ctx.RepositoryID == "" {
		ctx.RepositoryID = LocalFallbackID(ctx.RepositoryName, root)
		ctx.RepositoryIDSource = "fallback"
	} else {
		ctx.RepositoryIDSource = "resolved"
		ctx.RepositoryName = RepositoryNameFromID(ctx.RepositoryID)
	}
	ctx.RepoSubpath, ctx.RepoSubpathPresent = runGit(cwd, "rev-parse", "--show-prefix")
	ctx.CommitSHA, _ = runGit(root, "rev-parse", "HEAD")
	ctx.Branch, _ = runGit(root, "branch", "--show-current")
	return ctx, err
}

// rootLookupDefinitive reports whether a failed --show-toplevel is git's answer
// about the directory rather than a failure to ask. Only these are: git is not
// installed, discovery found no repository ("not a git repository (or any ..."),
// the directory is a bare repository or inside .git, or it is gone or is not a
// directory. The gitfile form "not a git repository: <gitdir>" is deliberately
// not one of them: it is a broken worktree or submodule, which a remount or
// repair brings back.
//
// It is given the command's outcome and nothing else. A cwd on a hung network
// mount is what makes git time out, and a stat of that cwd from this process
// has no deadline: it would stop the sync pass, and every agent's collection
// behind it. So a missing cwd is read from git's own stderr (stable, because
// git runs under LC_ALL=C), and a command that did not exit on its own is
// uncertain before its stderr is looked at. The messages are matched at the
// start and end of a line, where the quoted path cannot reach.
func rootLookupDefinitive(r gitRun) bool {
	if r.notFound {
		return true
	}
	if r.exitCode != 128 {
		return false
	}
	for _, line := range strings.Split(r.stderr, "\n") {
		switch {
		case strings.HasPrefix(line, "fatal: not a git repository (or any"),
			line == "fatal: this operation must be run in a work tree":
			return true
		case strings.HasPrefix(line, "fatal: cannot change to "):
			return strings.HasSuffix(line, ": No such file or directory") ||
				strings.HasSuffix(line, ": Not a directory")
		}
	}
	return false
}

func uncertainLookup(step string, r gitRun) error {
	return fmt.Errorf("%w: git %s exited %d", ErrUncertain, step, r.exitCode)
}

// NormalizeRemoteURL returns a stable host/path repository id without schemes,
// auth material, or .git suffix.
func NormalizeRemoteURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	if strings.Contains(raw, "://") {
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			host := strings.ToLower(u.Hostname())
			if port := u.Port(); port != "" {
				host = net.JoinHostPort(host, port)
			}
			return cleanRepoID(host, u.Path)
		}
	}

	if at := strings.LastIndex(raw, "@"); at >= 0 {
		tail := raw[at+1:]
		if colon := strings.Index(tail, ":"); colon > 0 && !strings.Contains(tail[:colon], "/") {
			return cleanRepoID(strings.ToLower(tail[:colon]), tail[colon+1:])
		}
	}

	trimmed := strings.TrimPrefix(raw, "https://")
	trimmed = strings.TrimPrefix(trimmed, "http://")
	trimmed = strings.TrimPrefix(trimmed, "ssh://")
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		return ""
	}
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) != 2 {
		return ""
	}
	return cleanRepoID(strings.ToLower(parts[0]), parts[1])
}

// SanitizeRemoteURL removes any embedded credentials (userinfo) from a git
// remote URL while preserving the scheme, host, and path for display. A
// scp-like SSH address (git@host:path) keeps its user, which is an SSH login
// name rather than a secret. URL-scheme forms (e.g. https://user:token@host/...)
// have the userinfo stripped so access tokens or passwords are never stored or
// transmitted.
func SanitizeRemoteURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	i := strings.Index(raw, "://")
	if i < 0 {
		// scp-like (git@host:path) or bare path — no userinfo credentials.
		return raw
	}
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		u.User = nil
		return u.String()
	}
	// Fallback: Parse failed (e.g. password with reserved chars). Strip the
	// userinfo manually from the authority component, before the first '/'.
	scheme, rest := raw[:i+3], raw[i+3:]
	authority, path := rest, ""
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		authority, path = rest[:slash], rest[slash:]
	}
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		authority = authority[at+1:]
	}
	return scheme + authority + path
}

// LocalFallbackID creates a stable local repository id without exposing the
// full filesystem path.
func LocalFallbackID(repositoryName, root string) string {
	repositoryName = strings.TrimSpace(repositoryName)
	root = strings.TrimSpace(root)
	if repositoryName == "" && root != "" {
		repositoryName = filepath.Base(filepath.Clean(root))
	}
	if repositoryName == "" || root == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(root))
	return "local:" + repositoryName + ":" + hex.EncodeToString(sum[:8])
}

// anonymousLocalID returns a stable id derived only from a hash of the path,
// without embedding any path component as plaintext. Used when git is not
// detected so the cwd basename (which may carry client/project names) is
// never sent to the server.
func anonymousLocalID(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(path))
	return "local:" + hex.EncodeToString(sum[:8])
}

// AllowsRepository reports whether a normalized repository id should be
// collected given an allowlist of id prefixes. An empty allowlist allows
// everything (default behavior). Matching is case-insensitive. A blank
// repository id is rejected when an allowlist is configured, since it cannot
// be confirmed to belong to an allowed repository.
func AllowsRepository(repositoryID string, allowPrefixes []string) bool {
	if len(allowPrefixes) == 0 {
		return true
	}
	id := strings.ToLower(strings.TrimSpace(repositoryID))
	if id == "" {
		return false
	}
	for _, p := range allowPrefixes {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" && strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}

// RepositoryNameFromID extracts the display repo name from a normalized id.
func RepositoryNameFromID(repositoryID string) string {
	repositoryID = strings.TrimSpace(repositoryID)
	if repositoryID == "" {
		return ""
	}
	if strings.HasPrefix(repositoryID, "local:") {
		parts := strings.Split(repositoryID, ":")
		// "local:name:hash" → name. "local:hash" (anonymous) → empty.
		if len(parts) >= 3 {
			return parts[1]
		}
		return ""
	}
	parts := strings.Split(strings.Trim(repositoryID, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

func cleanRepoID(host, path string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "/")
	path = strings.TrimSuffix(path, "/")
	path = strings.TrimSuffix(path, ".git")
	if host == "" || path == "" {
		return ""
	}
	return host + "/" + path
}

// gitRun is the outcome of one git command, with enough of the failure kept to
// tell "git answered" from "git could not be asked".
type gitRun struct {
	out      string
	ok       bool
	exitCode int    // -1 when git did not exit on its own (killed, timed out, not started)
	stderr   string // trimmed; empty on success
	notFound bool   // the git executable is not installed
}

func runGit(cwd string, args ...string) (string, bool) {
	r := runGitResult(cwd, args...)
	return r.out, r.ok
}

func runGitResult(cwd string, args ...string) gitRun {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", cwd}, args...)...)
	// Appended, not replacing the environment: HOME and the git config it points
	// at carry safe.directory. LC_ALL=C keeps stderr untranslated, so a failure
	// can be told apart by its text.
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err == nil {
		return gitRun{out: strings.TrimSpace(string(out)), ok: true}
	}
	r := gitRun{exitCode: -1, notFound: errors.Is(err, exec.ErrNotFound)}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		r.exitCode = ee.ExitCode()
		r.stderr = strings.TrimSpace(string(ee.Stderr))
	}
	return r
}
