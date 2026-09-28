package projectrule

import (
	"context"
	"errors"
	"sync"

	"cctrace/internal/store"
)

// ScanFunc is the Scan signature, so callers can pass a test double.
type ScanFunc func(context.Context, ScanOptions) ([]*store.ProjectRuleSnapshot, error)

// ErrScanInFlight is returned when a walk for the same agent+root is still
// running from an earlier, abandoned call.
var ErrScanInFlight = errors.New("project rule scan already in flight")

var (
	inFlightMu sync.Mutex
	inFlight   = make(map[string]struct{})
)

// RunBounded runs scan in its own goroutine and returns as soon as ctx is done,
// whether or not the scan noticed. Scan's own deadline handling is cooperative
// and is only observed between syscalls, so a walk parked in an uninterruptible
// readdir (stalled network or cloud-sync mount) never returns and would hold the
// entire sync pass behind it — which is exactly how a production daemon was
// found stuck in fdopendir. Go cannot kill that goroutine, so it is abandoned;
// it exits on its own if the filesystem ever answers.
//
// Abandoned goroutines are not allowed to accumulate: while one is still
// running, further calls for the same agent+root return ErrScanInFlight
// immediately instead of starting another walk behind it.
func RunBounded(ctx context.Context, opts ScanOptions, scan ScanFunc) ([]*store.ProjectRuleSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	key := opts.Agent + "|" + opts.RepositoryRoot

	inFlightMu.Lock()
	if _, busy := inFlight[key]; busy {
		inFlightMu.Unlock()
		return nil, ErrScanInFlight
	}
	inFlight[key] = struct{}{}
	inFlightMu.Unlock()

	type result struct {
		snapshots []*store.ProjectRuleSnapshot
		err       error
	}
	// Buffered: the send must not block once this call has walked away.
	done := make(chan result, 1)
	go func() {
		snapshots, err := scan(ctx, opts)
		inFlightMu.Lock()
		delete(inFlight, key)
		inFlightMu.Unlock()
		done <- result{snapshots: snapshots, err: err}
	}()

	select {
	case r := <-done:
		return r.snapshots, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
