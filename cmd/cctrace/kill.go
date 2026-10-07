package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// killGraceWait bounds how long the command waits for the sync lock to come
// free after signalling. It is short on purpose: the process has already been
// signalled, so this measures the OS reaping it, not the daemon winding down.
// It is a var so the test for the branch that gives up can reach that branch
// without spending the full grace period; production never assigns to it.
var killGraceWait = 5 * time.Second

var (
	syncLockFreeFn = isSyncLockFree
	killProcessFn  = killProcessByPID
)

// killCmd force-terminates the running sync daemon.
//
// `sync --stop` already asks the daemon to finish its pass and exit, and that
// is the right way to stop one. This exists for when it cannot be: on Windows a
// running binary cannot be replaced, so an unresponsive daemon blocks its own
// update, and the alternative the docs had to offer was Stop-Process by name --
// which matches every cctrace on the machine, including another profile's.
func killCmd() *cobra.Command {
	var profileName string

	cmd := &cobra.Command{
		Use:   "kill",
		Short: "Force-terminate the running sync daemon (prefer 'sync --stop')",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runKill(profileName)
		},
	}

	cmd.Flags().StringVar(&profileName, "profile", "", "Named profile to use (overrides CCTRACE_PROFILE env var)")
	return cmd
}

// runKill terminates the daemon holding profileName's sync lock.
//
// The lock decides whether anything is killed, not the recorded pid. A pid
// lives in a file, and a file outlives the process it describes: after a crash
// the recorded number can belong to something else by the time anyone runs
// this. Signalling it then kills an unrelated process. A held lock is proof
// that the daemon that took it is still alive, so the pid is only trusted while
// the lock says the process behind it exists.
func runKill(profileName string) error {
	if profileName == "" {
		profileName = os.Getenv("CCTRACE_PROFILE")
	}

	free, err := syncLockFreeFn(profileName)
	if err != nil {
		return fmt.Errorf("check sync lock: %w", err)
	}
	if free {
		// Not an error: "stop what is running" is satisfied by nothing running.
		// The files are still cleared, because a stale runtime file is what makes
		// the next `status` claim a daemon that is not there.
		clearSyncBookkeeping(profileName)
		fmt.Println("  No running sync daemon")
		return nil
	}

	runtimeState, _, err := readSyncRuntime(profileName)
	if err != nil {
		return fmt.Errorf("read sync runtime: %w", err)
	}
	pid, ok, err := currentWatcherPID(profileName, runtimeState)
	if err != nil {
		return fmt.Errorf("resolve daemon pid: %w", err)
	}
	if !ok || pid <= 0 {
		// The lock is held by a process this profile has no pid for. Killing
		// something else on a guess is worse than saying so.
		return fmt.Errorf("a sync daemon holds the lock but its pid is unknown; stop it with 'cctrace sync --stop'")
	}

	if err := killProcessFn(pid); err != nil {
		return fmt.Errorf("terminate pid %d: %w", pid, err)
	}

	stopped, err := waitForSyncLockFree(profileName, killGraceWait)
	if err != nil {
		return fmt.Errorf("confirm daemon exit: %w", err)
	}
	if !stopped {
		// Reported rather than assumed. The caller's next step is usually
		// replacing the binary, which fails confusingly if it believes a stop
		// happened that did not.
		return fmt.Errorf("signalled pid %d but the sync lock is still held", pid)
	}

	clearSyncBookkeeping(profileName)
	fmt.Printf("  Killed sync daemon (pid %d)\n", pid)
	return nil
}

func waitForSyncLockFree(profileName string, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		free, err := syncLockFreeFn(profileName)
		if err != nil {
			return false, err
		}
		if free {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		time.Sleep(syncPollInterval)
	}
}

// clearSyncBookkeeping removes the files that describe a daemon which is no
// longer running.
//
// A file that was never there is the normal case and stays silent. Anything
// else is reported: issue #449 saw sync-stop.json survive a kill on Windows,
// and because every error was discarded the CI log recorded that the file was
// present and nothing about why it could not be removed. Reporting does not
// change the outcome -- the command still succeeds and nothing is retried --
// it only leaves the cause where the next failure can be read from it.
func clearSyncBookkeeping(profileName string) {
	for _, path := range []string{
		runtimeFilePath(profileName),
		pidFilePath(profileName),
		stopRequestFilePath(profileName),
	} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			diagf("kill: remove %s: %v", path, err)
		}
	}
}

// killProcessByPID terminates a process without asking it to cooperate.
// os.Process.Kill is SIGKILL on unix and TerminateProcess on Windows, which is
// what makes this work where a stop request does not.
func killProcessByPID(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
