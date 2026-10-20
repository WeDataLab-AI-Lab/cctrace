package aireport

import (
	"context"
	"slices"
	"sync"
)

// runQueue admits report runs in the order they joined, at most limit at a
// time. A weekly firing starts every user's run in the same minute; past the
// limit they wait here, in the order the scheduler started them, rather than
// all asking the provider at once.
//
// The limit moves while runs are waiting: raising it lets the line in at once,
// and lowering it stops none of the runs already going.
type runQueue struct {
	mu      sync.Mutex
	limit   int
	running int
	line    []*runTurn
}

// runTurn is one run's place. admitted closes when the run may go.
type runTurn struct {
	q        *runQueue
	admitted chan struct{}
}

func newRunQueue(limit int) *runQueue {
	return &runQueue{limit: limit}
}

// join takes a place at the back of the line, admitted at once when a slot is
// free. It does not block, so the caller fixes the order before going async.
func (q *runQueue) join() *runTurn {
	q.mu.Lock()
	defer q.mu.Unlock()
	t := &runTurn{q: q, admitted: make(chan struct{})}
	q.line = append(q.line, t)
	q.admitLocked()
	return t
}

// wait blocks until the turn is admitted or ctx ends. On ctx's end the place
// is given up, and the caller must not release.
func (t *runTurn) wait(ctx context.Context) error {
	select {
	case <-t.admitted:
		return nil
	case <-ctx.Done():
	}
	q := t.q
	q.mu.Lock()
	defer q.mu.Unlock()
	select {
	case <-t.admitted:
		// Admitted as the context ended: the slot passes to the next in line.
		q.running--
		q.admitLocked()
	default:
		q.line = slices.DeleteFunc(q.line, func(o *runTurn) bool { return o == t })
	}
	return ctx.Err()
}

// release frees an admitted turn's slot for the next in line.
func (t *runTurn) release() {
	q := t.q
	q.mu.Lock()
	defer q.mu.Unlock()
	q.running--
	q.admitLocked()
}

func (q *runQueue) setLimit(n int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.limit = n
	q.admitLocked()
}

// waiting is how many runs are in line behind the limit.
func (q *runQueue) waiting() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.line)
}

func (q *runQueue) admitLocked() {
	for q.running < q.limit && len(q.line) > 0 {
		t := q.line[0]
		q.line = q.line[1:]
		q.running++
		close(t.admitted)
	}
}
