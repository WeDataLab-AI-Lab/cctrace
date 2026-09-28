package gjclog

import (
	"os"
	"path/filepath"
	"regexp"
)

// uuidRe matches the trailing UUID in a gjc session filename, e.g.
// 2026-01-01T16-17-22-275Z_019f0000-0000-7000-8000-000000000001.jsonl
var uuidRe = regexp.MustCompile(`([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.jsonl$`)

// dirUUIDRe matches the UUID that identifies a subagent artifact directory.
// The directory is named after the parent session file minus ".jsonl", either
// bare (2026-01-01T16-17-22-275Z_019f0000-0000-7000-8000-000000000001) or with
// an "-artifacts" suffix (...-000000000001-artifacts), per design doc section 4.1.
var dirUUIDRe = regexp.MustCompile(`([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})(?:-artifacts)?$`)

// DefaultGjcDir returns the gjc config directory, $HOME/.gjc. Unlike Codex,
// gjc documents no environment variable override.
func DefaultGjcDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gjc")
}

// FindSessionFiles returns all gjc main session JSONL file paths under gjcDir.
// Each file lives directly under a v2-<base32> project directory:
// agent/sessions/v2-*/*.jsonl.
func FindSessionFiles(gjcDir string) ([]string, error) {
	pattern := filepath.Join(gjcDir, "agent", "sessions", "v2-*", "*.jsonl")
	return globAbs(pattern)
}

// FindSubagentFiles returns all gjc subagent transcript JSONL file paths
// under gjcDir. Subagent transcripts live one level deeper than the main
// session file, inside an artifact directory named like the parent session
// file minus ".jsonl": agent/sessions/v2-*/<ts>_<uuid>/*.jsonl.
func FindSubagentFiles(gjcDir string) ([]string, error) {
	pattern := filepath.Join(gjcDir, "agent", "sessions", "v2-*", "*", "*.jsonl")
	return globAbs(pattern)
}

func globAbs(pattern string) ([]string, error) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(matches))
	for _, m := range matches {
		abs, err := filepath.Abs(m)
		if err != nil {
			continue
		}
		result = append(result, abs)
	}
	return result, nil
}

// SessionIDFromPath extracts the session UUID from a gjc session filename
// (<ts>_<uuid>.jsonl). Returns empty string if no UUID is found.
func SessionIDFromPath(path string) string {
	base := filepath.Base(path)
	m := uuidRe.FindStringSubmatch(base)
	if m == nil {
		return ""
	}
	return m[1]
}

// ParentSessionIDFromSubagentPath extracts the parent session UUID from a
// subagent transcript path. The parent session id is the UUID in the
// artifact directory name (the transcript file's parent directory), not the
// transcript's own filename, which is an arbitrary subagentId.
func ParentSessionIDFromSubagentPath(path string) string {
	dir := filepath.Base(filepath.Dir(path))
	m := dirUUIDRe.FindStringSubmatch(dir)
	if m == nil {
		return ""
	}
	return m[1]
}

// TokenLogPath deterministically derives the project-local token-log path
// for a given cwd (the directory gjc was started from) and session id.
func TokenLogPath(cwd, sessionID string) string {
	return filepath.Join(cwd, ".gjc", "_session-"+sessionID, "token-logs", "token-log.jsonl")
}
