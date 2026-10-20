package codexconfig

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func TestConfigLockCoversEntireReadModifyWrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	release, err := lockConfig(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	contender := flock.New(filepath.Join(dir, "config.toml.lock"))
	ok, err := contender.TryLock()
	if err != nil {
		release()
		t.Fatal(err)
	}
	if ok {
		_ = contender.Unlock()
		release()
		t.Fatal("config lock is not held")
	}
	release()
	if err = WriteOtelBlock(dir, "http://localhost:4317", "token", ""); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := HealExistingOtelBlock(dir, "http://localhost:4317", "next", "", false); done <- err }()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("heal deadlocked by recursively taking config lock")
	}
	if got := OtelBearerToken(dir); got != "next" {
		t.Fatalf("token=%s", got)
	}
}

func TestHealDoesNotCreateMissingConfigHome(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "missing")
	result, err := HealExistingOtelBlock(dir, "http://localhost:4317", "token", "", false)
	if err != nil || result != HealNoOtel {
		t.Fatalf("result=%v err=%v", result, err)
	}
	if _, err = os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("heal created unused home: %v", err)
	}
}
