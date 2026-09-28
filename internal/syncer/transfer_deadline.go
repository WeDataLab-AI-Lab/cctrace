package syncer

import "time"

// Deadlines for a request are a function of how much it carries, not a constant.
//
// A fixed wait makes the server's configured size limit a claim it cannot keep:
// the operator raises CCTRACE_MAX_SYNC_BODY_BYTES, the batch is accepted in
// principle, and the transfer still fails because neither side would wait long
// enough. That failure surfaces as a transport error with no HTTP status, so it
// reads like an unreachable server and the 413-driven batch split has nothing to
// react to.
//
// assumedFloorBytesPerSecond is the link speed the deadline is sized for. It is
// deliberately low -- a remote client on a poor connection is the case the fixed
// wait failed -- and it is an assumption, not a measurement: a slower link than
// this still fails, just later.
const (
	baseTransferDeadline       = 30 * time.Second
	assumedFloorBytesPerSecond = 1 << 20
	maxTransferDeadline        = 5 * time.Minute
)

// transferDeadline is how long one request of size bytes is given, end to end.
// Bounded at both ends: the base keeps the common small batch on the wait it
// already had, and the cap keeps a slow sender from holding a connection for an
// unbounded time.
func transferDeadline(size int64) time.Duration {
	if size < 0 {
		size = 0
	}
	d := baseTransferDeadline + time.Duration(size/assumedFloorBytesPerSecond)*time.Second
	if d > maxTransferDeadline {
		return maxTransferDeadline
	}
	return d
}
