package aireport

import (
	"testing"
	"time"

	"cctrace/internal/airuntime"
)

func recv(t *testing.T, ch <-chan airuntime.Event) (airuntime.Event, bool) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		return ev, ok
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
		return airuntime.Event{}, false
	}
}

func TestBrokerReplayThenLiveWithoutDuplicates(t *testing.T) {
	b := newBroker()
	b.open(1)
	b.publish(1, airuntime.Event{Kind: airuntime.EventToolCall, Seq: 1})
	b.publish(1, airuntime.Event{Kind: airuntime.EventToolResult, Seq: 1})
	b.publish(1, airuntime.Event{Kind: airuntime.EventToolCall, Seq: 2})

	ch, unsub := b.subscribe(1, 1)
	defer unsub()
	b.publish(1, airuntime.Event{Kind: airuntime.EventToolResult, Seq: 2})
	b.close(1)

	var got []airuntime.Event
	for {
		ev, ok := recv(t, ch)
		if !ok {
			break
		}
		got = append(got, ev)
	}
	if len(got) != 2 || got[0].Kind != airuntime.EventToolCall || got[1].Kind != airuntime.EventToolResult || got[0].Seq != 2 {
		t.Fatalf("got %+v", got)
	}
}

func TestBrokerUnknownOrClosedRunClosesImmediately(t *testing.T) {
	b := newBroker()
	ch, unsub := b.subscribe(42, 0)
	defer unsub()
	if _, ok := recv(t, ch); ok {
		t.Fatal("want closed channel")
	}
	b.open(1)
	b.close(1)
	ch2, unsub2 := b.subscribe(1, 0)
	defer unsub2()
	if _, ok := recv(t, ch2); ok {
		t.Fatal("want closed channel after close")
	}
}

func TestBrokerDropsSlowSubscriberInsteadOfBlocking(t *testing.T) {
	b := newBroker()
	b.open(1)
	ch, unsub := b.subscribe(1, 0)
	defer unsub()
	done := make(chan struct{})
	go func() {
		for i := 0; i < subscriberBuffer*2; i++ {
			b.publish(1, airuntime.Event{Kind: airuntime.EventUsage})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publish blocked on a slow subscriber")
	}
	n := 0
	for range ch {
		n++
	}
	if n > subscriberBuffer {
		t.Fatalf("received %d, buffer is %d", n, subscriberBuffer)
	}
}

func TestBrokerUnsubscribeIsIdempotent(t *testing.T) {
	b := newBroker()
	b.open(1)
	_, unsub := b.subscribe(1, 0)
	unsub()
	unsub()
	b.publish(1, airuntime.Event{Kind: airuntime.EventUsage})
	b.close(1)
}
