package buffer

import (
	"log"
	"sync"
)

// Ring is a thread-safe ring buffer that stores []byte messages.
// When full, the oldest entry is overwritten.
type Ring struct {
	mu    sync.Mutex
	buf   [][]byte
	cap   int
	head  int // next write position
	count int // number of items currently stored
}

// NewRing creates a ring buffer with the given capacity.
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		capacity = 10000
	}
	return &Ring{
		buf: make([][]byte, capacity),
		cap: capacity,
	}
}

// Push adds data to the ring buffer. If the buffer is full, the oldest
// entry is overwritten and a warning is logged. Returns true if an
// existing entry was overwritten.
func (r *Ring) Push(data []byte) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	overwritten := r.count == r.cap
	if overwritten {
		log.Printf("[buffer] ring buffer full (cap=%d), overwriting oldest entry", r.cap)
	}

	// Copy data to avoid external mutation
	cp := make([]byte, len(data))
	copy(cp, data)

	r.buf[r.head] = cp
	r.head = (r.head + 1) % r.cap

	if !overwritten {
		r.count++
	}
	return overwritten
}

// Pop removes and returns the oldest item from the buffer.
// Returns nil, false if empty.
func (r *Ring) Pop() ([]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.count == 0 {
		return nil, false
	}

	// tail is the oldest entry
	tail := (r.head - r.count + r.cap) % r.cap
	data := r.buf[tail]
	r.buf[tail] = nil // allow GC
	r.count--
	return data, true
}

// Len returns the number of items in the buffer.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

// IsFull returns true if the buffer is at capacity.
func (r *Ring) IsFull() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count == r.cap
}
