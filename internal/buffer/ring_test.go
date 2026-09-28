package buffer

import (
	"sync"
	"testing"
)

func TestRing_PushPop_FIFO(t *testing.T) {
	r := NewRing(10)
	r.Push([]byte("a"))
	r.Push([]byte("b"))
	r.Push([]byte("c"))

	for _, want := range []string{"a", "b", "c"} {
		got, ok := r.Pop()
		if !ok {
			t.Fatalf("expected item %q, got nothing", want)
		}
		if string(got) != want {
			t.Fatalf("expected %q, got %q", want, string(got))
		}
	}
}

func TestRing_OverwriteOnFull(t *testing.T) {
	r := NewRing(3)
	r.Push([]byte("a"))
	r.Push([]byte("b"))
	r.Push([]byte("c"))

	overwritten := r.Push([]byte("d"))
	if !overwritten {
		t.Fatal("expected overwrite when buffer is full")
	}

	// "a" should be gone; first pop should return "b"
	got, ok := r.Pop()
	if !ok {
		t.Fatal("expected item, got nothing")
	}
	if string(got) != "b" {
		t.Fatalf("expected %q, got %q", "b", string(got))
	}
}

func TestRing_Len(t *testing.T) {
	r := NewRing(5)
	if r.Len() != 0 {
		t.Fatalf("expected len 0, got %d", r.Len())
	}

	r.Push([]byte("a"))
	r.Push([]byte("b"))
	if r.Len() != 2 {
		t.Fatalf("expected len 2, got %d", r.Len())
	}

	r.Pop()
	if r.Len() != 1 {
		t.Fatalf("expected len 1, got %d", r.Len())
	}
}

func TestRing_IsFull(t *testing.T) {
	r := NewRing(2)
	if r.IsFull() {
		t.Fatal("should not be full when empty")
	}

	r.Push([]byte("a"))
	if r.IsFull() {
		t.Fatal("should not be full with 1/2 items")
	}

	r.Push([]byte("b"))
	if !r.IsFull() {
		t.Fatal("should be full at capacity")
	}
}

func TestRing_EmptyPop(t *testing.T) {
	r := NewRing(5)
	data, ok := r.Pop()
	if ok {
		t.Fatal("expected ok=false for empty buffer")
	}
	if data != nil {
		t.Fatalf("expected nil data, got %v", data)
	}
}

func TestRing_ConcurrentAccess(t *testing.T) {
	r := NewRing(100)
	var wg sync.WaitGroup

	// 10 goroutines pushing
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				r.Push([]byte("x"))
			}
		}()
	}

	// 10 goroutines popping
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				r.Pop()
			}
		}()
	}

	wg.Wait()
	// No race detector failures = pass
}

func TestRing_DataIsolation(t *testing.T) {
	r := NewRing(5)
	original := []byte("hello")
	r.Push(original)

	// Mutate the original slice after push
	original[0] = 'X'

	got, ok := r.Pop()
	if !ok {
		t.Fatal("expected item")
	}
	if string(got) != "hello" {
		t.Fatalf("expected %q, got %q — data isolation violated", "hello", string(got))
	}
}
