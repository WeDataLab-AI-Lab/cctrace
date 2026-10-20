//go:build windows

package localdaemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"

	"cctrace/internal/filesec/filesectest"
)

func TestEveryoneWriteGrantMakesDaemonFilesUnsafe(t *testing.T) {
	root := t.TempDir()
	filesectest.Grant(t, root, windows.WinWorldSid, windows.FILE_GENERIC_WRITE)
	if err := safeDirectory(root); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Errorf("a directory Everyone can write: safeDirectory = %v, want the unsafe refusal", err)
	}
	if _, err := Running(root); err == nil {
		t.Error("Running accepted a root Everyone can write")
	}

	root = t.TempDir()
	path := filepath.Join(root, "daemon-runtime.json")
	if err := writeJSON(path, Runtime{PID: 1, InstanceID: "instance"}); err != nil {
		t.Fatal(err)
	}
	filesectest.Grant(t, path, windows.WinWorldSid, windows.FILE_GENERIC_WRITE)
	if _, err := ReadRuntime(root); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Errorf("a file Everyone can write: ReadRuntime = %v, want the unsafe refusal", err)
	}
}

// The daemon replaces its status file while status readers read it, and each holds
// the other off for a moment: a reader's open without FILE_SHARE_DELETE makes the
// replace fail until it lets go, and the replace makes an open that comes in during
// it fail with a sharing violation. atomicfile waits both out, so neither a publish
// nor a read fails because of it.
func TestPublishingWhileReadingNeverFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon-status.json")
	if err := writeJSON(path, Runtime{PID: 1, InstanceID: "instance"}); err != nil {
		t.Fatal(err)
	}
	const publishes = 300
	published := make(chan error, 1)
	go func() {
		for i := 1; i <= publishes; i++ {
			if err := writeJSON(path, Runtime{PID: i, InstanceID: "instance"}); err != nil {
				published <- err
				return
			}
		}
		published <- nil
	}()
	for {
		select {
		case err := <-published:
			if err != nil {
				t.Fatalf("publish while the file was being read: %v", err)
			}
			return
		default:
		}
		// Seeing a replacement is allowed; any other failure to read is not.
		var rt Runtime
		if err := ReadJSON(path, &rt); err != nil && !errors.Is(err, ErrMetadataChanged) {
			t.Fatalf("read while the file was being published: %v", err)
		}
	}
}
