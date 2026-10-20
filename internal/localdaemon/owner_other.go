//go:build !darwin && !linux

package localdaemon

import "context"

// Unsupported legacy ownership remains migration_pending. New singleton jobs
// still use the portable lock; no unverified process is stopped.
func ownerPID(string) int                             { return 0 }
func verifiedOwner(context.Context, string, int) bool { return false }
