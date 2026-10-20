package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/gofrs/flock"

	"cctrace/internal/configtxn"
	"cctrace/internal/envgen"
	"cctrace/internal/profile"
)

// policyLockWait is the most one `config policy apply` or `check` run waits for
// locks, all of them together. It is a bound on waiting, not an estimate of how
// long a holder takes: a daemon runs a policy check every five minutes, and a
// holder that never lets go would otherwise leave one more waiting process behind
// each time. Two minutes keeps the waiters from piling up.
var policyLockWait = 2 * time.Minute

// policySettingsLockWait is the most a policy run waits for the locks of one set
// of settings files, within policyLockWait. The writers that hold them -- a sync
// startup repairing its settings, a Claude Code window, `cctrace init` -- read and
// replace one small file under the lock, about 10ms with the fsync dominating and
// under 20ms at the longest on the development machine
// (TestPolicySettingsLockHoldTimes). A run takes them while it holds the global
// policy lock, so every other profile's sync startup waits behind it; waiting
// longer for a lock that is normally held for milliseconds only holds those
// startups up, and the next run retries. Five seconds is two orders of magnitude
// over the writers' hold time, which leaves room for a slow disk, and well under
// the 35 seconds a startup waits for the global lock.
var policySettingsLockWait = 5 * time.Second

// policyLockPoll is how often a lock that is held is tried again.
const policyLockPoll = 100 * time.Millisecond

// policyLockBusyError is what a run reports when a lock stayed held past its wait.
type policyLockBusyError struct{ path string }

func (e *policyLockBusyError) Error() string { return "policy lock busy: " + e.path }

// lockPolicySettings uses the same locks as the ordinary Claude and Codex
// writers. A deterministic order also serializes profiles with shared homes.
// Preview skips missing parents; applying creates the Claude parent before
// locking, so another writer cannot create settings between snapshot and write.
func lockPolicySettings(ctx context.Context, p *profile.Profile, create bool) (func(), error) {
	release, _, err := lockPolicySettingsPaths(ctx, p, create)
	return release, err
}

// lockPolicySettingsPaths takes the locks of p's settings. A lock of the explicit
// Codex home that cannot be taken, for a reason other than being busy, is left
// out: nobody shares the home, so the policy goes on and leaves its block as it
// is. The map tells which locks are held.
func lockPolicySettingsPaths(ctx context.Context, p *profile.Profile, create bool) (func(), map[string]bool, error) {
	targets, explicit, err := policySettingsPaths(p)
	if err != nil {
		return nil, nil, err
	}
	release, locked, err := lockPolicyFiles(ctx, targets, create)
	var failed *policyPathError
	if errors.As(err, &failed) && explicit != "" && failed.path == explicit {
		targets = slices.DeleteFunc(slices.Clone(targets), func(path string) bool { return path == explicit })
		return lockPolicyFiles(ctx, targets, create)
	}
	return release, locked, err
}

// policyPathError is a failure to take the lock of one path, other than the lock
// being busy. Its text is the failure's own.
type policyPathError struct {
	path string
	err  error
}

func (e *policyPathError) Error() string { return e.err.Error() }
func (e *policyPathError) Unwrap() error { return e.err }

// lockPolicyFile takes the lock at path, trying again until ctx ends. A ctx that
// ran out of time reports *policyLockBusyError; one that was cancelled reports its
// own error, so a stop is not mistaken for a busy lock. The lock is tried once
// even when ctx has already ended.
func lockPolicyFile(ctx context.Context, path string) (*flock.Flock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	lock := flock.New(path)
	for {
		locked, err := lock.TryLock()
		if err != nil {
			return nil, err
		}
		if locked {
			return lock, nil
		}
		timer := time.NewTimer(policyLockPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, &policyLockBusyError{path}
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// lockPolicyFiles takes the writers' lock of each canonical path, in the order
// given, and reports which it holds. Without create, a path whose parent is
// missing is skipped: there is no file there to protect. A path naming a file
// already locked under another name shares that lock. The locks are waited for
// together, until ctx ends or policySettingsLockWait has passed.
func lockPolicyFiles(ctx context.Context, targets []string, create bool) (func(), map[string]bool, error) {
	ctx, cancel := context.WithTimeout(ctx, policySettingsLockWait)
	defer cancel()
	lockedPaths := map[string]bool{}
	var err error
	var locks []*flock.Flock
	var lockFiles []os.FileInfo
	release := func() {
		for i := len(locks) - 1; i >= 0; i-- {
			_ = locks[i].Unlock()
		}
	}
	for _, path := range targets {
		if create {
			err = os.MkdirAll(filepath.Dir(path), 0700)
		} else {
			_, err = os.Stat(filepath.Dir(path))
		}
		if !create && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			release()
			return nil, nil, &policyPathError{path, err}
		}
		// Do not follow a separately planted lock-file symlink.
		lockPath, e := canonicalPolicyPath(path + ".lock")
		if e != nil {
			release()
			return nil, nil, &policyPathError{path, e}
		}
		// flock(2) locks belong to the open file: a second open of a lock file
		// already held, under another name, would wait on the first. Every held
		// lock file exists, so one not created yet is not among them.
		if info, e := os.Stat(lockPath); e == nil && slices.ContainsFunc(lockFiles, func(held os.FileInfo) bool { return os.SameFile(held, info) }) {
			lockedPaths[path] = true
			continue
		}
		lock, e := lockPolicyFile(ctx, lockPath)
		if e != nil {
			release()
			// A busy lock and a cancelled wait are not this path's failure.
			var busy *policyLockBusyError
			if errors.As(e, &busy) || errors.Is(e, context.Canceled) {
				return nil, nil, e
			}
			return nil, nil, &policyPathError{path, e}
		}
		locks = append(locks, lock)
		info, e := os.Stat(lockPath)
		if e != nil {
			release()
			return nil, nil, &policyPathError{path, e}
		}
		lockFiles = append(lockFiles, info)
		lockedPaths[path] = true
	}
	return release, lockedPaths, nil
}

// policySettingsPaths lists the files whose locks a policy takes for p, sorted,
// and which of them is the explicit Codex home's config, if p has one the policy
// handles.
func policySettingsPaths(p *profile.Profile) (targets []string, explicit string, err error) {
	claude, err := envgen.ClaudeSettingsPath(p)
	if err != nil {
		return nil, "", err
	}
	claude, err = canonicalPolicyPath(claude)
	if err != nil {
		return nil, "", err
	}
	codex, err := policyCodexConfigPaths(p)
	if err != nil {
		return nil, "", err
	}
	// The explicit home's config is locked when the policy can name it. One it
	// cannot, a symlink, is not written, so it must not stop a run, a recovery or
	// a sync start from taking the locks.
	if home, ok := policyExplicitCodexHome(p, codex); ok && home.config != "" {
		codex = append(codex, home.config)
		explicit = home.config
	}
	targets = append([]string{claude}, codex...)
	sort.Strings(targets)

	return targets, explicit, nil
}

func validatePolicySettingsLocks(changes []configtxn.Change, locked map[string]bool) error {
	for _, change := range changes {
		base := filepath.Base(change.Path)
		if (base == "settings.json" || base == "config.toml") && !locked[change.Path] {
			return errors.New("settings paths changed; retry")
		}
	}
	return nil
}
