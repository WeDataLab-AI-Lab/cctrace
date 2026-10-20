package localdaemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

// lineLocksDir holds one lock file per route, named by its id.
const lineLocksDir = "route-locks"

// ErrLineBusy is returned when a line is being read by another run.
var ErrLineBusy = errors.New("collection source is in use by another run")

// ErrRouteChanged is returned when a route is not, any more, an active route of
// the profile that asked.
var ErrRouteChanged = errors.New("route is no longer held by that profile")

// lineLockRetry is how often a run that waits for a lock tries it again.
var lineLockRetry = 5 * time.Millisecond

// removeLineLock is a seam: Windows refuses to remove a file that is open, which
// tests need to produce elsewhere.
var removeLineLock = os.Remove

// LockLines takes the lock of every route in ids, or none of them, and returns
// the function that gives them up (safe to call more than once). Whoever reads a
// line holds its lock for as long as it reads: a daemon's job for a generation,
// a one-shot run for a pass. The kernel releases the lock if the process dies.
//
// wait is how long to wait for the locks, all together; zero tries once. A lock
// still held then is ErrLineBusy. The caller's own cancellation is returned as
// itself, never as a busy line. The locks are taken in id order, so two runs that
// wait for the same lines cannot wait for each other.
//
// A lock proves nobody else is reading the line. It does not prove the caller
// may: ownership can change while it waits, so a caller that waited must read the
// route again with ConfirmRoute.
func LockLines(ctx context.Context, root string, ids []string, wait time.Duration) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	for _, id := range ids {
		if !isHex(id, 8) {
			return nil, errors.New("invalid route id")
		}
	}
	if len(ids) == 0 {
		return func() {}, nil
	}
	dir := filepath.Join(root, lineLocksDir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := safeDirectory(dir); err != nil {
		return nil, err
	}
	waitCtx := ctx
	if wait > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, wait)
		defer cancel()
	}
	var held []*flock.Flock
	var once sync.Once
	release := func() {
		once.Do(func() {
			for i := len(held) - 1; i >= 0; i-- {
				_ = held[i].Unlock()
			}
		})
	}
	for _, id := range ids {
		path := filepath.Join(dir, id+".lock")
		// Do not follow a lock file somebody planted as a link.
		if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
			release()
			return nil, errors.New("unsafe route lock")
		}
		lock := flock.New(path)
		var locked bool
		var err error
		if wait > 0 {
			locked, err = lock.TryLockContext(waitCtx, lineLockRetry)
		} else {
			locked, err = lock.TryLock()
		}
		if locked {
			held = append(held, lock)
			continue
		}
		release()
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case err == nil || errors.Is(err, context.DeadlineExceeded):
			return nil, fmt.Errorf("%w (route %s)", ErrLineBusy, id)
		}
		return nil, err
	}
	return release, nil
}

// ErrHandoffPending is returned when a route has just been handed to the profile
// and its cursor not yet taken over: the profile's daemon job reads the line
// first, from the cursor the last reader left.
var ErrHandoffPending = errors.New("route was just handed over; the daemon takes its cursor over first")

// ConfirmRoute reads the routes file again and returns the route id if it is
// still active and owned by owner. A run calls it after it holds the line's lock:
// the owner may have changed between the decision to read the line and the lock,
// and from the confirmation to the end of the pass nobody can change it, because
// publishing a new owner needs the same lock. ErrRouteChanged means the run must
// leave the line alone, and so does ErrHandoffPending, for a route whose cursor
// is still to be taken over; any other error means the file could not be read,
// and nothing is confirmed.
func ConfirmRoute(root, id, owner string) (Route, error) {
	f, err := ReadRoutes(root)
	if errors.Is(err, os.ErrNotExist) {
		return Route{}, fmt.Errorf("%w: no routes file", ErrRouteChanged)
	}
	if err != nil {
		return Route{}, err
	}
	for _, r := range f.Routes {
		if r.ID == id {
			if r.State == RouteActive && owner != "" && r.Owner == owner {
				if r.Handoff != "" {
					return Route{}, fmt.Errorf("%w (route %s)", ErrHandoffPending, id)
				}
				return r, nil
			}
			break
		}
	}
	return Route{}, fmt.Errorf("%w (route %s)", ErrRouteChanged, id)
}

// PruneLineLocks removes the lock files of gone, the routes the file had and has
// no longer, and returns those a run still holds, to be tried again. Removing the
// file under a run that has just opened or locked it is harmless: that run
// confirms its route after locking and finds it gone. A lock file of an id no
// route had is not one to remove: it is what a run outside the daemon took for a
// line no route covers (SourceLockID), and such a run finds no route to confirm,
// so another run could take the same lock on a new file while it reads.
func PruneLineLocks(root string, gone []string) (held []string, err error) {
	dir := filepath.Join(root, lineLocksDir)
	if err := safeDirectory(dir); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var errs []error
	for _, id := range gone {
		path := filepath.Join(dir, id+".lock")
		if info, err := os.Lstat(path); !isHex(id, 8) || err != nil || !info.Mode().IsRegular() {
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
			continue
		}
		lock := flock.New(path)
		locked, err := lock.TryLock()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !locked {
			held = append(held, id)
			continue
		}
		// Windows does not remove a file that is open, and the lock keeps it open:
		// giving the lock up closes the file.
		_ = lock.Unlock()
		if err := removeLineLock(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return held, errors.Join(errs...)
}
