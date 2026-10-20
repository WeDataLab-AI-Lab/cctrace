//go:build !darwin && !linux

package localdaemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

// Where the kernel owner of a legacy lock cannot be looked up (owner_other.go), a
// live legacy watcher is not stopped: the lease reports migration_pending, writes
// no stop request, and the watcher keeps the lock. The graceful handoff of
// legacy_handoff_test.go is not available here.
func TestLegacyLeaseOfAnUnverifiableOwnerStaysPending(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLegacyLeaseHelper$", "sync", "--watch")
	child.Env = []string{"PATH=" + os.Getenv("PATH"), "CCTRACE_LEGACY_LEASE_FIXTURE=" + dir}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = child.Wait()
	})
	eventually(t, func() bool {
		var rt Runtime
		_, err := os.Stat(filepath.Join(dir, "sync.pid"))
		return err == nil && ReadJSON(filepath.Join(dir, "sync-runtime.json"), &rt) == nil
	})
	if _, err := AcquireProfile(ctx, dir); !errors.Is(err, ErrMigrationPending) {
		t.Fatalf("AcquireProfile = %v, want migration pending", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sync-stop.json")); !os.IsNotExist(err) {
		t.Fatalf("a stop request was sent to an owner that could not be verified: %v", err)
	}
	held, err := flock.New(filepath.Join(dir, "sync.lock")).TryLock()
	if err != nil || held {
		t.Fatalf("the watcher no longer holds its lock: acquired=%v, %v", held, err)
	}
}
