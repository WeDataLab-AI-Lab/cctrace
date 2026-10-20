//go:build !windows

package profile

import "os"

func profileLockLocation(root string) (string, int) { return root, os.O_RDONLY }
