//go:build !darwin

package desktop

import (
	"errors"
	"os"
)

func probeDaemonLock(path string) daemonLockState {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return daemonLockMissing
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return daemonLockUnknown
	}
	// F_GETLK does not observe the BSD flock ownership used by cctrace on
	// unsupported platforms; an existing lock file therefore proves neither state.
	return daemonLockUnknown
}
