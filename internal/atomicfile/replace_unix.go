//go:build !windows

package atomicfile

import "os"

func replace(tmpName string, path string) error {
	return os.Rename(tmpName, path)
}
