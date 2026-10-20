package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"cctrace/internal/profile"

	"github.com/gofrs/flock"
)

func TestKillCannotTurnProfileStopIntoWholeDaemonKill(t *testing.T) {
	useTempSyncHome(t)
	root := profile.DefaultDir()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	lock := flock.New(filepath.Join(root, "daemon.lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	if err := startWatchRuntime("work", &syncRuntime{PID: os.Getpid(), InstanceID: "fixture", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	oldFree, oldKill := syncLockFreeFn, killProcessFn
	t.Cleanup(func() { syncLockFreeFn = oldFree; killProcessFn = oldKill })
	probes := 0
	syncLockFreeFn = func(string) (bool, error) { probes++; return probes > 1, nil }
	called := false
	killProcessFn = func(int) error { called = true; return nil }
	if err := runKill("work"); err == nil || called {
		t.Fatalf("profile force control killed singleton: error=%v signalled=%v", err, called)
	}
}
