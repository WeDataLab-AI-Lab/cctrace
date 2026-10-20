//go:build !windows

package configtxn

import (
	"errors"
	"os"
)

func sameRestoreMode(current, want os.FileMode) bool { return current == want }

func changeModeDurably(path string, mode os.FileMode) error {
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	// POSIX fsync accepts a read-only descriptor, including a final 0400 mode.
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
