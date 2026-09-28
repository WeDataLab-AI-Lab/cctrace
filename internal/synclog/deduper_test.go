package synclog

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// fakeClock lets the interval-based flush be exercised without sleeping.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestDeduper(buf *bytes.Buffer, flushAfter time.Duration) (*Deduper, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 8, 13, 14, 0, 0, 0, time.UTC)}
	return newDeduper(buf, flushAfter, clk.now), clk
}

func TestDeduperCollapsesRepeats(t *testing.T) {
	var buf bytes.Buffer
	d, _ := newTestDeduper(&buf, time.Minute)

	line := "2026/08/13 14:00:00 [syncer] network is unreachable\n"
	for i := 0; i < 5; i++ {
		if _, err := d.Write([]byte(line)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if got := strings.Count(buf.String(), "network is unreachable"); got != 1 {
		t.Fatalf("repeated message written %d times, want 1", got)
	}

	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !strings.Contains(buf.String(), "repeated 4 times") {
		t.Fatalf("close should report the suppressed count, got:\n%s", buf.String())
	}
}

// The case this package exists for. Two loops firing on the same tick interleave
// their output, so no two consecutive lines are ever equal — folding that only
// compares against the previous line never fires. Every oversized log examined
// had exactly this shape.
func TestDeduperFoldsInterleavedMessages(t *testing.T) {
	var buf bytes.Buffer
	d, _ := newTestDeduper(&buf, time.Minute)

	for i := 0; i < 50; i++ {
		d.Write([]byte("2026/08/13 14:00:00 [sync] 0 records synced\n"))
		d.Write([]byte("2026/08/13 14:00:00 [quota] fetch failed: rate limited\n"))
	}

	if got := strings.Count(buf.String(), "records synced"); got != 1 {
		t.Fatalf("interleaved message written %d times, want 1", got)
	}
	if got := strings.Count(buf.String(), "rate limited"); got != 1 {
		t.Fatalf("interleaved message written %d times, want 1", got)
	}
}

// Only the message body is compared: the log package stamps every line with a
// fresh timestamp, so comparing whole lines would never find a duplicate.
func TestDeduperIgnoresTimestamp(t *testing.T) {
	var buf bytes.Buffer
	d, _ := newTestDeduper(&buf, time.Minute)

	d.Write([]byte("2026/08/13 14:00:01 [syncer] same body\n"))
	d.Write([]byte("2026/08/13 14:00:02 [syncer] same body\n"))
	d.Write([]byte("2026/08/13 14:00:03 [syncer] same body\n"))

	if got := strings.Count(buf.String(), "same body"); got != 1 {
		t.Fatalf("timestamps defeated dedup: message written %d times, want 1", got)
	}
}

// A message not seen before is never delayed — the first occurrence of anything
// is written immediately, so a new failure shows up at once.
func TestDeduperWritesNewMessagesImmediately(t *testing.T) {
	var buf bytes.Buffer
	d, _ := newTestDeduper(&buf, time.Minute)

	d.Write([]byte("2026/08/13 14:00:00 first\n"))
	d.Write([]byte("2026/08/13 14:00:01 second\n"))
	d.Write([]byte("2026/08/13 14:00:02 third\n"))

	for _, want := range []string{"first", "second", "third"} {
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("%q was withheld, got:\n%s", want, buf.String())
		}
	}
}

// A message that repeats forever must still surface periodically, otherwise a
// hot failure loop goes completely silent and the signal is lost.
func TestDeduperReemitsAfterInterval(t *testing.T) {
	var buf bytes.Buffer
	d, clk := newTestDeduper(&buf, time.Minute)

	line := "2026/08/13 14:00:00 stuck\n"
	d.Write([]byte(line))
	for i := 0; i < 10; i++ {
		d.Write([]byte(line))
	}
	if strings.Count(buf.String(), "stuck") != 1 {
		t.Fatalf("re-emitted before the interval elapsed, got:\n%s", buf.String())
	}

	clk.advance(time.Minute)
	d.Write([]byte(line))

	if strings.Count(buf.String(), "stuck") != 2 {
		t.Fatalf("interval elapsed but the message did not reappear, got:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "repeated 10 times in between") {
		t.Fatalf("suppressed count not reported, got:\n%s", buf.String())
	}
}

// The count restarts after each report, so two summaries never claim the same
// occurrences.
func TestDeduperResetsCountAfterReport(t *testing.T) {
	var buf bytes.Buffer
	d, clk := newTestDeduper(&buf, time.Minute)

	line := "2026/08/13 14:00:00 stuck\n"
	d.Write([]byte(line)) // emitted
	d.Write([]byte(line)) // suppressed 1
	d.Write([]byte(line)) // suppressed 2
	clk.advance(time.Minute)
	d.Write([]byte(line)) // re-emitted, reports 2
	d.Write([]byte(line)) // suppressed 1
	clk.advance(time.Minute)
	d.Write([]byte(line)) // re-emitted, reports 1

	if strings.Count(buf.String(), "repeated 2 times") != 1 {
		t.Fatalf("expected exactly one report of 2, got:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "repeated 1 times") {
		t.Fatalf("second batch should report its own count, got:\n%s", buf.String())
	}
}

// Tracking is bounded: output carrying unique detail must not grow the map
// without limit.
func TestDeduperEvictsOldestBeyondCap(t *testing.T) {
	var buf bytes.Buffer
	d, clk := newTestDeduper(&buf, time.Minute)
	d.maxTracked = 4

	for i := 0; i < 20; i++ {
		clk.advance(time.Second)
		d.Write([]byte("2026/08/13 14:00:00 unique message " + string(rune('a'+i)) + "\n"))
	}

	if len(d.seen) > 4 {
		t.Fatalf("tracked %d messages, cap is 4", len(d.seen))
	}
}

// Writes that are not a single complete line are passed straight through:
// splitting or buffering them would corrupt output the caller framed itself.
func TestDeduperPassesThroughMultiLineWrites(t *testing.T) {
	var buf bytes.Buffer
	d, _ := newTestDeduper(&buf, time.Minute)

	payload := "line one\nline two\n"
	d.Write([]byte(payload))
	d.Write([]byte(payload))

	if got := strings.Count(buf.String(), "line one"); got != 2 {
		t.Fatalf("multi-line payload was deduped: %d occurrences, want 2", got)
	}
}

// The io.Writer contract matters here: log.Output treats a short write as an
// error, so suppressed lines must still report the full length.
func TestDeduperReportsFullLength(t *testing.T) {
	var buf bytes.Buffer
	d, _ := newTestDeduper(&buf, time.Minute)

	line := []byte("2026/08/13 14:00:00 body\n")
	d.Write(line)
	n, err := d.Write(line) // suppressed
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != len(line) {
		t.Fatalf("suppressed write reported %d bytes, want %d", n, len(line))
	}
}
