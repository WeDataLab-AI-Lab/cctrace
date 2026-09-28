package projectrule

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"cctrace/internal/store"
)

const (
	AgentClaude = "claude"
	AgentCodex  = "codex"

	StatusActive     = "active"
	StatusMissing    = "missing"
	StatusUnreadable = "unreadable"

	defaultMaxFileBytes = 1024 * 1024
)

// ignoredDirs are skipped during the rule-file walk. Rule files
// (CLAUDE.md/AGENTS.md) never live in these, and descending into them on a
// 30s rescan cadence is the dominant cost on large/monorepo trees.
var ignoredDirs = map[string]struct{}{
	".git":          {},
	".hg":           {},
	".svn":          {},
	".next":         {},
	".nuxt":         {},
	".turbo":        {},
	".cache":        {},
	"node_modules":  {},
	"vendor":        {},
	"dist":          {},
	"build":         {},
	"out":           {},
	"target":        {},
	"tmp":           {},
	"bin":           {},
	"obj":           {},
	"coverage":      {},
	"__pycache__":   {},
	".venv":         {},
	"venv":          {},
	".tox":          {},
	".mypy_cache":   {},
	".pytest_cache": {},
	".gradle":       {},
	".terraform":    {},
	".idea":         {},
	".vscode":       {},
	".svelte-kit":   {},
	".parcel-cache": {},
	".pnpm-store":   {},
	// Interpreter/toolchain caches. These dwarf node_modules on data-science
	// checkouts — a live walk was caught inside
	// <repo>/.conda/lib/python3.10/site-packages/tensorflow/include/... — and,
	// like the entries above, never hold a repository's own rule files.
	".conda":        {},
	"site-packages": {},
	".cargo":        {},
	".rustup":       {},
	".npm":          {},
	".pnpm":         {},
	".gem":          {},
	".pyenv":        {},
	".nvm":          {},
	"Pods":          {},
	"DerivedData":   {},
}

// ScanOptions controls repository rule-file discovery.
type ScanOptions struct {
	Agent              string
	RepositoryRoot     string
	IncludeMissingRoot bool
	MaxFileBytes       int64
}

// Scan discovers agent rule files under a repository root and converts them to
// ProjectRuleSnapshot payloads for the sync API.
//
// Cancellation contract: ctx is checked once before any filesystem access and
// again on every walk entry. When it is done Scan returns (nil, ctx.Err()) and
// discards any snapshots collected so far. Partial results are never returned,
// because a caller cannot tell a truncated rule set apart from a repository
// whose rule files were deleted, and would persist the wrong state. A nil ctx is
// treated as context.Background().
//
// The deadline is cooperative and is observed only BETWEEN syscalls: Go cannot
// cancel a blocking filesystem call, so if a readdir (or the root stat) parks in
// the kernel on a stalled network or cloud-sync mount, Scan does not return and
// no ctx expiry can make it. Callers that must bound wall-clock time therefore
// cannot simply pass a deadline; they have to run Scan through RunBounded, which
// abandons the goroutine instead of waiting for it.
func Scan(ctx context.Context, opts ScanOptions) ([]*store.ProjectRuleSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	root := strings.TrimSpace(opts.RepositoryRoot)
	if root == "" {
		return nil, nil
	}
	root = filepath.Clean(root)
	// Checked before os.Stat: on a stalled mount the root stat is itself an
	// uninterruptible syscall, so an expired deadline must not enter it.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	maxBytes := opts.MaxFileBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxFileBytes
	}

	specs := specsForAgent(opts.Agent)
	if len(specs) == 0 {
		return nil, nil
	}

	seenRoot := make(map[string]bool, len(specs))
	snapshots := make([]*store.ProjectRuleSnapshot, 0, len(specs))
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		// Checked on every entry so the walk unwinds at the first opportunity
		// after cancellation, instead of finishing the remaining tree.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if walkErr != nil {
			if path == root {
				return walkErr
			}
			if spec, ok := specForName(filepath.Base(path), specs); ok {
				rel := relativePath(root, path)
				snapshots = append(snapshots, unreadableSnapshot(rel, spec, walkErr))
				if !strings.Contains(rel, "/") {
					seenRoot[spec.FileName] = true
				}
			}
			return nil
		}

		if entry.IsDir() {
			if path != root {
				if _, skip := ignoredDirs[entry.Name()]; skip {
					return filepath.SkipDir
				}
			}
			return nil
		}

		spec, ok := specForName(entry.Name(), specs)
		if !ok {
			return nil
		}
		rel := relativePath(root, path)
		if !strings.Contains(rel, "/") {
			seenRoot[spec.FileName] = true
		}
		snapshot, scanErr := snapshotFile(root, path, rel, spec, maxBytes)
		snapshots = append(snapshots, snapshot)
		return scanErr
	})
	if err != nil {
		return nil, err
	}

	if opts.IncludeMissingRoot {
		for _, spec := range specs {
			if !seenRoot[spec.FileName] {
				snapshots = append(snapshots, missingSnapshot(spec))
			}
		}
	}
	return snapshots, nil
}

