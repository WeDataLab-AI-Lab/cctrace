package aireport

import (
	"context"
	"errors"
	"testing"
)

// admitted reports whether a turn may run, without waiting for it.
func admitted(t *runTurn) bool {
	select {
	case <-t.admitted:
		return true
	default:
		return false
	}
}

// Runs past the limit go in the order they joined, one per freed slot.
func TestRunQueueAdmitsInArrivalOrder(t *testing.T) {
	q := newRunQueue(1)
	first, second, third := q.join(), q.join(), q.join()
	if !admitted(first) || admitted(second) || admitted(third) || q.waiting() != 2 {
		t.Fatalf("after joining: %v %v %v, waiting %d", admitted(first), admitted(second), admitted(third), q.waiting())
	}
	first.release()
	if !admitted(second) || admitted(third) {
		t.Fatalf("after first: second %v third %v", admitted(second), admitted(third))
	}
	second.release()
	if !admitted(third) || q.waiting() != 0 {
		t.Fatalf("after second: third %v, waiting %d", admitted(third), q.waiting())
	}
}

// Raising the limit lets the line in at once; lowering it holds new runs until
// the ones already going drop below it, and stops none of them.
func TestRunQueueFollowsTheLimit(t *testing.T) {
	q := newRunQueue(1)
	a, b, c := q.join(), q.join(), q.join()
	q.setLimit(3)
	if !admitted(b) || !admitted(c) {
		t.Fatalf("raised: b %v c %v", admitted(b), admitted(c))
	}

	q.setLimit(1)
	d := q.join()
	a.release()
	b.release()
	if admitted(d) {
		t.Fatal("d ran while two were still going over the lowered limit")
	}
	c.release()
	if !admitted(d) {
		t.Fatal("d did not run once the limit had room")
	}
}

// A run canceled while waiting leaves the line and takes no slot with it.
func TestRunQueueCanceledWaiterLeavesTheLine(t *testing.T) {
	q := newRunQueue(1)
	first, quitter, next := q.join(), q.join(), q.join()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := quitter.wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled wait = %v", err)
	}
	if q.waiting() != 1 {
		t.Fatalf("waiting = %d, want only next", q.waiting())
	}
	first.release()
	if !admitted(next) {
		t.Fatal("next was not admitted after the quitter left")
	}
	if err := next.wait(context.Background()); err != nil {
		t.Fatalf("admitted wait = %v", err)
	}
}

// A turn admitted at the moment its context ended hands the slot on, or the
// slot would be lost until a restart.
func TestRunQueueAdmittedThenCanceledHandsTheSlotOn(t *testing.T) {
	q := newRunQueue(1)
	first, late, next := q.join(), q.join(), q.join()
	first.release() // late is admitted but has not looked yet

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Either outcome of the race is allowed; the slot must not leak.
	if err := late.wait(ctx); err == nil {
		late.release()
	}
	if !admitted(next) {
		t.Fatal("the slot late gave up did not reach next")
	}
}
