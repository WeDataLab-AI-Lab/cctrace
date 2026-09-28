package projectrule

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

// countingCtx reports "cancelled" only after Err has been consulted cancelAt
// times, so a walk can be interrupted at a deterministic point without relying
// on timing. WalkDir drives the callback from a single goroutine, so the
// unsynchronised counter is safe here.
type countingCtx struct {
	context.Context
	calls    int
	cancelAt int
}

func (c *countingCtx) Err() error {
	c.calls++
	if c.calls > c.cancelAt {
		return context.Canceled
	}
	return c.Context.Err()
}

// buildRuleTree creates dirs directories under root, each holding one rule file,
// plus a rule file at the root itself.
func buildRuleTree(t *testing.T, root string, dirs int) {
	t.Helper()
	writeFile(t, filepath.Join(root, "CLAUDE.md"), "# Root Rules\n")
	for i := 0; i < dirs; i++ {
		sub := filepath.Join(root, fmt.Sprintf("pkg%02d", i))
		mkdir(t, sub)
		writeFile(t, filepath.Join(sub, "CLAUDE.md"), fmt.Sprintf("# Rules %d\n", i))
	}
}

// TestScan_CancelledContextBeforeRootStat pins the ordering the doc comment
// promises: a context that is already done short-circuits before Scan touches
// the filesystem at all. A nonexistent root makes the check observable — the
// os.Stat path swallows IsNotExist and returns (nil, nil), so an error here can
// only come from a pre-stat context check. On a stalled mount that very stat is
// the uninterruptible syscall this scan must avoid entering.
func TestScan_CancelledContextBeforeRootStat(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	snapshots, err := Scan(ctx, ScanOptions{Agent: AgentClaude, RepositoryRoot: missing})
	if err != context.Canceled {
		t.Fatalf("Scan err = %v, want context.Canceled (context must be checked before os.Stat)", err)
	}
	if snapshots != nil {
		t.Fatalf("snapshots = %v, want nil on cancellation", snapshots)
	}
}

// TestScan_AlreadyCancelledContext verifies Scan aborts when the caller's
// context is already done, returning no partial snapshots.
func TestScan_AlreadyCancelledContext(t *testing.T) {
	root := t.TempDir()
	buildRuleTree(t, root, 5)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	snapshots, err := Scan(ctx, ScanOptions{Agent: AgentClaude, RepositoryRoot: root, IncludeMissingRoot: true})
	if err != context.Canceled {
		t.Fatalf("Scan err = %v, want context.Canceled", err)
	}
	if snapshots != nil {
		t.Fatalf("snapshots = %v, want nil on cancellation", snapshots)
	}
}

// TestScan_CancelDuringWalk verifies the walk stops at the first callback that
// sees a cancelled context instead of finishing the whole tree.
func TestScan_CancelDuringWalk(t *testing.T) {
	root := t.TempDir()
	const dirs = 20
	buildRuleTree(t, root, dirs)

	// A full walk visits root + dirs directories + (dirs+1) rule files.
	fullWalkEntries := 1 + dirs + (dirs + 1)
	const cancelAt = 5

	ctx := &countingCtx{Context: context.Background(), cancelAt: cancelAt}
	snapshots, err := Scan(ctx, ScanOptions{Agent: AgentClaude, RepositoryRoot: root})
	if err != context.Canceled {
		t.Fatalf("Scan err = %v, want context.Canceled", err)
	}
	if snapshots != nil {
		t.Fatalf("snapshots = %v, want nil on cancellation", snapshots)
	}
	if ctx.calls != cancelAt+1 {
		t.Fatalf("ctx.Err calls = %d, want %d (walk must stop at the first cancelled check)", ctx.calls, cancelAt+1)
	}
	if ctx.calls >= fullWalkEntries {
		t.Fatalf("ctx.Err calls = %d, walk did not stop early (full walk = %d entries)", ctx.calls, fullWalkEntries)
	}
}

// TestScan_LiveContextMatchesFullScan is the regression guard: a live context
// must produce exactly the snapshots the pre-context implementation produced.
func TestScan_LiveContextMatchesFullScan(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, "pkg"))
	writeFile(t, filepath.Join(root, "CLAUDE.md"), "# Root Rules\n")
	writeFile(t, filepath.Join(root, "pkg", "CLAUDE.md"), "# Package Rules\n")
	mkdir(t, filepath.Join(root, "node_modules", "ignored"))
	writeFile(t, filepath.Join(root, "node_modules", "ignored", "CLAUDE.md"), "# Ignore\n")

	snapshots, err := Scan(context.Background(), ScanOptions{Agent: AgentClaude, RepositoryRoot: root})
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("len(snapshots) = %d, want 2", len(snapshots))
	}
	if snapshots[0].RulePath != "CLAUDE.md" || snapshots[0].Status != StatusActive {
		t.Fatalf("root snapshot = %+v", snapshots[0])
	}
	if snapshots[1].RulePath != "pkg/CLAUDE.md" || snapshots[1].RuleScope != "directory" {
		t.Fatalf("sub snapshot = %+v", snapshots[1])
	}
}

// TestScan_NilContextTreatedAsBackground keeps Scan usable from call sites that
// have no context to hand without panicking.
func TestScan_NilContextTreatedAsBackground(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "CLAUDE.md"), "# Root Rules\n")

	//nolint:staticcheck // deliberately exercising the nil-context guard
	snapshots, err := Scan(nil, ScanOptions{Agent: AgentClaude, RepositoryRoot: root})
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("len(snapshots) = %d, want 1", len(snapshots))
	}
}
