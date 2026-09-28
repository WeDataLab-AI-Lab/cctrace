package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The updater writes its replacement next to the running binary, so the
// directory is what has to be writable -- not the file. A user hit exactly this
// distinction: `mv` asked "override rwxr-xr-x root/wheel?", was answered yes,
// and still failed, because /usr/local/bin itself is root-owned (#459).
func TestUpdateTargetWritableChecksDirectoryNotFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permissions")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the permission bits this test relies on")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "cctrace")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	if updateTargetWritable(bin) {
		t.Fatal("read-only directory reported writable; the update would fail at apply")
	}
}

// A writable directory is the ordinary case and must not be reported as broken.
func TestUpdateTargetWritableAcceptsWritableDir(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "cctrace")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !updateTargetWritable(bin) {
		t.Fatal("writable directory reported unwritable")
	}
}

// An unresolvable path must not claim the update is broken: a false alarm in
// `status` costs more than staying quiet, and the updater reports its own error.
func TestUpdateTargetWritableTreatsUnknownPathAsOK(t *testing.T) {
	if updateTargetWritable("") {
		return // either answer is defensible; pin the one we chose below
	}
	t.Fatal("empty path should not be reported as unwritable")
}
