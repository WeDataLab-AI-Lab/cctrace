//go:build windows

package atomicfile

import "golang.org/x/sys/windows"

func replace(tmpName string, path string) error {
	from, err := windows.UTF16PtrFromString(tmpName)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
