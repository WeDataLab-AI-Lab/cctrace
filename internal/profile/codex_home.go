package profile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// NormalizeCodexHome expands ~, resolves existing symlinks to a canonical
// absolute path, and verifies ownership and permissions before storing it.
func NormalizeCodexHome(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if raw == "~" || strings.HasPrefix(raw, "~/") || strings.HasPrefix(raw, "~"+string(os.PathSeparator)) {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", errors.New("cannot resolve current home directory")
		}
		if raw == "~" {
			raw = home
		} else {
			raw = filepath.Join(home, raw[2:])
		}
	}
	if !filepath.IsAbs(raw) {
		return "", fmt.Errorf("codex_home must be an absolute path or start with '~': %s", raw)
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(raw))
	if err != nil {
		return "", errors.New("codex_home does not resolve to an existing directory")
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", errors.New("codex_home is not a directory")
	}
	if err := validateCodexHomeOwnerAndMode(canonical, info); err != nil {
		return "", err
	}
	return canonical, nil
}

// CheckCodexHome rechecks a stored home immediately before using it. A home
// moved or replaced after configuration is unavailable; callers must not fall
// back to CODEX_HOME, CodexDirs, or the process default.
func CheckCodexHome(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("codex_home is not configured as an absolute path")
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return errors.New("codex_home is unavailable")
	}
	if canonical != filepath.Clean(path) {
		return errors.New("codex_home path changed through a symlink")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("codex_home is unavailable or is not a regular directory")
	}
	return validateCodexHomeOwnerAndMode(path, info)
}
