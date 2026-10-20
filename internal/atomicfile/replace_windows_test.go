//go:build windows

package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// os.Open, like every Go open, leaves out FILE_SHARE_DELETE, and a classic
// MoveFileEx cannot replace a file while any handle on it is open. The replace
// waits, and goes through once the reader has closed. The wait is the seam
// retrySleep: the first sleep closes the reader, so nothing really sleeps.
func TestWriteWaitsForAReaderToClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sleeps := 0
	useRetrySleep(t, func(time.Duration) {
		sleeps++
		_ = reader.Close()
	})

	if err := Write(path, []byte("new"), 0600); err != nil {
		t.Fatalf("Write with a reader that closes within the budget: %v", err)
	}
	if sleeps == 0 {
		t.Fatal("the replace never had to wait: an open reader no longer holds it off, and this test proves nothing")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "new" {
		t.Fatalf("content after the replace = %q, %v", got, err)
	}
}

// A reader that never lets go outlasts the budget, and the replace fails with the
// error Windows gave, leaving the old content and no temporary file behind.
func TestWriteFailsWhenAReaderOutlastsTheBudget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var waited time.Duration
	useRetrySleep(t, func(d time.Duration) { waited += d })

	err = Write(path, []byte("new"), 0600)
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("Write = %v, want the access error Windows gave", err)
	}
	if waited < retryBudget {
		t.Fatalf("gave up after %v, before the %v budget", waited, retryBudget)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != "old" {
		t.Fatalf("content after the failed replace = %q, %v", got, readErr)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("temporary file leaked: %s", entry.Name())
		}
	}
}

func useRetrySleep(t *testing.T, sleep func(time.Duration)) {
	t.Helper()
	previous := retrySleep
	retrySleep = sleep
	t.Cleanup(func() { retrySleep = previous })
}
