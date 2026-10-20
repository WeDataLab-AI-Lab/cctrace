package localdaemon

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func TestLegacyLeaseHelper(t *testing.T) {
	dir := os.Getenv("CCTRACE_LEGACY_LEASE_FIXTURE")
	if dir == "" {
		return
	}
	l := flock.New(filepath.Join(dir, "sync.lock"))
	if err := l.Lock(); err != nil {
		t.Fatal(err)
	}
	defer l.Unlock()
	rt := Runtime{PID: os.Getpid(), InstanceID: "fixture-instance", StartedAt: time.Now().UTC(), Version: "dev"}
	if err := writeJSON(filepath.Join(dir, "sync-runtime.json"), rt); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sync.pid"), []byte(strconv.Itoa(rt.PID)), 0600); err != nil {
		t.Fatal(err)
	}
	for {
		var req commandRequest
		if ReadJSON(filepath.Join(dir, "sync-stop.json"), &req) == nil && req.InstanceID == rt.InstanceID {
			if err := writeJSON(filepath.Join(dir, "sync-exit.json"), struct {
				InstanceID string    `json:"instance_id"`
				ExitedAt   time.Time `json:"exited_at"`
			}{rt.InstanceID, time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestLegacyLeaseRefusesUnverifiedRuntime(t *testing.T) {
	dir := t.TempDir()
	l := flock.New(filepath.Join(dir, "sync.lock"))
	if err := l.Lock(); err != nil {
		t.Fatal(err)
	}
	defer l.Unlock()
	if err := writeJSON(filepath.Join(dir, "sync-runtime.json"), Runtime{PID: os.Getpid() + 100000, InstanceID: "forged", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := AcquireProfile(ctx, dir); err != ErrMigrationPending {
		t.Fatalf("unverified legacy owner accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sync-stop.json")); !os.IsNotExist(err) {
		t.Fatal("sent stop to unverified owner")
	}
}
