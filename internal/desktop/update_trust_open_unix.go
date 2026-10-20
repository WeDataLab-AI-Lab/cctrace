//go:build darwin || linux

package desktop

import (
	"os"
	"syscall"
)

// Keep symlink rejection and nonblocking opens atomic with the open operation:
// lstat alone cannot protect against replacement before open.
func openUpdateTrustCA(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
