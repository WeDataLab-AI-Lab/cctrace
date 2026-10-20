package codexconfig

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"cctrace/internal/profile"
)

func TestOwnedOtelRemovalPlansExactBytesAndPreservesForeign(t *testing.T) {
	t.Parallel()
	home, _ := profile.NormalizeCodexHome(t.TempDir())
	if err := WriteOtelBlock(home, "https://owned.invalid", "synthetic", ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "config.toml")
	before, _ := os.ReadFile(path)
	foreign, err := PlanOwnedOtelRemoval(before, "https://foreign.invalid", "synthetic")
	if err != nil || !bytes.Equal(before, foreign) {
		t.Fatal("foreign endpoint was removed")
	}
	after, err := PlanOwnedOtelRemoval(before, "https://owned.invalid", "synthetic")
	if err != nil || bytes.Contains(after, []byte("metrics_exporter")) {
		t.Fatalf("owned exporter not removed: %v", err)
	}
	if err = ApplyHomeConfigCAS(home, before, after); err != nil {
		t.Fatal(err)
	}
	if err = ApplyHomeConfigCAS(home, after, before); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(path)
	if !bytes.Equal(restored, before) {
		t.Fatal("exact restore failed")
	}
}
