package buffer

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestFlusher_NormalDrain(t *testing.T) {
	r := NewRing(10)
	r.Push([]byte("a"))
	r.Push([]byte("b"))
	r.Push([]byte("c"))

	var flushed []string
	fn := func(data []byte) error {
		flushed = append(flushed, string(data))
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	f := NewFlusher(r, fn, "test")

	done := make(chan struct{})
	go func() {
		f.Run(ctx)
		close(done)
	}()

	// Wait for at least one tick to flush
	time.Sleep(2 * time.Second)
	cancel()
	<-done

	if len(flushed) != 3 {
		t.Fatalf("expected 3 flushed items, got %d", len(flushed))
	}
	for i, want := range []string{"a", "b", "c"} {
		if flushed[i] != want {
			t.Fatalf("flushed[%d] = %q, want %q", i, flushed[i], want)
		}
	}
}

func TestFlusher_FlushFailure(t *testing.T) {
	r := NewRing(10)
	r.Push([]byte("item"))

	var callCount atomic.Int32
	fn := func(data []byte) error {
		callCount.Add(1)
		return errors.New("send failed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	f := NewFlusher(r, fn, "test-fail")

	done := make(chan struct{})
	go func() {
		f.Run(ctx)
		close(done)
	}()

	// Let it attempt at least one flush cycle
	time.Sleep(2 * time.Second)
	cancel()
	<-done

	if callCount.Load() == 0 {
		t.Fatal("expected flush func to be called at least once")
	}
	// Item should still be in the ring (pushed back on failure)
	if r.Len() == 0 {
		t.Fatal("expected item to be pushed back into ring after flush failure")
	}
}

func TestFlusher_ShutdownDrain(t *testing.T) {
	r := NewRing(10)
	r.Push([]byte("x"))
	r.Push([]byte("y"))

	var flushed []string
	fn := func(data []byte) error {
		flushed = append(flushed, string(data))
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	f := NewFlusher(r, fn, "test-shutdown")

	done := make(chan struct{})
	go func() {
		f.Run(ctx)
		close(done)
	}()

	// Cancel immediately so items are drained via drainOnce on shutdown
	cancel()
	<-done

	if len(flushed) != 2 {
		t.Fatalf("expected 2 items drained on shutdown, got %d", len(flushed))
	}
	if r.Len() != 0 {
		t.Fatalf("expected ring to be empty after shutdown drain, got %d", r.Len())
	}
}

func TestFlusher_DrainOrSpillBatchesFailedRecords(t *testing.T) {
	ring := NewRing(10)
	for _, record := range []string{"a", "b", "c"} {
		ring.Push([]byte(record))
	}
	spiller := NewDiskSpiller(t.TempDir(), 100<<20)
	t.Cleanup(func() { _ = spiller.Close() })
	flusher := NewFlusher(ring, func([]byte) error { return errors.New("queue unavailable") }, "test").WithSpiller(spiller)

	flusher.DrainOrSpill()

	if ring.Len() != 0 {
		t.Fatalf("ring length = %d, want 0", ring.Len())
	}
	var recovered []string
	for record := range spiller.Recover() {
		recovered = append(recovered, string(record))
	}
	if got, want := len(recovered), 3; got != want {
		t.Fatalf("recovered %d records, want %d", got, want)
	}
}

func TestFlusher_EmptyBuffer(t *testing.T) {
	r := NewRing(10)
	called := false
	fn := func(data []byte) error {
		called = true
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	f := NewFlusher(r, fn, "test-empty")

	done := make(chan struct{})
	go func() {
		f.Run(ctx)
		close(done)
	}()

	time.Sleep(2 * time.Second)
	cancel()
	<-done

	if called {
		t.Fatal("flush func should not be called on empty buffer")
	}
}
