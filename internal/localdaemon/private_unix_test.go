//go:build !windows

package localdaemon

import (
	"os"
	"testing"
)

// requirePrivate fails the test unless the file or directory at path has the
// mode the daemon makes it with, perm.
func requirePrivate(t *testing.T, path string, perm os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != perm {
		t.Fatalf("%v %v, want mode %v", info, err, perm)
	}
}

// letGroupWrite lets users other than the owner change path: mode perm, which
// carries the group write bit.
func letGroupWrite(t *testing.T, path string, perm os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, perm); err != nil {
		t.Fatal(err)
	}
}
