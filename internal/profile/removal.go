package profile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Missing external homes remain removable. Resolve their existing ancestry,
// while refusing unreadable paths and broken or cyclic symlinks.
func resolveRemovalPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", ErrUnsafe
	}
	current := filepath.Clean(path)
	var suffix []string
	for {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", ErrUnsafe
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func removalWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return true
	}
	anchor, err := os.Stat(root)
	if err != nil {
		return false
	}
	for current := path; ; current = filepath.Dir(current) {
		if info, err := os.Stat(current); err == nil && os.SameFile(anchor, info) {
			return true
		}
		if filepath.Dir(current) == current {
			return false
		}
	}
}
