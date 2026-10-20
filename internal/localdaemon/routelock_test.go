package localdaemon

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func lockLines(t *testing.T, root string, wait time.Duration, ids ...string) (func(), error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return LockLines(ctx, root, ids, wait)
}

func mustLock(t *testing.T, root string, ids ...string) func() {
	t.Helper()
	release, err := lockLines(t, root, 0, ids...)
	if err != nil {
		t.Fatalf("lock %v: %v", ids, err)
	}
	t.Cleanup(release)
	return release
}

func TestLineLockExcludesAnotherRunOfTheSameLine(t *testing.T) {
	root := t.TempDir()
	release := mustLock(t, root, "0000000a")
	if _, err := lockLines(t, root, 0, "0000000a"); !errors.Is(err, ErrLineBusy) {
		t.Fatalf("a line read by one run cannot be taken by another: %v", err)
	}
	other := mustLock(t, root, "0000000b")
	other()
	release()
	again := mustLock(t, root, "0000000a")
	again()
	release() // a second release is harmless
}

func TestLineLockIsAllOrNothing(t *testing.T) {
	root := t.TempDir()
	held := mustLock(t, root, "0000000b")
	if _, err := lockLines(t, root, 0, "0000000c", "0000000a", "0000000b"); !errors.Is(err, ErrLineBusy) {
		t.Fatalf("err = %v", err)
	}
	// Nothing taken on the way is left behind.
	for _, id := range []string{"0000000a", "0000000c"} {
		mustLock(t, root, id)()
	}
	held()
	mustLock(t, root, "0000000a", "0000000b", "0000000c", "0000000b")() // duplicates are one lock
}

// A run that waits for its locks must not deadlock against another that wants
// the same lines in the other order; taking them in id order is what guarantees
// it.
func TestLineLockTakesLocksInIDOrder(t *testing.T) {
	root := t.TempDir()
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for _, order := range [][]string{{"00000001", "00000002"}, {"00000002", "00000001"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				release, err := LockLines(ctx, root, order, 5*time.Second)
				cancel()
				if err != nil {
					errs <- err
					return
				}
				release()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("two waiting runs deadlocked or timed out: %v", err)
	}
}

func TestLineLockWaitsForARelease(t *testing.T) {
	root := t.TempDir()
	release := mustLock(t, root, "0000000a")
	result := make(chan error, 1)
	go func() {
		got, err := lockLines(t, root, 5*time.Second, "0000000a")
		if err == nil {
			got()
		}
		result <- err
	}()
	release()
	if err := <-result; err != nil {
		t.Fatalf("a run that waits gets the line once it is free: %v", err)
	}
}

func TestLineLockGivesUpAfterItsWaitAndNamesWhy(t *testing.T) {
	root := t.TempDir()
	mustLock(t, root, "0000000a")
	start := time.Now()
	_, err := lockLines(t, root, 30*time.Millisecond, "0000000a")
	if !errors.Is(err, ErrLineBusy) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a wait that ran out is a busy line, not a timeout: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the wait did not end")
	}
}

func TestLineLockReportsACancelledCallerAsSuch(t *testing.T) {
	root := t.TempDir()
	mustLock(t, root, "0000000a")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := LockLines(ctx, root, []string{"0000000a"}, time.Second); !errors.Is(err, context.Canceled) || errors.Is(err, ErrLineBusy) {
		t.Fatalf("a cancelled run is not a busy line: %v", err)
	}
}

func TestLineLockRefusesWhatIsNotARouteID(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"", "../x", "ABCDEF01", "0000000", "000000000", "0000000g"} {
		if _, err := lockLines(t, root, 0, id); err == nil || errors.Is(err, ErrLineBusy) {
			t.Errorf("id %q: err = %v", id, err)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "route-locks")); len(entries) != 0 {
		t.Fatalf("a refused id created files: %v", entries)
	}
}

func TestLineLockFilesLiveInAPrivateDirectory(t *testing.T) {
	root := t.TempDir()
	mustLock(t, root, "0000000a")
	dir := filepath.Join(root, "route-locks")
	requirePrivate(t, dir, 0700)
	if _, err := os.Stat(filepath.Join(dir, "0000000a.lock")); err != nil {
		t.Fatal(err)
	}
}

