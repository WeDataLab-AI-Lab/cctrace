package gitctx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const commandTimeout = 2 * time.Second

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

// Resolve reads Git metadata for cwd. It returns best-effort values and never
// shells out outside the provided cwd/root.
func Resolve(cwd string) Context {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return Context{}
	}

	ctx := Context{}

	root, ok := runGit(cwd, "rev-parse", "--show-toplevel")
	if !ok || root == "" {
		// git not detected — do NOT expose cwd basename (may contain client/project PII).
		// Emit hash-only id so the record is still groupable without leaking the path.
		ctx.RepositoryID = anonymousLocalID(cwd)
		ctx.RepositoryIDSource = "fallback"
		return ctx
	}

	ctx.RepositoryRoot = root
	ctx.RepositoryName = filepath.Base(root)
	rawRemote, _ := runGit(root, "remote", "get-url", "origin")
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
	return ctx
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

func runGit(cwd string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", cwd}, args...)...).Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}
