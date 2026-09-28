package projectrule

import (
	"context"
	"errors"
	"testing"
	"time"

	"cctrace/internal/store"
)

// blockingScan stands in for a walk parked in an uninterruptible readdir
// syscall: it ignores ctx completely and returns only when release is closed.
func blockingScan(release <-chan struct{}) ScanFunc {
	return func(context.Context, ScanOptions) ([]*store.ProjectRuleSnapshot, error) {
		<-release
		return nil, nil
	}
}

// TestRunBounded_ScanIgnoringContext reproduces the production hang: the daemon
// was parked in fdopendir, where no context expiry is ever observed. RunBounded
// must give up on the goroutine and return at the deadline anyway.
func TestRunBounded_ScanIgnoringContext(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	snapshots, err := RunBounded(ctx, ScanOptions{Agent: AgentClaude, RepositoryRoot: t.TempDir()}, blockingScan(release))
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunBounded err = %v, want context.DeadlineExceeded", err)
	}
	if snapshots != nil {
		t.Fatalf("snapshots = %v, want nil", snapshots)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("RunBounded took %v, want a bounded return at the deadline", elapsed)
	}
}

// TestRunBounded_InFlightScanNotRestarted keeps abandoned goroutines from
// accumulating: a root whose scan is still stuck must not get a second walk
// started behind it on every retry.
func TestRunBounded_InFlightScanNotRestarted(t *testing.T) {
	opts := ScanOptions{Agent: AgentClaude, RepositoryRoot: t.TempDir()}
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := RunBounded(ctx, opts, blockingScan(release)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first RunBounded err = %v, want context.DeadlineExceeded", err)
	}

	calls := 0
	_, err := RunBounded(context.Background(), opts, func(context.Context, ScanOptions) ([]*store.ProjectRuleSnapshot, error) {
		calls++
		return nil, nil
	})
	if !errors.Is(err, ErrScanInFlight) {
		t.Fatalf("second RunBounded err = %v, want ErrScanInFlight", err)
	}
	if calls != 0 {
		t.Fatalf("scan calls = %d, want 0 while the previous walk is still stuck", calls)
	}
}

// TestRunBounded_PassesResultThrough guards the normal path: a scan that
// finishes inside the deadline is returned verbatim.
func TestRunBounded_PassesResultThrough(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/CLAUDE.md", "# Root Rules\n")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	snapshots, err := RunBounded(ctx, ScanOptions{Agent: AgentClaude, RepositoryRoot: root}, Scan)
	if err != nil {
		t.Fatalf("RunBounded err = %v", err)
	}
	if len(snapshots) != 1 || snapshots[0].RulePath != "CLAUDE.md" {
		t.Fatalf("snapshots = %+v, want the root CLAUDE.md", snapshots)
	}
}

// TestRunBounded_ScanErrorPropagates keeps caller-side error handling (TTL
// parking) reachable for ordinary scan failures.
func TestRunBounded_ScanErrorPropagates(t *testing.T) {
	want := errors.New("input/output error")
	_, err := RunBounded(context.Background(), ScanOptions{Agent: AgentClaude, RepositoryRoot: t.TempDir()},
		func(context.Context, ScanOptions) ([]*store.ProjectRuleSnapshot, error) { return nil, want })
	if !errors.Is(err, want) {
		t.Fatalf("RunBounded err = %v, want %v", err, want)
	}
}
