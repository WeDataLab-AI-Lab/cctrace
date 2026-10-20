//go:build windows

package processio

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"cctrace/internal/filesec/filesectest"
)

// Go reports every Windows directory as 0777, so the owner-only mode bits the
// queue asks of its directory on Unix cannot be read here. A queue in a
// directory the user just created has to work all the same.
func TestQueueWorksInADirectoryTheUserCreated(t *testing.T) {
	queue := newTestQueue(t)
	report := queueReport("session-1", time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC))
	if err := queue.Upsert(report); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	items, err := queue.List()
	if err != nil || len(items) != 1 {
		t.Fatalf("List = %d reports, %v; want 1", len(items), err)
	}
	if err := queue.Ack(items[0]); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if items, err = queue.List(); err != nil || len(items) != 0 {
		t.Fatalf("List after Ack = %d reports, %v; want 0", len(items), err)
	}
}

// Read privacy is not checked on Windows, but a directory that Everyone can write
// would let another user plant queue entries the client then uploads.
func TestQueueRefusesADirectoryEveryoneCanWrite(t *testing.T) {
	queue := newTestQueue(t)
	filesectest.Grant(t, filepath.Dir(queue.Path), windows.WinWorldSid, windows.FILE_GENERIC_WRITE)
	if err := queue.Upsert(queueReport("session-1", time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC))); err == nil || !strings.Contains(err.Error(), "must be private") {
		t.Errorf("Upsert error = %v, want the private-directory refusal", err)
	}
	if _, err := queue.List(); err == nil || !strings.Contains(err.Error(), "must be private") {
		t.Errorf("List error = %v, want the private-directory refusal", err)
	}
}
