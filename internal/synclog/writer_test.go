package synclog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write is a helper that fails the test on a short or failed write, so the
// rotation assertions below are not masked by an I/O error.
func write(t *testing.T, w *Writer, s string) {
	t.Helper()
	n, err := w.Write([]byte(s))
	if err != nil {
		t.Fatalf("write %q: %v", s, err)
	}
	if n != len(s) {
		t.Fatalf("write %q: wrote %d bytes, want %d", s, n, len(s))
	}
}

func TestWriterRotatesAtMaxSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sync.log")
	w := New(path, 32, 3)
	defer w.Close()

	write(t, w, "0123456789\n") // 11 bytes, under the limit
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("rotated before reaching maxSize")
	}

	write(t, w, strings.Repeat("x", 40)+"\n") // crosses 32 bytes

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected %s.1 after rotation: %v", path, err)
	}
	// The backup holds what was written before the rotation, and the live file
	// starts empty again.
	backup, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if !strings.Contains(string(backup), "0123456789") {
		t.Fatalf("backup lost pre-rotation content: %q", backup)
	}
	live, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read live file: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("live file should be empty after rotation, got %d bytes", len(live))
	}
}

func TestWriterKeepsMaxBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sync.log")
	const maxBackups = 3
	w := New(path, 8, maxBackups)
	defer w.Close()

	// Every write exceeds maxSize, so each one rotates.
	for i := 0; i < maxBackups+4; i++ {
		write(t, w, strings.Repeat("y", 16)+"\n")
	}

	for i := 1; i <= maxBackups; i++ {
		if _, err := os.Stat(pathFor(path, i)); err != nil {
			t.Fatalf("expected backup %d to exist: %v", i, err)
		}
	}
	if _, err := os.Stat(pathFor(path, maxBackups+1)); !os.IsNotExist(err) {
		t.Fatalf("backup %d should have been discarded", maxBackups+1)
	}
}

// A daemon restart must not reset the budget: reopening an existing log has to
// carry its current size forward, otherwise every restart grants another
// maxSize of growth and the cap never binds.
func TestWriterResumesExistingFileSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sync.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("z", 30)), 0644); err != nil {
		t.Fatalf("seed log: %v", err)
	}

	w := New(path, 32, 2)
	defer w.Close()
	write(t, w, "abc\n") // 30 + 4 crosses 32

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected rotation to account for the pre-existing size: %v", err)
	}
}

// Backups shift by one on each rotation, so .2 is always older than .1.
func TestWriterShiftsBackupsInOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sync.log")
	w := New(path, 8, 3)
	defer w.Close()

	write(t, w, "first-block-aaaa\n")
	write(t, w, "second-block-bbb\n")
	write(t, w, "third-block-cccc\n")

	// Three rotations: .1 holds the newest displaced block, .3 the oldest.
	for backup, want := range map[int]string{1: "third-block", 2: "second-block", 3: "first-block"} {
		got, err := os.ReadFile(pathFor(path, backup))
		if err != nil {
			t.Fatalf("read .%d: %v", backup, err)
		}
		if !strings.Contains(string(got), want) {
			t.Fatalf(".%d should hold %q, got %q", backup, want, got)
		}
	}
}

// maxBackups 0 means "keep nothing": the log is truncated instead of renamed,
// so the cap still holds without leaving files behind.
func TestWriterWithoutBackupsTruncates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sync.log")
	w := New(path, 8, 0)
	defer w.Close()

	write(t, w, "aaaaaaaaaaaaaaaa\n")
	write(t, w, "bbbb\n")

	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("no backup should be kept when maxBackups is 0")
	}
	live, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read live file: %v", err)
	}
	if strings.Contains(string(live), "aaaa") {
		t.Fatalf("pre-rotation content should be gone, got %q", live)
	}
}

func TestWriterCreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "sync.log")
	w := New(path, 1024, 1)
	defer w.Close()

	write(t, w, "hello\n")

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected log file in a created parent dir: %v", err)
	}
}
