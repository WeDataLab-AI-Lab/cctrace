package main

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

const (
	// daemonLockAcquireWait absorbs the instant after a lock comes free: a
	// probe that is polling can take it for the moment before it lets go, and a
	// single TryLock that lands in that window reports "already running" for a
	// daemon that is not there. cmd/cctrace/sync.go:1057 is the record of that
	// accident.
	daemonLockAcquireWait = 1 * time.Second
	lockPollInterval      = 100 * time.Millisecond
)

var (
	errDaemonAlreadyRunning = errors.New("another cctraced is already running")
	errDaemonLockStillHeld  = errors.New("daemon lock is still held")
)

// The lock is the only state the kernel drops when the process holding it dies,
// which is what makes it -- and not the pid file -- the answer to "is a daemon
// running". flock is BSD-style on darwin/linux (per open file description) and
// LockFileEx on Windows; aix/solaris fcntl semantics are not a build target.
func lockFilePath() string {
	return filepath.Join(cctraceDir(), "cctraced.lock")
}

// The control lock serializes startup's "take the lock, then write the pid"
// against --stop's "decide, read the pid, signal". Without it --stop can see a
// held lock in the instant before the daemon has written its pid and act on the
// previous generation's number.
func controlLockFilePath() string {
	return filepath.Join(cctraceDir(), "cctraced.control.lock")
}

// acquireDaemonLock is for startup only: it creates ~/.cctrace and the lock
// file if they are missing.
func acquireDaemonLock() (*flock.Flock, error) {
	return acquireStartupLock(lockFilePath(), daemonLockAcquireWait)
}

// acquireControlLock is reached from --stop as well, but only once the lock
// file -- and therefore the directory -- already exists, so it never brings
// ~/.cctrace into being on a machine that has never run a daemon.
func acquireControlLock(wait time.Duration) (*flock.Flock, error) {
	return acquireStartupLock(controlLockFilePath(), wait)
}

// acquireStartupLock is cmd/cctrace/sync.go:976 acquireFileLock, kept separate
// because that one is in the client's package and carries the client's sentinel.
//
// The permissions match the pid file's 0644 but are not a claim to solve
// anything across uids: an existing 0600 file is not chmod'ed, umask still
// applies, and Windows ignores the mode entirely.
func acquireStartupLock(path string, wait time.Duration) (*flock.Flock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	fl := flock.New(path, flock.SetPermissions(0644))
	deadline := time.Now().Add(wait)
	for {
		locked, err := fl.TryLock()
		if err != nil {
			return nil, err
		}
		if locked {
			return fl, nil
		}
		if wait <= 0 || time.Now().After(deadline) {
			return nil, errDaemonAlreadyRunning
		}
		time.Sleep(lockPollInterval)
	}
}

// probeDaemonLock reports what the daemon lock says, without creating it.
// SetFlag replaces the open flags rather than adding to them, so dropping
// O_CREATE is what keeps --stop from leaving a lock file behind on a machine
// where no daemon has ever run.
//
// Returns (nil, false, nil) when the file does not exist, (nil, true, nil) when
// a daemon holds it, and (fl, true, nil) when it is free -- in that last case
// the caller holds the lock and must unlock it, and can clean up underneath it
// knowing no daemon can write a pid meanwhile.
func probeDaemonLock() (*flock.Flock, bool, error) {
	fl := flock.New(lockFilePath(), flock.SetFlag(os.O_RDONLY), flock.SetPermissions(0644))
	locked, err := fl.TryLock()
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if locked {
		return fl, true, nil
	}
	return nil, true, nil
}

// waitForDaemonLockFree polls until the daemon lock comes free and returns the
// handle that took it, so the caller can clean up while holding it. A nil
// handle with a nil error means the lock file itself is gone -- there is
// nothing left to hold.
func waitForDaemonLockFree(timeout time.Duration) (*flock.Flock, error) {
	deadline := time.Now().Add(timeout)
	for {
		fl, exists, err := probeDaemonLock()
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, nil
		}
		if fl != nil {
			return fl, nil
		}
		if time.Now().After(deadline) {
			return nil, errDaemonLockStillHeld
		}
		time.Sleep(lockPollInterval)
	}
}

// The lock file is never removed. Unlinking it splits the inode: the running
// daemon keeps its fd and its lock while the path disappears, so the next
// daemon locks a fresh inode and both run at once.
