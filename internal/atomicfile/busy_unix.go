//go:build !windows

package atomicfile

// RetryBusy runs attempt, the open of a file another process may be replacing.
// A POSIX rename replaces a file under its readers, so an open is never held off
// and there is nothing to wait for: it is one attempt.
func RetryBusy(attempt func() error) error { return attempt() }