func TestLineLockRefusesUnsafeLocations(t *testing.T) {
	t.Run("a symlinked directory", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Symlink(t.TempDir(), filepath.Join(root, "route-locks")); err != nil {
			t.Skip("symlinks unavailable")
		}
		if _, err := lockLines(t, root, 0, "0000000a"); err == nil || errors.Is(err, ErrLineBusy) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a group-writable directory", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "route-locks")
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		letGroupWrite(t, dir, 0770)
		if _, err := lockLines(t, root, 0, "0000000a"); err == nil || errors.Is(err, ErrLineBusy) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a symlinked lock file", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "route-locks")
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(dir, "0000000a.lock")); err != nil {
			t.Skip("symlinks unavailable")
		}
		if _, err := lockLines(t, root, 0, "0000000a"); err == nil || errors.Is(err, ErrLineBusy) {
			t.Fatalf("err = %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, "elsewhere")); err == nil {
			t.Fatal("the planted link was followed and its target created")
		}
	})
}

// The point of a line lock is to exclude another process: a one-shot sync is a
// separate process from the daemon.
func TestLineLockExcludesAnotherProcess(t *testing.T) {
	if root := os.Getenv("CCTRACE_LINE_LOCK_FIXTURE"); root != "" {
		release, err := LockLines(context.Background(), root, []string{"0000000a"}, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		os.Stdout.WriteString("held\n")
		_, _ = io.Copy(io.Discard, os.Stdin) // held until the parent closes our stdin
		return
	}
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLineLockExcludesAnotherProcess$")
	child.Env = []string{"PATH=" + os.Getenv("PATH"), "CCTRACE_LINE_LOCK_FIXTURE=" + root}
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close(); _ = child.Wait() })
	if n, err := stdout.Read(make([]byte, 5)); err != nil || n == 0 {
		t.Fatalf("the child never reported its lock: %v", err)
	}
	if _, err := lockLines(t, root, 0, "0000000a"); !errors.Is(err, ErrLineBusy) {
		t.Fatalf("another process's hold must exclude this one: %v", err)
	}
	stdin.Close()
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	mustLock(t, root, "0000000a")() // released with the process
}

func TestConfirmRouteSeesWhoOwnsTheLineNow(t *testing.T) {
	root, dir := t.TempDir(), canonicalSource(t.TempDir())
	owned := realRoute(dir, "0000000a", "a")
	owned.Owner = "default"
	moving := realRoute(dir, "0000000b", "b")
	moving.State, moving.To = RouteReleasing, "named:work"
	given := realRoute(dir, "0000000c", "c")
	given.Owner = "named:work"
	tomb := realRoute(dir, "0000000d", "d")
	tomb.Owner, tomb.State, tomb.From = "", RouteUnowned, "default"
	if _, err := WriteRoutes(root, RouteFile{Version: 1, Routes: []Route{owned, moving, given, tomb}}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, id, owner string
		ok              bool
	}{
		{"still the owner", "0000000a", "default", true},
		{"someone else owns it now", "0000000c", "default", false},
		{"being handed over", "0000000b", "default", false},
		{"nobody owns it", "0000000d", "default", false},
		{"the route is gone", "000000ff", "default", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := ConfirmRoute(root, tc.id, tc.owner)
			if tc.ok {
				if err != nil || r.ID != tc.id {
					t.Fatalf("route %+v, err %v", r, err)
				}
				return
			}
			if !errors.Is(err, ErrRouteChanged) {
				t.Fatalf("err = %v, want ErrRouteChanged", err)
			}
		})
	}
	t.Run("the owner changes after the first look", func(t *testing.T) {
		if _, err := ConfirmRoute(root, "0000000a", "default"); err != nil {
			t.Fatal(err)
		}
		read, _ := ReadRoutes(root)
		read.Routes[0].Owner = "named:work"
		if _, err := WriteRoutes(root, read); err != nil {
			t.Fatal(err)
		}
		if _, err := ConfirmRoute(root, "0000000a", "default"); !errors.Is(err, ErrRouteChanged) {
			t.Fatalf("a run that took the lock must find out it lost the line: %v", err)
		}
	})
}

