package projectrule

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestScan_CodexRules(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, "pkg"))
	writeFile(t, filepath.Join(root, "AGENTS.md"), "# Root Rules\n\nUse tests.\n")
	writeFile(t, filepath.Join(root, "pkg", "AGENTS.md"), "# Package Rules\n")
	writeFile(t, filepath.Join(root, "CLAUDE.md"), "# Claude Rules\n")
	mkdir(t, filepath.Join(root, "node_modules", "ignored"))
	writeFile(t, filepath.Join(root, "node_modules", "ignored", "AGENTS.md"), "# Ignore\n")

	snapshots, err := Scan(context.Background(), ScanOptions{Agent: AgentCodex, RepositoryRoot: root})
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("len(snapshots) = %d, want 2", len(snapshots))
	}

	rootSnap := snapshots[0]
	if rootSnap.RulePath != "AGENTS.md" {
		t.Fatalf("root rule_path = %q, want AGENTS.md", rootSnap.RulePath)
	}
	if rootSnap.RuleKind != "agents" {
		t.Fatalf("rule_kind = %q, want agents", rootSnap.RuleKind)
	}
	if rootSnap.RuleScope != "repository" {
		t.Fatalf("root rule_scope = %q, want repository", rootSnap.RuleScope)
	}
	if rootSnap.Status != StatusActive {
		t.Fatalf("status = %q, want active", rootSnap.Status)
	}
	if rootSnap.Title != "Root Rules" {
		t.Fatalf("title = %q, want Root Rules", rootSnap.Title)
	}
	if rootSnap.ContentHash == "" {
		t.Fatal("content_hash is empty")
	}

	subSnap := snapshots[1]
	if subSnap.RulePath != "pkg/AGENTS.md" {
		t.Fatalf("sub rule_path = %q, want pkg/AGENTS.md", subSnap.RulePath)
	}
	if subSnap.RuleScope != "directory" {
		t.Fatalf("sub rule_scope = %q, want directory", subSnap.RuleScope)
	}
	if len(subSnap.AppliesTo) != 1 || subSnap.AppliesTo[0] != "pkg" {
		t.Fatalf("applies_to = %v, want [pkg]", subSnap.AppliesTo)
	}
}

// TestScan_SkipsDependencyCacheDirs pins the skip list against the trees that
// actually dominated a real walk. The daemon was caught inside
// <repo>/.conda/lib/python3.10/site-packages/tensorflow/..., which the original
// list did not cover: node_modules was skipped while the far larger interpreter
// and toolchain caches were walked in full.
func TestScan_SkipsDependencyCacheDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "CLAUDE.md"), "# Root Rules\n")
	for _, dir := range []string{".conda", "site-packages", ".cargo", ".rustup", ".npm", ".pnpm", ".gem", ".pyenv", ".nvm", "Pods", "DerivedData"} {
		mkdir(t, filepath.Join(root, dir, "nested"))
		writeFile(t, filepath.Join(root, dir, "nested", "CLAUDE.md"), "# Vendored\n")
	}

	snapshots, err := Scan(context.Background(), ScanOptions{Agent: AgentClaude, RepositoryRoot: root})
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	if len(snapshots) != 1 || snapshots[0].RulePath != "CLAUDE.md" {
		t.Fatalf("snapshots = %+v, want only the root CLAUDE.md", snapshots)
	}
}

func TestScan_MissingRoot(t *testing.T) {
	root := t.TempDir()

	snapshots, err := Scan(context.Background(), ScanOptions{
		Agent:              AgentClaude,
		RepositoryRoot:     root,
		IncludeMissingRoot: true,
	})
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("len(snapshots) = %d, want 1", len(snapshots))
	}
	if snapshots[0].RulePath != "CLAUDE.md" {
		t.Fatalf("rule_path = %q, want CLAUDE.md", snapshots[0].RulePath)
	}
	if snapshots[0].Status != StatusMissing {
		t.Fatalf("status = %q, want missing", snapshots[0].Status)
	}
}

func TestScan_ContentHashStable(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "AGENTS.md")
	writeFile(t, path, "# Rules\n")

	first, err := Scan(context.Background(), ScanOptions{Agent: AgentCodex, RepositoryRoot: root})
	if err != nil {
		t.Fatalf("first Scan returned error: %v", err)
	}
	second, err := Scan(context.Background(), ScanOptions{Agent: AgentCodex, RepositoryRoot: root})
	if err != nil {
		t.Fatalf("second Scan returned error: %v", err)
	}
	if first[0].ContentHash == "" {
		t.Fatal("content_hash is empty")
	}
	if first[0].ContentHash != second[0].ContentHash {
		t.Fatalf("content_hash changed without content change: %q != %q", first[0].ContentHash, second[0].ContentHash)
	}

	writeFile(t, path, "# Rules\n\nMore\n")
	changed, err := Scan(context.Background(), ScanOptions{Agent: AgentCodex, RepositoryRoot: root})
	if err != nil {
		t.Fatalf("changed Scan returned error: %v", err)
	}
	if changed[0].ContentHash == first[0].ContentHash {
		t.Fatalf("content_hash did not change after content update: %q", changed[0].ContentHash)
	}
}

func TestScan_UnreadableOversized(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "too large")

	snapshots, err := Scan(context.Background(), ScanOptions{
		Agent:          AgentCodex,
		RepositoryRoot: root,
		MaxFileBytes:   1,
	})
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("len(snapshots) = %d, want 1", len(snapshots))
	}
	if snapshots[0].Status != StatusUnreadable {
		t.Fatalf("status = %q, want unreadable", snapshots[0].Status)
	}
	if snapshots[0].ReadError == "" {
		t.Fatal("read_error is empty")
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir parent %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
