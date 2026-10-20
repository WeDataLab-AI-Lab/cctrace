//go:build windows

package configtxn

import (
	"errors"
	"os"
)

// Windows Chmod represents only the owner-write bit (FILE_ATTRIBUTE_READONLY).
func sameRestoreMode(current, want os.FileMode) bool { return current&0200 == want&0200 }

func changeModeDurably(path string, mode os.FileMode) error {
	// FlushFileBuffers requires GENERIC_WRITE. Open before setting read-only so
	// the final attribute does not prevent obtaining that handle. When restoring
	// writable mode, clear the old read-only attribute before opening instead.
	if mode&0200 != 0 {
		if err := os.Chmod(path, mode); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	if mode&0200 == 0 {
		if err := os.Chmod(path, mode); err != nil {
			return errors.Join(err, f.Close())
		}
	}
	return errors.Join(f.Sync(), f.Close())
}
