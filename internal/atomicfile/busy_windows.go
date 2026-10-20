//go:build windows

package atomicfile

import (
	"errors"
	"time"

	"golang.org/x/sys/windows"
)

// retrySleep is how the retries wait between attempts; tests swap it so that no
// test sleeps.
var retrySleep = time.Sleep

// RetryBusy runs attempt, the open of a file another process may be replacing,
// again while Windows answers with a sharing violation, for the same bounded wait
// a replace gives a reader. A replace opens its destination with DELETE access for
// a moment, and an open that comes in then, without FILE_SHARE_DELETE (os.Open,
// like every Go open), cannot share the file with it. Any other error, among them
// a permission refusal, is the caller's at once.
func RetryBusy(attempt func() error) error {
	return retryTransient(attempt, func(err error) bool { return errors.Is(err, windows.ERROR_SHARING_VIOLATION) }, retrySleep)
}
