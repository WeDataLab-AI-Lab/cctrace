//go:build windows

package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// holdForReplace opens path the way a replace does while it runs: with DELETE
// access, which an open without FILE_SHARE_DELETE (os.Open) cannot coexist with.
// That is what makes a reader's open fail with a sharing violation.
func holdForReplace(t *testing.T, path string) windows.Handle {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.DELETE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	return handle
}

func busyFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("content"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func openWithRetry(path string) (f *os.File, err error) {
	err = RetryBusy(func() (openErr error) { f, openErr = os.Open(path); return openErr })
	return f, err
}

// The window a replace holds a file is short, and a reader that meets it opens the
// file once the replace is through. The first wait ends the hold, so nothing
// really sleeps.
func TestOpenWaitsOutAReplaceInProgress(t *testing.T) {
	path := busyFile(t)
	held := holdForReplace(t, path)
	released := false
	release := func() {
		if !released {
			released = true
			_ = windows.CloseHandle(held)
		}
	}
	defer release()
	sleeps := 0
	useRetrySleep(t, func(time.Duration) { sleeps++; release() })

	f, err := openWithRetry(path)
	if err != nil {
		t.Fatalf("open of a file held for a replace that ends within the budget: %v", err)
	}
	f.Close()
	if sleeps == 0 {
		t.Fatal("the open never had to wait: a replace in progress no longer holds readers off, and this test proves nothing")
	}
}

func TestOpenGivesUpOnAFileHeldPastTheBudget(t *testing.T) {
	path := busyFile(t)
	held := holdForReplace(t, path)
	defer windows.CloseHandle(held)
	var waited time.Duration
	useRetrySleep(t, func(d time.Duration) { waited += d })

	f, err := openWithRetry(path)
	if err == nil {
		f.Close()
		t.Fatal("open succeeded on a file held for a replace")
	}
	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("open error = %v, want the sharing violation Windows gave", err)
	}
	if waited < retryBudget {
		t.Fatalf("gave up after %v, before the %v budget", waited, retryBudget)
	}
}

func TestOpenDoesNotWaitForAMissingFile(t *testing.T) {
	useRetrySleep(t, func(time.Duration) { t.Fatal("waited for a file that does not exist") })
	_, err := openWithRetry(filepath.Join(t.TempDir(), "absent"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("open of a missing file = %v, want os.ErrNotExist", err)
	}
}
