package codexlog

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// uuidRe matches the UUID suffix in a Codex rollout filename.
// e.g. rollout-2026-04-23T11-30-10-019db82c-66c5-7160-a6a7-b76dc2dd72c5.jsonl
var uuidRe = regexp.MustCompile(`([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.jsonl$`)

// DefaultCodexDir returns the Codex config directory.
// Respects CODEX_CONFIG_DIR environment variable.
func DefaultCodexDir() string {
	if v := os.Getenv("CODEX_CONFIG_DIR"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

// ExpandHome expands a leading "~" element to the current user's home
// directory. Paths that do not start with "~" (or "~/") are returned unchanged,
// as is the input when the home directory cannot be determined.
func ExpandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, "~"+string(os.PathSeparator)) {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

// ResolveScanDirs returns every Codex home directory that should be scanned for
// session JSONL files, in a deterministic order: the default home first, then
// CODEX_HOME, then the caller-supplied extra directories in input order.
//
// CODEX_HOME is added on top of the default home rather than replacing it, so a
// launcher that points Codex at another home does not silently stop collection
// from the default one. A leading "~" is expanded so a configured "~/xhome" is
// not silently dropped. Only paths that exist as directories are returned.
func ResolveScanDirs(extra []string) []string {
	candidates := make([]string, 0, len(extra)+2)
	candidates = append(candidates, DefaultCodexDir())
	if v := os.Getenv("CODEX_HOME"); v != "" {
		candidates = append(candidates, v)
	}
	candidates = append(candidates, extra...)

	seen := make(map[string]struct{}, len(candidates))
	dirs := make([]string, 0, len(candidates))
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		abs, err := filepath.Abs(ExpandHome(c))
		if err != nil {
			continue
		}
		if _, ok := seen[abs]; ok {
			continue
		}
		seen[abs] = struct{}{}
		if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
			continue
		}
		dirs = append(dirs, abs)
	}
	return dirs
}

// FindJSONLFiles returns all Codex session JSONL file paths under codexDir.
// Scans two patterns:
//   - sessions/rollout-*.jsonl          (legacy root-level)
//   - sessions/YYYY/MM/DD/rollout-*.jsonl (date-partitioned)
func FindJSONLFiles(codexDir string) ([]string, error) {
	sessionsDir := filepath.Join(codexDir, "sessions")
	patterns := []string{
		filepath.Join(sessionsDir, "rollout-*.jsonl"),
		filepath.Join(sessionsDir, "*", "*", "*", "rollout-*.jsonl"),
	}

	seen := make(map[string]struct{})
	var result []string
	for _, p := range patterns {
		matches, err := filepath.Glob(p)
		if err != nil {
			continue
		}
		for _, m := range matches {
			abs, err := filepath.Abs(m)
			if err != nil {
				continue
			}
			if _, ok := seen[abs]; !ok {
				seen[abs] = struct{}{}
				result = append(result, abs)
			}
		}
	}
	return result, nil
}

// SessionIDFromPath extracts the session UUID from a Codex rollout filename.
// Returns empty string if no UUID is found.
func SessionIDFromPath(path string) string {
	base := filepath.Base(path)
	m := uuidRe.FindStringSubmatch(base)
	if m == nil {
		return ""
	}
	return m[1]
}
