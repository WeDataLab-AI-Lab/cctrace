//go:build !darwin && !linux

package desktop

import (
	"errors"
	"os"
)

// Do not substitute an ordinary open on platforms where the required no-follow
// and nonblocking guarantees have not been implemented. System-only profiles
// still work because they never request a CA file open.
func openUpdateTrustCA(string) (*os.File, error) {
	return nil, errors.New("safe CA file opening is unsupported")
}
