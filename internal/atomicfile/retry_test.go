package atomicfile

import (
	"errors"
	"testing"
	"time"
)

var errBusy = errors.New("busy")

func isBusy(err error) bool { return errors.Is(err, errBusy) }

func TestRetryTransientSucceedsOnceTheFileIsFree(t *testing.T) {
	busy := 3
	var sleeps []time.Duration
	err := retryTransient(func() error {
		if busy > 0 {
			busy--
			return errBusy
		}
		return nil
	}, isBusy, func(d time.Duration) { sleeps = append(sleeps, d) })
	if err != nil {
		t.Fatalf("retry ended with %v after the file became free", err)
	}
	if len(sleeps) != 3 {
		t.Fatalf("slept %v between four attempts", sleeps)
	}
	for i := 1; i < len(sleeps); i++ {
		if sleeps[i] <= sleeps[i-1] {
			t.Fatalf("waits do not back off: %v", sleeps)
		}
	}
}

func TestRetryTransientGivesUpAfterABoundedWait(t *testing.T) {
	attempts := 0
	var waited time.Duration
	err := retryTransient(func() error { attempts++; return errBusy }, isBusy, func(d time.Duration) { waited += d })
	if !errors.Is(err, errBusy) {
		t.Fatalf("retry ended with %v, want the last busy error", err)
	}
	if waited < retryBudget || waited > 2*retryBudget {
		t.Fatalf("waited %v in total, want about the %v budget", waited, retryBudget)
	}
	if attempts < 3 || attempts > 100 {
		t.Fatalf("%d attempts inside the budget", attempts)
	}
}

func TestRetryTransientReturnsOtherErrorsAtOnce(t *testing.T) {
	other := errors.New("destination is a directory")
	attempts := 0
	err := retryTransient(func() error { attempts++; return other }, isBusy, func(time.Duration) { t.Fatal("slept for an error that will not clear") })
	if err != other || attempts != 1 {
		t.Fatalf("err = %v after %d attempts, want %v after 1", err, attempts, other)
	}
}
