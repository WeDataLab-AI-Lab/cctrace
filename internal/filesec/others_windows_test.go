//go:build windows

package filesec

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"

	"cctrace/internal/filesec/filesectest"
)

func fixtures(t *testing.T) (dir, file string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "d")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file = filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	return dir, file
}

func writable(t *testing.T, path string) bool {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := WritableByOthers(path, info)
	if err != nil {
		t.Fatalf("WritableByOthers(%s): %v", filepath.Base(path), err)
	}
	return got
}

func TestWritableByOthersAcceptsWhatTheUserCreated(t *testing.T) {
	dir, file := fixtures(t)
	for _, path := range []string{dir, file} {
		if writable(t, path) {
			t.Errorf("%s created under the user's temp directory counts as writable by others", filepath.Base(path))
		}
	}
}

func TestWritableByOthersRejectsBroadGroupWrite(t *testing.T) {
	for name, group := range map[string]windows.WELL_KNOWN_SID_TYPE{
		"everyone":            windows.WinWorldSid,
		"authenticated users": windows.WinAuthenticatedUserSid,
		"builtin users":       windows.WinBuiltinUsersSid,
	} {
		dir, file := fixtures(t)
		for _, path := range []string{dir, file} {
			filesectest.Grant(t, path, group, windows.FILE_GENERIC_WRITE)
			if !writable(t, path) {
				t.Errorf("%s: a write entry for %s on a %s went unnoticed", name, name, filepath.Base(path))
			}
		}
	}
}

func TestWritableByOthersAcceptsBroadGroupRead(t *testing.T) {
	dir, file := fixtures(t)
	for _, path := range []string{dir, file} {
		filesectest.Grant(t, path, windows.WinWorldSid, windows.FILE_GENERIC_READ)
		if writable(t, path) {
			t.Errorf("a read-only entry for everyone on a %s counts as write access", filepath.Base(path))
		}
	}
}

func TestWritableByOthersReportsAMissingPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	got, err := WritableByOthers(missing, nil)
	if err == nil || got {
		t.Fatalf("WritableByOthers on a missing path = %v, %v; want an error", got, err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the error for a missing path hides that it is missing: %v", err)
	}
}
