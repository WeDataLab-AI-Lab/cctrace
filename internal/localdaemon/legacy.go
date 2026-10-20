package localdaemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

var ErrMigrationPending = errors.New("legacy sync ownership cannot be verified; migration pending")

// AcquireProfile preserves the legacy lock and cursor path. A cooperative stop
// is addressed only to the nonce whose PID is the kernel-observed lock owner.
// An unsupported owner remains untouched; there is no PID signal fallback.
func AcquireProfile(ctx context.Context, dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := safeDirectory(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "sync.lock")
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return nil, ErrMigrationPending
	}
	lock := flock.New(path)
	locked, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if locked {
		return func() { _ = lock.Unlock() }, nil
	}
	var rt Runtime
	if ReadJSON(filepath.Join(dir, "sync-runtime.json"), &rt) != nil || rt.PID <= 0 || rt.InstanceID == "" || rt.StartedAt.IsZero() || rt.StartedAt.After(time.Now()) {
		return nil, ErrMigrationPending
	}
	b, err := os.ReadFile(filepath.Join(dir, "sync.pid"))
	if err != nil || len(b) > 64 {
		return nil, ErrMigrationPending
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid != rt.PID {
		return nil, ErrMigrationPending
	}
	if !verifiedOwner(ctx, path, rt.PID) {
		return nil, ErrMigrationPending
	}
	var latest Runtime
	if ReadJSON(filepath.Join(dir, "sync-runtime.json"), &latest) != nil || latest.InstanceID != rt.InstanceID || latest.PID != rt.PID {
		return nil, ErrMigrationPending
	}
	request := struct {
		InstanceID  string    `json:"instance_id"`
		RequestedAt time.Time `json:"requested_at"`
	}{rt.InstanceID, time.Now().UTC()}
	if err := writeJSON(filepath.Join(dir, "sync-stop.json"), request); err != nil {
		return nil, err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			locked, err := lock.TryLock()
			if err != nil {
				return nil, err
			}
			if locked {
				var receipt struct {
					InstanceID string    `json:"instance_id"`
					ExitedAt   time.Time `json:"exited_at"`
				}
				if ReadJSON(filepath.Join(dir, "sync-exit.json"), &receipt) != nil || receipt.InstanceID != rt.InstanceID || receipt.ExitedAt.Before(rt.StartedAt) || receipt.ExitedAt.After(time.Now()) {
					_ = lock.Unlock()
					return nil, ErrMigrationPending
				}
				return func() { _ = lock.Unlock() }, nil
			}
			// A different process won the handoff. Preserve it and report a conflict.
			if observed := ownerPID(path); observed > 0 && observed != rt.PID {
				return nil, ErrMigrationPending
			}
		}
	}
}
