//go:build !linux && !darwin

package api

// diskUsageSupported is false where diskUsage is the no-op stub; disk-dependent
// tests skip their assertions on these platforms.
const diskUsageSupported = false

// diskUsage falls back to "no data" on every platform except linux/darwin (the
// two whose syscall.Statfs_t layout diskUsage is verified against). The admin
// volume panel is simply omitted there; cctraced runs on linux in production.
func diskUsage(path string) (total, free, used uint64, err error) {
	return 0, 0, 0, nil
}
