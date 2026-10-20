//go:build darwin

package localdaemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func verifiedOwner(ctx context.Context, path string, pid int) bool {
	if owner := ownerPID(path); owner > 0 {
		return owner == pid
	}
	if ownerPID(path) != -1 {
		return false
	}
	// Darwin reports PID -1 for flock locks. Bind the exact executable's watch
	// invocation and open lock descriptor, then require the nonce/lock handoff.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		return false
	}
	comm, err := exec.CommandContext(ctx, "/bin/ps", "-ww", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil || canonicalSource(strings.TrimSpace(string(comm))) != canonicalSource(executable) {
		return false
	}
	args, err := exec.CommandContext(ctx, "/bin/ps", "-ww", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return false
	}
	words := strings.Fields(string(args))
	watch, sync := false, false
	for _, word := range words {
		watch = watch || word == "--watch"
		sync = sync || word == "sync"
	}
	if !watch || !sync {
		return false
	}
	b, err := exec.CommandContext(ctx, "/usr/sbin/lsof", "-nP", "-a", "-p", strconv.Itoa(pid), "-F", "pfnl", path).Output()
	if err != nil || len(b) > 64<<10 {
		return false
	}
	process, descriptor, name := "", false, ""
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			process = line[1:]
		case 'f':
			descriptor = true
			name = ""
		case 'n':
			name = line[1:]
		}
		if process == strconv.Itoa(pid) && descriptor && canonicalSource(name) == canonicalSource(filepath.Clean(path)) {
			return true
		}
	}
	return false
}

func ownerPID(path string) int {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return 0
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return 0
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil {
		return 0
	}
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 0}
	if unix.FcntlFlock(uintptr(fd), unix.F_GETLK, &lock) != nil || lock.Type != unix.F_WRLCK {
		return 0
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) {
		return 0
	}
	return int(lock.Pid)
}