// A route just handed to the profile is the profile's, but its first read is the
// daemon's job's, which takes over the cursor the last reader left; until then a
// run outside the daemon leaves the line alone.
func TestConfirmRouteWaitsForTheCursorToBeTakenOver(t *testing.T) {
	root, dir := t.TempDir(), canonicalSource(t.TempDir())
	given := realRoute(dir, "0000000a", "a")
	given.Owner, given.From, given.Handoff = "named:work", "default", strings.Repeat("ab", 16)
	if _, err := WriteRoutes(root, RouteFile{Version: 1, Routes: []Route{given}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfirmRoute(root, "0000000a", "named:work"); !errors.Is(err, ErrHandoffPending) {
		t.Fatalf("err = %v, want ErrHandoffPending", err)
	}
	read, _ := ReadRoutes(root)
	read.Routes[0].From, read.Routes[0].Handoff = "", ""
	if _, err := WriteRoutes(root, read); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfirmRoute(root, "0000000a", "named:work"); err != nil {
		t.Fatalf("once the cursor is taken over the line is the profile's to read: %v", err)
	}
}

func TestConfirmRouteCannotConfirmFromAnUnusableFile(t *testing.T) {
	root := t.TempDir()
	if _, err := ConfirmRoute(root, "0000000a", "default"); !errors.Is(err, ErrRouteChanged) {
		t.Fatalf("no file means no route: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "daemon-routes.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := ConfirmRoute(root, "0000000a", "default")
	var invalid *InvalidRoutesError
	if !errors.As(err, &invalid) || errors.Is(err, ErrRouteChanged) {
		t.Fatalf("an unreadable file is not an answer, and the caller must not take it for one: %v", err)
	}
}

func TestPruneLineLocksRemovesOnlyUnheldLocksOfDeletedRoutes(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"0000000a", "0000000b", "0000000c", "0000000d"} {
		mustLock(t, root, id)()
	}
	dir := filepath.Join(root, "route-locks")
	for _, name := range []string{"README", "0000000e.txt", "xyz.lock"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	busy := mustLock(t, root, "0000000c") // a run still holds a deleted route's line
	defer busy()
	gone := []string{"0000000a", "0000000c", "0000000d", "0000000f", "xyz"}
	held, err := PruneLineLocks(root, gone)
	if err != nil || !equalStrings(held, []string{"0000000c"}) {
		t.Fatalf("held %v, %v", held, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	// b is not among the routes that went: whoever holds its lock file, it stays.
	want := []string{"0000000b.lock", "0000000c.lock", "0000000e.txt", "README", "xyz.lock"}
	if !equalStrings(left, want) {
		t.Fatalf("left %v, want %v", left, want)
	}
	busy()
	if held, err := PruneLineLocks(root, held); err != nil || len(held) != 0 {
		t.Fatalf("held %v, %v", held, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "0000000c.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a lock that became free is removed on the next pass")
	}
}

// Windows does not remove a file that is open, and holding a lock keeps its file
// open. So the lock is given up, which closes the file, before the file goes.
func TestPruneLineLocksClosesALockFileBeforeRemovingIt(t *testing.T) {
	root := t.TempDir()
	mustLock(t, root, "0000000a")()
	old := removeLineLock
	removeLineLock = func(path string) error {
		// A file someone keeps open cannot be locked by another handle either.
		probe := flock.New(path)
		if locked, err := probe.TryLock(); err != nil || !locked {
			return errors.New("the file is still open")
		}
		_ = probe.Unlock()
		return old(path)
	}
	t.Cleanup(func() { removeLineLock = old })
	if _, err := PruneLineLocks(root, []string{"0000000a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "route-locks", "0000000a.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the lock file of a deleted route stays: %v", err)
	}
}

func TestPruneLineLocksWithoutADirectory(t *testing.T) {
	if _, err := PruneLineLocks(t.TempDir(), []string{"0000000a"}); err != nil {
		t.Fatal(err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
