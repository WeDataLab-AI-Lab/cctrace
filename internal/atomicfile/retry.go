package atomicfile

import "time"

const (
	// retryBudget is the most a replace waits, in total, for a file another process
	// is using. A reader holds it for microseconds; a wait this long means
	// something that will not let go, and the error is then the caller's. A reader
	// that keeps the destination open past the budget therefore still makes the
	// replace fail (and a failed publish stops the local daemon's supervisor): a
	// known limitation, recorded for follow-up.
	retryBudget  = 2 * time.Second
	maxRetryWait = 100 * time.Millisecond
)

// retryTransient runs attempt until it succeeds, fails with an error transient
// does not accept, or has waited retryBudget. The waits start at a millisecond
// and double. sleep is time.Sleep outside tests.
func retryTransient(attempt func() error, transient func(error) bool, sleep func(time.Duration)) error {
	wait := time.Millisecond
	var waited time.Duration
	for {
		err := attempt()
		if err == nil || !transient(err) || waited >= retryBudget {
			return err
		}
		sleep(wait)
		waited += wait
		wait = min(2*wait, maxRetryWait)
	}
}
