//go:build windows

package profile

import (
	"os"
	"path/filepath"
)

func profileLockLocation(root string) (string, int) {
	return filepath.Join(root, ".registry.lock"), os.O_CREATE | os.O_RDWR
}
