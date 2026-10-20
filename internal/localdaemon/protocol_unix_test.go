//go:build !windows

package localdaemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOtherUsersWriteBitsMakeDaemonFilesUnsafe(t *testing.T) {
	for _, tc := range []struct{ dir, file os.FileMode }{
		{0720, 0620},
		{0702, 0602},
		{0777, 0666},
	} {
		root := t.TempDir()
		if err := os.Chmod(root, tc.dir); err != nil {
			t.Fatal(err)
		}
		if err := safeDirectory(root); err == nil {
			t.Errorf("a directory with mode %04o passed", tc.dir)
		}
		if _, err := Running(root); err == nil {
			t.Errorf("Running accepted a root with mode %04o", tc.dir)
		}

		root = t.TempDir()
		path := filepath.Join(root, "daemon-runtime.json")
		if err := writeJSON(path, Runtime{PID: 1, InstanceID: "instance"}); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, tc.file); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadRuntime(root); err == nil {
			t.Errorf("a file with mode %04o was read", tc.file)
		}
	}
}

func TestOwnerOnlyWriteDaemonFilesStayReadable(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "daemon-runtime.json")
	if err := writeJSON(path, Runtime{PID: 1, InstanceID: "instance"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := safeDirectory(root); err != nil {
		t.Fatalf("a directory others may read: %v", err)
	}
	if _, err := ReadRuntime(root); err != nil {
		t.Fatalf("a file others may read: %v", err)
	}
}
