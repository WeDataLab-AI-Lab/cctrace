package syncer

import (
	"os"
	"path/filepath"
	"testing"
)

// A State built without a path used to write ".tmp" into the working directory
// and then fail the rename, leaving the file behind. Most callers discard the
// Save error -- a failed state save must not stop collection -- so the litter
// was the only trace, with nothing pointing at the cause. Two of my own tests
// produced one each before this guard existed.
func TestSaveRefusesAnEmptyPath(t *testing.T) {
	dir := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	s := &State{}
	if err := s.Save(); err == nil {
		t.Error("Save with no path reported success")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		t.Errorf("Save wrote %q into the working directory", filepath.Join(dir, e.Name()))
	}
}