type ruleSpec struct {
	FileName string
	Kind     string
	Title    string
}

func specsForAgent(agent string) []ruleSpec {
	switch strings.ToLower(strings.TrimSpace(agent)) {
	case AgentClaude:
		return []ruleSpec{{FileName: "CLAUDE.md", Kind: "claude", Title: "Claude project instructions"}}
	case AgentCodex:
		return []ruleSpec{{FileName: "AGENTS.md", Kind: "agents", Title: "Codex agent instructions"}}
	default:
		return nil
	}
}

func specForName(name string, specs []ruleSpec) (ruleSpec, bool) {
	for _, spec := range specs {
		if name == spec.FileName {
			return spec, true
		}
	}
	return ruleSpec{}, false
}

func snapshotFile(root, path, rel string, spec ruleSpec, maxBytes int64) (*store.ProjectRuleSnapshot, error) {
	info, err := os.Stat(path)
	if err != nil {
		return unreadableSnapshot(rel, spec, err), nil
	}
	if info.IsDir() {
		err := fmt.Errorf("rule path is a directory")
		return unreadableSnapshot(rel, spec, err), nil
	}
	if info.Size() > maxBytes {
		err := fmt.Errorf("rule file exceeds max size %d bytes", maxBytes)
		return unreadableSnapshot(rel, spec, err), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return unreadableSnapshot(rel, spec, err), nil
	}
	hash := sha256.Sum256(data)
	content := string(data)
	return &store.ProjectRuleSnapshot{
		RulePath:    rel,
		RuleKind:    spec.Kind,
		RuleScope:   scopeForPath(rel),
		Title:       firstMarkdownHeading(content, spec.Title),
		Status:      StatusActive,
		ContentHash: "sha256:" + hex.EncodeToString(hash[:]),
		Content:     content,
		SizeBytes:   len(data),
		AppliesTo:   appliesTo(root, rel),
		RawMetadata: map[string]interface{}{
			"source": "cctrace-sync",
		},
	}, nil
}

func missingSnapshot(spec ruleSpec) *store.ProjectRuleSnapshot {
	return &store.ProjectRuleSnapshot{
		RulePath:  spec.FileName,
		RuleKind:  spec.Kind,
		RuleScope: "repository",
		Title:     spec.Title,
		Status:    StatusMissing,
		RawMetadata: map[string]interface{}{
			"source": "cctrace-sync",
		},
	}
}

func unreadableSnapshot(rel string, spec ruleSpec, err error) *store.ProjectRuleSnapshot {
	if rel == "" {
		rel = spec.FileName
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return &store.ProjectRuleSnapshot{
		RulePath:  rel,
		RuleKind:  spec.Kind,
		RuleScope: scopeForPath(rel),
		Title:     spec.Title,
		Status:    StatusUnreadable,
		ReadError: msg,
		RawMetadata: map[string]interface{}{
			"source":     "cctrace-sync",
			"read_error": msg,
		},
	}
}

func relativePath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return filepath.Base(path)
	}
	return filepath.ToSlash(rel)
}

func scopeForPath(rel string) string {
	if strings.TrimSpace(rel) == "" || !strings.Contains(rel, "/") {
		return "repository"
	}
	return "directory"
}

func appliesTo(root, rel string) []string {
	if scopeForPath(rel) == "repository" {
		return []string{filepath.Base(root)}
	}
	return []string{filepath.ToSlash(filepath.Dir(rel))}
}

func firstMarkdownHeading(content, fallback string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			title := strings.TrimSpace(strings.TrimPrefix(line, "# "))
			if title != "" {
				return title
			}
		}
	}
	return fallback
}
