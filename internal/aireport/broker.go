package aireport

import (
	"sync"

	"cctrace/internal/airuntime"
)

// subscriberBuffer is how far a subscriber may fall behind before it is dropped.
// A dropped subscriber sees its channel close and falls back to the database.
const subscriberBuffer = 256

// broker fans one run's progress events out to stream subscribers. It keeps the
// run's history so a late subscriber replays what it missed and then follows
// live under the same lock, with no gap and no repeat. The database stays the
// source of truth; a closed channel means "re-read it".
type broker struct {
	mu   sync.Mutex
	runs map[int64]*runStream
}

type runStream struct {
	history []airuntime.Event
	subs    map[*subscriber]struct{}
}

type subscriber struct {
	ch     chan airuntime.Event
	closed bool
}

func newBroker() *broker { return &broker{runs: map[int64]*runStream{}} }

func (b *broker) open(runID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.runs[runID] = &runStream{subs: map[*subscriber]struct{}{}}
}

func (b *broker) publish(runID int64, ev airuntime.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rs := b.runs[runID]
	if rs == nil {
		return
	}
	rs.history = append(rs.history, ev)
	for sub := range rs.subs {
		select {
		case sub.ch <- ev:
		default:
			sub.closed = true
			close(sub.ch)
			delete(rs.subs, sub)
		}
	}
}

// close ends the run's stream: every subscriber's channel closes and later
// subscribers get an already-closed channel.
func (b *broker) close(runID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rs := b.runs[runID]
	if rs == nil {
		return
	}
	for sub := range rs.subs {
		sub.closed = true
		close(sub.ch)
	}
	delete(b.runs, runID)
}

// subscribe replays tool events with Seq > afterSeq and the latest usage event,
// then delivers live events until the run closes or the returned func is called.
func (b *broker) subscribe(runID int64, afterSeq int) (<-chan airuntime.Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rs := b.runs[runID]
	if rs == nil {
		ch := make(chan airuntime.Event)
		close(ch)
		return ch, func() {}
	}
	var replay []airuntime.Event
	var usage *airuntime.Event
	for i, ev := range rs.history {
		switch {
		case ev.Kind == airuntime.EventUsage:
			usage = &rs.history[i]
		case ev.Seq > afterSeq:
			replay = append(replay, ev)
		}
	}
	if usage != nil {
		replay = append(replay, *usage)
	}
	sub := &subscriber{ch: make(chan airuntime.Event, len(replay)+subscriberBuffer)}
	for _, ev := range replay {
		sub.ch <- ev
	}
	rs.subs[sub] = struct{}{}
	return sub.ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if sub.closed {
			return
		}
		sub.closed = true
		close(sub.ch)
		if rs := b.runs[runID]; rs != nil {
			delete(rs.subs, sub)
		}
	}
}
