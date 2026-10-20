//go:build !windows

package filesec

import "os"

// WritableByOthers reports whether users other than the owner can change the
// file or directory at path: whether info, from os.Lstat(path), carries a group
// or world write bit.
func WritableByOthers(_ string, info os.FileInfo) (bool, error) {
	return info.Mode().Perm()&0022 != 0, nil
}
