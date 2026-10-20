package localapi

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// findFiles does not descend through symlinked directories, including a symlinked
// projects/sessions directory. The provider finders follow those links; this
// local HTTP surface must not even enumerate a configured root's exterior.
func findFiles(rootDir, agent string) ([]string, error) {
	base := "projects"
	if agent == "codex" {
		base = "sessions"
	}
	dir := filepath.Join(rootDir, base)
	var files []string
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && path == dir {
				return nil
			}
			return err
		}
		rel, e := filepath.Rel(dir, path)
		if e != nil {
			return e
		}
		if rel == "." {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if entry.IsDir() {
			if agent == "claude" {
				if len(parts) > 3 || len(parts) == 3 && parts[2] != "subagents" {
					return filepath.SkipDir
				}
			}
			if agent == "codex" {
				if len(parts) > 3 {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		if agent == "claude" && (len(parts) == 2 || len(parts) == 3 && parts[1] == "subagents" || len(parts) == 4 && parts[2] == "subagents") {
			files = append(files, path)
		}
		if agent == "codex" && strings.HasPrefix(entry.Name(), "rollout-") && (len(parts) == 1 || len(parts) == 4) {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}
