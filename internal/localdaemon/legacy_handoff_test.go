//go:build darwin || linux

package localdaemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The graceful handoff needs the kernel owner of the lock (owner_darwin.go,
// owner_linux.go). Where there is no way to look it up, the same situation ends in
// migration_pending: see legacy_unverified_test.go.
func TestLegacyLeaseGracefulHandoffPreservesCursor(t *testing.T) {
	dir := t.TempDir()
	cursor := []byte(`{"files":{"/synthetic/session.jsonl":{"offset":17}}}`)
	if err := os.WriteFile(filepath.Join(dir, "sync-state.json"), cursor, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLegacyLeaseHelper$", "sync", "--watch")
	child.Env = []string{"PATH=" + os.Getenv("PATH"), "CCTRACE_LEGACY_LEASE_FIXTURE=" + dir}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	joined := false
	t.Cleanup(func() {
		cancel()
		if !joined {
			_ = child.Wait()
		}
	})
	eventually(t, func() bool {
		return legacyLeaseFixtureReady(dir, child.Process.Pid)
	})
	release, err := AcquireProfile(ctx, dir)
	if err != nil {
		pid := strconv.Itoa(child.Process.Pid)
		pidRaw, pidReadErr := os.ReadFile(filepath.Join(dir, "sync.pid"))
		runtimeRaw, runtimeReadErr := os.ReadFile(filepath.Join(dir, "sync-runtime.json"))
		executable, exeErr := os.Executable()
		comm, commErr := exec.Command("/bin/ps", "-ww", "-p", pid, "-o", "comm=").CombinedOutput()
		args, argsErr := exec.Command("/bin/ps", "-ww", "-p", pid, "-o", "command=").CombinedOutput()
		out, lsofErr := exec.Command("/usr/sbin/lsof", "-nP", "-a", "-p", pid, "-F", "pfnl", filepath.Join(dir, "sync.lock")).CombinedOutput()
		t.Fatalf("handoff failed: %v (kernel owner %d, child %d), pid=%q (%v) runtime=%q (%v), executable=%q (%v), fixture comm=%q (%v), args=%q (%v), lsof=%q (%v)", err, ownerPID(filepath.Join(dir, "sync.lock")), child.Process.Pid, pidRaw, pidReadErr, runtimeRaw, runtimeReadErr, executable, exeErr, comm, commErr, args, argsErr, out, lsofErr)
	}
	defer release()
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	joined = true
	got, err := os.ReadFile(filepath.Join(dir, "sync-state.json"))
	if err != nil || string(got) != string(cursor) {
		t.Fatalf("cursor modified at handoff: %s %v", got, err)
	}
}

func legacyLeaseFixtureReady(dir string, pid int) bool {
	// WriteFile creates sync.pid before publishing its bytes. Its existence
	// alone does not mean the helper has finished announcing its identity.
	b, err := os.ReadFile(filepath.Join(dir, "sync.pid"))
	if err != nil {
		return false
	}
	publishedPID, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || publishedPID != pid {
		return false
	}
	var rt Runtime
	return ReadJSON(filepath.Join(dir, "sync-runtime.json"), &rt) == nil && rt.PID == pid
}

func TestLegacyLeaseFixtureReadinessRequiresPublishedPID(t *testing.T) {
	t.Parallel()
	pid := os.Getpid()
	for _, tc := range []struct {
		name       string
		pidText    string
		runtimePID int
		want       bool
	}{
		{name: "empty pid", runtimePID: pid},
		{name: "partial pid", pidText: strconv.Itoa(pid)[:len(strconv.Itoa(pid))-1], runtimePID: pid},
		{name: "other pid", pidText: strconv.Itoa(pid + 1), runtimePID: pid},
		{name: "other runtime pid", pidText: strconv.Itoa(pid), runtimePID: pid + 1},
		{name: "published child", pidText: strconv.Itoa(pid), runtimePID: pid, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			rt := Runtime{PID: tc.runtimePID, InstanceID: "fixture-instance", StartedAt: time.Now().UTC(), Version: "dev"}
			if err := writeJSON(filepath.Join(dir, "sync-runtime.json"), rt); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "sync.pid"), []byte(tc.pidText), 0600); err != nil {
				t.Fatal(err)
			}
			if got := legacyLeaseFixtureReady(dir, pid); got != tc.want {
				t.Fatalf("fixture ready=%v, want %v for pid=%q runtime pid=%d expected child=%d", got, tc.want, tc.pidText, tc.runtimePID, pid)
			}
		})
	}
}
