//go:build linux || darwin

package api

import "syscall"

// diskUsageSupported reports whether diskUsage returns real data on this build's
// platform (true on linux/darwin). Tests use it to skip disk-dependent asserts.
const diskUsageSupported = true

// diskUsage returns total, available, and used bytes of the filesystem
// containing path. On prod the cctraced container's WAL_DIR mount shares the
// /data partition with the DB volume, so statfs on it reports the storage the
// DB actually consumes. used is derived from Bfree (matching df's Used column),
// while free uses Bavail (space actually usable by the unprivileged process),
// so root-reserved blocks are not double-counted as used.
//
// The build tag is restricted to the platforms whose syscall.Statfs_t field
// types this code is verified against (linux = prod, darwin = dev). Every other
// GOOS — including the BSDs (differing Statfs_t field types) and solaris/illumos
// (Statvfs, not Statfs) — falls back to the no-op stub in disk_other.go.
func diskUsage(path string) (total, free, used uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, 0, err
	}
	bsize := uint64(st.Bsize) //nolint:unconvert // Bsize is int64 on linux, uint32 on darwin
	total = st.Blocks * bsize
	free = st.Bavail * bsize
	used = (st.Blocks - st.Bfree) * bsize
	return total, free, used, nil
}
