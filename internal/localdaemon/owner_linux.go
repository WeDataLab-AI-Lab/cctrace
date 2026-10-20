//go:build linux

package localdaemon

import (
	"context"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func verifiedOwner(_ context.Context, path string, pid int) bool { return ownerPID(path) == pid }

func ownerPID(path string) int {
	i, err := os.Lstat(path)
	if err != nil || !i.Mode().IsRegular() || i.Mode()&os.ModeSymlink != 0 {
		return 0
	}
	st, ok := i.Sys().(*syscall.Stat_t)
	if !ok {
		return 0
	}
	b, err := os.ReadFile("/proc/locks")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 6 || f[1] != "FLOCK" || f[3] != "WRITE" {
			continue
		}
		key := strings.Split(f[5], ":")
		if len(key) != 3 {
			continue
		}
		major, e1 := strconv.ParseUint(key[0], 16, 32)
		minor, e2 := strconv.ParseUint(key[1], 16, 32)
		inode, e3 := strconv.ParseUint(key[2], 10, 64)
		if e1 == nil && e2 == nil && e3 == nil && major == uint64(unix.Major(uint64(st.Dev))) && minor == uint64(unix.Minor(uint64(st.Dev))) && inode == st.Ino {
			pid, _ := strconv.Atoi(f[4])
			return pid
		}
	}
	return 0
}
