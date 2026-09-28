package synclog

import (
	"fmt"
	"io"
	"sort"
	"sync"
	"time"
)

const (
	// DefaultFlushInterval bounds how often any one message may reappear. A loop
	// firing once a second contributes 2 lines a minute instead of 60, and stays
	// visible.
	DefaultFlushInterval = time.Minute

	// DefaultMaxTracked caps how many distinct messages are tracked at once, so
	// output carrying unbounded detail (paths, ids) cannot grow the map without
	// limit. The least recently emitted entry is dropped past this point.
	DefaultMaxTracked = 256
)

// Deduper rate-limits each distinct message and records how many occurrences it
// suppressed.
//
// It exists to keep rotation from destroying evidence. Every oversized log
// examined so far was a handful of messages repeated by retry loops that never
// backed off — 1.29M copies of one quota failure in a 305MB sample. Rotation
// alone would discard the log's only abnormal signal, its size, and leave the
// loop invisible; the suppressed counts preserve that signal in a line or two.
//
// Messages are tracked individually rather than by comparing against the
// previous line. Two loops running at the same interval interleave their output,
// so no two consecutive lines are ever equal and consecutive-only folding never
// fires — which is exactly the shape of every log this was built for.
//
// Only the message body is compared. The log package stamps each line with a
// fresh timestamp, so comparing whole lines would never match.
type Deduper struct {
	w          io.Writer
	flushAfter time.Duration
	now        func() time.Time
	maxTracked int

	mu   sync.Mutex
	seen map[string]*dedupEntry
}

type dedupEntry struct {
	suppressed int
	lastEmit   time.Time
}

// NewDeduper wraps w, letting each distinct message through at most once per
// DefaultFlushInterval.
func NewDeduper(w io.Writer) *Deduper {
	return newDeduper(w, DefaultFlushInterval, time.Now)
}

func newDeduper(w io.Writer, flushAfter time.Duration, now func() time.Time) *Deduper {
	return &Deduper{
		w:          w,
		flushAfter: flushAfter,
		now:        now,
		maxTracked: DefaultMaxTracked,
		seen:       make(map[string]*dedupEntry),
	}
}

func (d *Deduper) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	body, ok := singleLineBody(p)
	if !ok {
		// Anything the caller framed itself — a multi-line payload or a partial
		// write — passes through untouched rather than risk reordering it.
		return d.w.Write(p)
	}

	key := string(body)
	e := d.seen[key]
	now := d.now()
	if e != nil && now.Sub(e.lastEmit) < d.flushAfter {
		e.suppressed++
		return len(p), nil
	}

	n, err := d.w.Write(p)
	if err != nil {
		return n, err
	}
	if e == nil {
		d.evictLocked()
		e = &dedupEntry{}
		d.seen[key] = e
	}
	if e.suppressed > 0 {
		if _, err := fmt.Fprintf(d.w, "%s ... (previous message repeated %d times in between)\n",
			now.Format(stampLayout), e.suppressed); err != nil {
			return n, err
		}
		e.suppressed = 0
	}
	e.lastEmit = now
	return n, nil
}

// Close reports every still-pending count, then closes the underlying writer if
// it supports it — so callers can hand off a single Closer.
//
// Without this, a daemon that spent its whole life in a failure loop would exit
// having logged the failure once, with no indication it happened a million times.
func (d *Deduper) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	pending := make([]string, 0, len(d.seen))
	for key, e := range d.seen {
		if e.suppressed > 0 {
			pending = append(pending, key)
		}
	}
	// Map order is random; sort so the tail of a log is reproducible.
	sort.Strings(pending)

	stamp := d.now().Format(stampLayout)
	for _, key := range pending {
		if _, err := fmt.Fprintf(d.w, "%s ... (repeated %d times, not shown): %s\n",
			stamp, d.seen[key].suppressed, key); err != nil {
			return err
		}
		d.seen[key].suppressed = 0
	}

	if c, ok := d.w.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// evictLocked drops the least recently emitted entry once the map is full.
// Losing a stale entry only costs one suppressed count, which is cheaper than
// tracking every unique string a failing subsystem can produce.
func (d *Deduper) evictLocked() {
	if len(d.seen) < d.maxTracked {
		return
	}
	var oldestKey string
	var oldest time.Time
	for key, e := range d.seen {
		if oldestKey == "" || e.lastEmit.Before(oldest) {
			oldestKey, oldest = key, e.lastEmit
		}
	}
	delete(d.seen, oldestKey)
}

const stampLayout = "2006/01/02 15:04:05"

// singleLineBody returns the message with its log timestamp and trailing
// newline removed, and reports whether p was exactly one complete line.
func singleLineBody(p []byte) ([]byte, bool) {
	if len(p) < 2 || p[len(p)-1] != '\n' {
		return nil, false
	}
	line := p[:len(p)-1]
	for _, c := range line {
		if c == '\n' {
			return nil, false
		}
	}
	return stripTimestamp(line), true
}

// stripTimestamp removes a leading "2006/01/02 15:04:05" stamp, with optional
// fractional seconds, as written by the standard log package.
func stripTimestamp(line []byte) []byte {
	const stamp = len(stampLayout)
	if len(line) < stamp+1 {
		return line
	}
	for i, c := range line[:stamp] {
		switch i {
		case 4, 7:
			if c != '/' {
				return line
			}
		case 10:
			if c != ' ' {
				return line
			}
		case 13, 16:
			if c != ':' {
				return line
			}
		default:
			if c < '0' || c > '9' {
				return line
			}
		}
	}
	rest := line[stamp:]
	if len(rest) > 0 && rest[0] == '.' {
		i := 1
		for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
			i++
		}
		rest = rest[i:]
	}
	if len(rest) > 0 && rest[0] == ' ' {
		return rest[1:]
	}
	return line
}

var _ io.WriteCloser = (*Deduper)(nil)
