//go:build windows

package atomicfile

import (
	"errors"

	"golang.org/x/sys/windows"
)

func replace(tmpName string, path string) error {
	from, err := windows.UTF16PtrFromString(tmpName)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return retryTransient(func() error {
		return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
	}, replaceBusy, retrySleep)
}

// replaceBusy is what Windows answers when another handle on the destination was
// opened without FILE_SHARE_DELETE, which is how Go's os.Open opens every file:
// a reader holds the replace off for as long as it reads. A POSIX rename does not
// wait for readers, so nothing else in this package has to.
func replaceBusy(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
