//go:build darwin

package desktop

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func probeDaemonLock(path string) daemonLockState {
	return probeDaemonLockWithHook(path, nil)
}

func probeDaemonLockWithHook(path string, afterQuery func()) daemonLockState {
	before, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return daemonLockMissing
	}
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return daemonLockUnknown
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return daemonLockUnknown
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return daemonLockUnknown
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return daemonLockUnknown
	}
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 0}
	if err := unix.FcntlFlock(file.Fd(), unix.F_GETLK, &lock); err != nil {
		return daemonLockUnknown
	}
	if afterQuery != nil {
		afterQuery()
	}
	afterOpen, openErr := file.Stat()
	afterPath, pathErr := os.Lstat(path)
	if openErr != nil || pathErr != nil || afterPath.Mode()&os.ModeSymlink != 0 || !afterPath.Mode().IsRegular() ||
		!os.SameFile(before, afterOpen) || !os.SameFile(before, afterPath) ||
		before.Size() != afterOpen.Size() || before.Size() != afterPath.Size() ||
		!before.ModTime().Equal(afterOpen.ModTime()) || !before.ModTime().Equal(afterPath.ModTime()) {
		return daemonLockUnknown
	}
	switch lock.Type {
	case unix.F_UNLCK:
		return daemonLockFree
	case unix.F_WRLCK:
		return daemonLockHeld
	default:
		// A shared lock does not prove ownership of the CLI daemon.
		return daemonLockUnknown
	}
}
