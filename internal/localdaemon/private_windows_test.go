//go:build windows

package localdaemon

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"

	"cctrace/internal/filesec"
	"cctrace/internal/filesec/filesectest"
)

// requirePrivate fails the test if users other than the owner can change the
// file or directory at path. Go reports every Windows file as 0666 and every
// directory as 0777 whatever mode it was made with, so perm cannot be read back;
// what it stands for is read from the access list, as the daemon itself does.
func requirePrivate(t *testing.T, path string, _ os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if writable, err := filesec.WritableByOthers(path, info); err != nil || writable {
		t.Fatalf("writable by others = %v, %v", writable, err)
	}
}

// letGroupWrite lets users other than the owner change path: a write grant to
// BUILTIN\Users, the group every local user is in. chmod cannot do it here; it
// sets only the read-only attribute.
func letGroupWrite(t *testing.T, path string, _ os.FileMode) {
	t.Helper()
	filesectest.Grant(t, path, windows.WinBuiltinUsersSid, windows.FILE_GENERIC_WRITE)
}
