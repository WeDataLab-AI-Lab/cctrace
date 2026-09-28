package omolog

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"cctrace/internal/jsonlscan"
)

func TestScanFileWithMetadataContextStopsOnRealDeadlineDuringHugeLine(t *testing.T) {
	path := writeOmoFile(t, []string{strings.Repeat("x", jsonlscan.MaxLineBytes+1)})
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)

	records, newOffset, _, err := ScanFileWithMetadataContext(ctx, path, 0, "sess-1", Metadata{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context deadline", err)
	}
	if len(records) != 0 {
		t.Fatalf("records = %d, want none after deadline", len(records))
	}
	if newOffset != 0 {
		t.Fatalf("newOffset = %d, want unchanged", newOffset)
	}
}
