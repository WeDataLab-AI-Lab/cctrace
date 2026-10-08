package sessionlog

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// DefaultClaudeDir returns the default Claude config directory.
func DefaultClaudeDir() string {
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// FindJSONLFiles returns all session JSONL file paths under claudeDir.
// It scans:
//   - projects/{project}/*.jsonl           (main sessions)
//   - projects/{project}/subagents/*.jsonl (old subagent path)
//   - projects/{project}/*/*.jsonl         (some nested layouts)
func FindJSONLFiles(claudeDir string) ([]string, error) {
	patterns := []string{
		filepath.Join(claudeDir, "projects", "*", "*.jsonl"),
		filepath.Join(claudeDir, "projects", "*", "subagents", "*.jsonl"),
		filepath.Join(claudeDir, "projects", "*", "*", "subagents", "*.jsonl"),
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

// FindJSONLFilesStrict returns the files FindJSONLFiles returns, or an error
// when it cannot be sure it has them all.
//
// FindJSONLFiles is built on filepath.Glob, which ignores I/O errors: a
// directory that cannot be read contributes no matches and no error. That is
// right for a sync pass -- the files are picked up when the directory is
// readable again -- and wrong for a caller that must act on every session file
// at one moment and would otherwise take a partial list for the whole. This
// walks the same three layouts with os.ReadDir and reports the first directory
// it cannot read. A projects directory that does not exist is an empty list.
func FindJSONLFilesStrict(claudeDir string) ([]string, error) {
	projects, err := filepath.Abs(filepath.Join(claudeDir, "projects"))
	if err != nil {
		return nil, err
	}
	_, projectDirs, err := readSessionDir(projects)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result []string
	for _, project := range projectDirs {
		// projects/{project}/*.jsonl
		files, children, err := readSessionDir(project)
		if err != nil {
			return nil, err
		}
		result = append(result, files...)
		for _, child := range children {
			// projects/{project}/subagents/*.jsonl
			if filepath.Base(child) == "subagents" {
				files, _, err := readSessionDir(child)
				if err != nil {
					return nil, err
				}
				result = append(result, files...)
			}
			// projects/{project}/*/subagents/*.jsonl
			nested := filepath.Join(child, "subagents")
			if isDir, err := isDirectory(nested); err != nil {
				return nil, err
			} else if isDir {
				files, _, err := readSessionDir(nested)
				if err != nil {
					return nil, err
				}
				result = append(result, files...)
			}
		}
	}
	return result, nil
}

// readSessionDir reads dir and returns the paths of its *.jsonl entries and of
// its subdirectories.
func readSessionDir(dir string) (jsonl, dirs []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if strings.HasSuffix(e.Name(), ".jsonl") {
			jsonl = append(jsonl, p)
		}
		isDir := e.IsDir()
		if e.Type()&fs.ModeSymlink != 0 {
			// Glob follows a symlinked directory, so this does too.
			if isDir, err = isDirectory(p); err != nil {
				return nil, nil, err
			}
		}
		if isDir {
			dirs = append(dirs, p)
		}
	}
	return jsonl, dirs, nil
}

// isDirectory reports whether path is a directory, following symlinks. A path
// that does not exist is not one, and neither is a symlink that leads nowhere
// -- dangling, or looping back on itself: nothing can be listed through it, so
// it is no more a missed directory than a regular file is. Any other failure
// to find out (permission, I/O) is an error. The loop is recognised by ELOOP,
// which is what POSIX systems report; Windows reports a link loop differently,
// so there it is still an error.
func isDirectory(path string) (bool, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.ELOOP) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return fi.IsDir(), nil
}

// ProjectHash extracts the project hash directory name from a JSONL file path.
// e.g. /home/user/.claude/projects/-home-user-myapp/session.jsonl → "-home-user-myapp"
func ProjectHash(claudeDir, filePath string) string {
	projectsDir := filepath.Join(claudeDir, "projects")
	rel, err := filepath.Rel(projectsDir, filePath)
	if err != nil {
		return ""
	}
	// First segment is the project hash
	parts := strings.SplitN(filepath.ToSlash(rel), "/", 2)
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}
