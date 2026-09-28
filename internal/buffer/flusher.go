package buffer

import (
	"context"
	"log"
	"time"
)

const (
	flushInterval  = 1 * time.Second
	maxBackoff     = 30 * time.Second
	initialBackoff = 500 * time.Millisecond
)

// FlushFunc is called for each item popped from the ring buffer.
type FlushFunc func(data []byte) error

// Flusher drains a Ring buffer by calling a flush function for each item.
type Flusher struct {
	ring    *Ring
	flush   FlushFunc
	name    string // label for logging
	done    chan struct{}
	spiller *DiskSpiller
}

// NewFlusher creates a flusher for the given ring buffer.
// name is used as a label in log messages (e.g. "logs", "metrics").
func NewFlusher(ring *Ring, flush FlushFunc, name string) *Flusher {
	return &Flusher{
		ring:  ring,
		flush: flush,
		name:  name,
		done:  make(chan struct{}),
	}
}

// Done returns a channel that is closed when the flusher's Run loop exits.
func (f *Flusher) Done() <-chan struct{} {
	return f.done
}

// WithSpiller attaches a DiskSpiller for WAL fallback when flush fails.
func (f *Flusher) WithSpiller(s *DiskSpiller) *Flusher {
	f.spiller = s
	return f
}

// Run starts the flush loop. It blocks until ctx is cancelled.
func (f *Flusher) Run(ctx context.Context) {
	defer close(f.done)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	backoff := initialBackoff

	for {
		select {
		case <-ctx.Done():
			// Drain remaining items on shutdown (best-effort)
			f.drainOnce()
			return
		case <-ticker.C:
			flushed := 0
			for {
				data, ok := f.ring.Pop()
				if !ok {
					break
				}
				if err := f.flush(data); err != nil {
					// Spill to WAL to prevent data loss during extended outages
					if f.spiller != nil {
						if spillErr := f.spiller.Spill(data); spillErr != nil {
							log.Printf("[buffer] %s spill failed: %v (flush err: %v)", f.name, spillErr, err)
							f.ring.Push(data) // last resort: push back
						} else {
							log.Printf("[buffer] %s flush failed, spilled to WAL: %v", f.name, err)
						}
					} else {
						f.ring.Push(data)
					}
					log.Printf("[buffer] %s backing off %v", f.name, backoff)
					select {
					case <-ctx.Done():
						return
					case <-time.After(backoff):
					}
					backoff *= 2
					if backoff > maxBackoff {
						backoff = maxBackoff
					}
					break
				}
				// Reset backoff on success
				backoff = initialBackoff
				flushed++
			}
			if flushed > 0 {
				log.Printf("[buffer] %s flushed %d items", f.name, flushed)
			}
		}
	}
}

func (f *Flusher) drainOnce() {
	flushed := 0
	spilled := 0
	for {
		data, ok := f.ring.Pop()
		if !ok {
			break
		}
		if err := f.flush(data); err != nil {
			if f.spiller != nil {
				if spillErr := f.spiller.Spill(data); spillErr != nil {
					log.Printf("[buffer] %s spill failed: %v (flush err: %v)", f.name, spillErr, err)
					f.ring.Push(data)
					return
				}
				spilled++
			} else {
				f.ring.Push(data)
				log.Printf("[buffer] %s shutdown drain failed (%d remaining): %v", f.name, f.ring.Len(), err)
				return
			}
		} else {
			flushed++
		}
	}
	if flushed > 0 || spilled > 0 {
		log.Printf("[buffer] %s drained %d items, spilled %d to WAL on shutdown", f.name, flushed, spilled)
	}
}

// DrainOrSpill attempts to flush all ring buffer items. Items that fail to flush
// are spilled to disk via the DiskSpiller (if configured).
func (f *Flusher) DrainOrSpill() {
	flushed := 0
	var pendingSpill [][]byte
	for {
		data, ok := f.ring.Pop()
		if !ok {
			break
		}
		if err := f.flush(data); err != nil {
			if f.spiller != nil {
				pendingSpill = append(pendingSpill, data)
			} else {
				f.ring.Push(data)
				log.Printf("[buffer] %s drain failed, no spiller (%d remaining): %v", f.name, f.ring.Len(), err)
				return
			}
		} else {
			flushed++
		}
	}
	spilled := 0
	if len(pendingSpill) > 0 {
		if err := f.spiller.SpillBatch(pendingSpill); err != nil {
			log.Printf("[buffer] %s shutdown batch spill failed: %v", f.name, err)
			for _, data := range pendingSpill {
				f.ring.Push(data) // last resort: retain in memory
			}
			return
		}
		spilled = len(pendingSpill)
	}
	if flushed > 0 || spilled > 0 {
		log.Printf("[buffer] %s drain: flushed %d, spilled %d to WAL", f.name, flushed, spilled)
	}
}
