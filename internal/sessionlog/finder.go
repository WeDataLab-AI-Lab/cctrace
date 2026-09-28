package sessionlog

import (
	"os"
	"path/filepath"
	"strings"
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
