package sessionlog

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"cctrace/internal/jsonlscan"
)

// This test narrows jsonlscan.MaxLineBytes, so it cannot run in parallel with
// the rest of the package.

// A line over the limit is drained without a record, and whatever it said --
// including which cwd the session had moved to -- is gone. The scan says where
// those lines were, so a caller that carries state from one record to the next
// knows the chain is broken there. ScanFileContext returns the same records and
// offset as before and simply does not report them.
func TestScanFileWithSkips_ReportsSkippedSpans(t *testing.T) {
	prev := jsonlscan.MaxLineBytes
	jsonlscan.MaxLineBytes = 256
	t.Cleanup(func() { jsonlscan.MaxLineBytes = prev })

	line1 := sampleLine("session1", true)
	big1 := strings.Repeat("x", 300) + "\n"
	line2 := sampleLine("session2", true)
	big2 := strings.Repeat("y", 400) + "\n"
	path := writeFile(t, line1+big1+line2+big2)

	records, newOffset, skipped, err := ScanFileWithSkips(context.Background(), path, 0)
	if err != nil {
		t.Fatalf("ScanFileWithSkips: %v", err)
	}
	if len(records) != 2 || records[0].SessionID != "session1" || records[1].SessionID != "session2" {
		t.Fatalf("records = %+v, want session1 and session2", records)
	}
	afterBig1 := int64(len(line1 + big1))
	want := []SkippedSpan{
		{Start: int64(len(line1)), End: afterBig1},
		{Start: afterBig1 + int64(len(line2)), End: afterBig1 + int64(len(line2+big2))},
	}
	if !reflect.DeepEqual(skipped, want) {
		t.Fatalf("skipped = %+v, want %+v", skipped, want)
	}
	if newOffset != want[1].End {
		t.Fatalf("newOffset = %d, want %d", newOffset, want[1].End)
	}

	plain, plainOffset, err := ScanFileContext(context.Background(), path, 0)
	if err != nil {
		t.Fatalf("ScanFileContext: %v", err)
	}
	if !reflect.DeepEqual(plain, records) || plainOffset != newOffset {
		t.Fatalf("ScanFileContext returned %d records to offset %d, want the same %d to %d", len(plain), plainOffset, len(records), newOffset)
	}

	// A scan that skips nothing reports nothing.
	_, _, none, err := ScanFileWithSkips(context.Background(), writeFile(t, line1+line2), 0)
	if err != nil || len(none) != 0 {
		t.Fatalf("skipped = %+v err = %v on a file without oversized lines", none, err)
	}
}
