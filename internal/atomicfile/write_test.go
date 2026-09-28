package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteReplacesContentWithoutTempLeak(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte("old\n"), 0644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	if err := Write(path, []byte("new\n"), 0644); err != nil {
		t.Fatalf("Write: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != "new\n" {
		t.Fatalf("content = %q, want new", data)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("temporary file leaked: %s", entry.Name())
		}
	}
}

func TestWritePreservesDestinationDirectoryOnFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatalf("mkdir destination: %v", err)
	}

	if err := Write(path, []byte("new\n"), 0644); err == nil {
		t.Fatal("expected Write to fail when destination is a directory")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat destination: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("destination directory was not preserved")
	}
}

func TestWriteDoesNotWidenExistingMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode preservation is not meaningful on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("old\n"), 0600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	if err := Write(path, []byte("new\n"), 0644); err != nil {
		t.Fatalf("Write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("mode = %v, want 0600", got)
	}
}

func TestWriteRejectsSymlinkDestination(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on some Windows hosts")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target.toml")
	link := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(target, []byte("old\n"), 0600); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := Write(link, []byte("new\n"), 0644)
	if !errors.Is(err, ErrSymlinkUnsupported) {
		t.Fatalf("err = %v, want ErrSymlinkUnsupported", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(data) != "old\n" {
		t.Fatalf("target content = %q, want old", data)
	}
}
