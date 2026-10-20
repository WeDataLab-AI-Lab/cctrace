package sessionrecord

import (
	"context"
	"sync"
)

// TransmissionGate linearizes the start of an external request body with
// policy mutations. The read side is held only until request bytes are sent;
// a write waits for that boundary, cancels still-running responses, and then
// commits its mutation.
type TransmissionGate struct {
	mu       sync.RWMutex
	activeMu sync.Mutex
	nextID   uint64
	active   map[uint64]context.CancelFunc
}

type TransmissionLease struct {
	releaseOnce sync.Once
	finishOnce  sync.Once
	releaseDB   func()
	releaseGate func()
	remove      func()
}

func (g *TransmissionGate) Acquire(ctx context.Context, cancel context.CancelFunc, acquireDB func(context.Context) (func(), error)) (*TransmissionLease, error) {
	g.mu.RLock()
	g.activeMu.Lock()
	g.nextID++
	id := g.nextID
	if g.active == nil {
		g.active = make(map[uint64]context.CancelFunc)
	}
	g.active[id] = cancel
	g.activeMu.Unlock()

	releaseDB, err := acquireDB(ctx)
	if err != nil {
		g.activeMu.Lock()
		delete(g.active, id)
		g.activeMu.Unlock()
		g.mu.RUnlock()
		return nil, err
	}
	return &TransmissionLease{
		releaseDB:   releaseDB,
		releaseGate: g.mu.RUnlock,
		remove: func() {
			g.activeMu.Lock()
			delete(g.active, id)
			g.activeMu.Unlock()
		},
	}, nil
}

// ReleaseBody is called by GatedRoundTripper as soon as the outgoing body has
// been consumed. Finish also releases defensively and unregisters the response
// context once the runtime call has ended.
func (l *TransmissionLease) ReleaseBody() {
	l.releaseOnce.Do(func() {
		if l.releaseDB != nil {
			l.releaseDB()
		}
		if l.releaseGate != nil {
			l.releaseGate()
		}
	})
}

func (l *TransmissionLease) Finish() {
	l.finishOnce.Do(func() {
		l.ReleaseBody()
		if l.remove != nil {
			l.remove()
		}
	})
}

// Mutate waits until no request body is crossing the external boundary, cancels
// any response still in flight, and then runs the policy/source mutation.
func (g *TransmissionGate) Mutate(fn func() error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.activeMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(g.active))
	for _, cancel := range g.active {
		cancels = append(cancels, cancel)
	}
	g.activeMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	return fn()
}
