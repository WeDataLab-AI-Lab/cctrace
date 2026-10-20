//go:build !windows

package atomicfile

import (
	"errors"
	"testing"
)

// A POSIX rename replaces a file under its readers, so an open is never held off
// and there is nothing to wait for: RetryBusy is one attempt.
func TestRetryBusyAttemptsOnceOutsideWindows(t *testing.T) {
	failure := errors.New("no such file")
	attempts := 0
	err := RetryBusy(func() error { attempts++; return failure })
	if err != failure || attempts != 1 {
		t.Fatalf("RetryBusy = %v after %d attempts, want %v after 1", err, attempts, failure)
	}
}
