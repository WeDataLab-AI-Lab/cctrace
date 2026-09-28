package gjclog

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"cctrace/internal/jsonlscan"
)

func oversizedGjcLines() []string {
	return []string{
		`{"id":"m1","parentId":null,"timestamp":"2026-08-12T16:17:23.000Z","type":"message","message":{"role":"user","content":"before"}}`,
		strings.Repeat("x", jsonlscan.MaxLineBytes+1),
		`{"id":"m2","parentId":"m1","timestamp":"2026-08-12T16:17:24.000Z","type":"message","message":{"role":"assistant","model":"claude-opus-5","content":"after"}}`,
	}
}

func TestScanFileWithMetadataContextSkipsOversizedLineAndKeepsSurroundingRecords(t *testing.T) {
	path := writeGjcFile(t, oversizedGjcLines())

	records, newOffset, _, err := ScanFileWithMetadataContext(context.Background(), path, 0, "session", Metadata{})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2 (records before and after the skipped line)", len(records))
	}
	if records[0].RecordType != "user" || records[1].RecordType != "assistant" {
		t.Fatalf("record types = %q/%q, want user/assistant", records[0].RecordType, records[1].RecordType)
	}
	fi, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if newOffset != fi.Size() {
		t.Fatalf("newOffset = %d, want %d (end of file)", newOffset, fi.Size())
	}
}

func TestScanFileWithMetadataContextOversizedLineDoesNotRetryOnRescan(t *testing.T) {
	path := writeGjcFile(t, oversizedGjcLines())

	_, firstOffset, meta, err := ScanFileWithMetadataContext(context.Background(), path, 0, "session", Metadata{})
	if err != nil {
		t.Fatalf("first scan err = %v, want nil", err)
	}

	records, secondOffset, _, err := ScanFileWithMetadataContext(context.Background(), path, firstOffset, "session", meta)
	if err != nil {
		t.Fatalf("second scan err = %v, want nil", err)
	}
	if len(records) != 0 {
		t.Fatalf("records = %d, want 0 on rescan", len(records))
	}
	if secondOffset != firstOffset {
		t.Fatalf("secondOffset = %d, want %d (offset must stay at end of file)", secondOffset, firstOffset)
	}
}

func TestScanFileWithMetadataContextStopsOnRealDeadlineDuringHugeLine(t *testing.T) {
	path := writeGjcFile(t, []string{strings.Repeat("x", jsonlscan.MaxLineBytes+1)})
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)

	records, newOffset, _, err := ScanFileWithMetadataContext(ctx, path, 0, "session", Metadata{})
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

func TestScanTokenLogContextStopsOnRealDeadlineDuringHugeLine(t *testing.T) {
	path := writeGjcFile(t, []string{strings.Repeat("x", jsonlscan.MaxLineBytes+1)})
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)

	records, newOffset, err := ScanTokenLogContext(ctx, path, 0)
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

func TestScanMetadataContextSkipsOversizedLine(t *testing.T) {
	lines := []string{
		strings.Repeat("x", jsonlscan.MaxLineBytes+1),
		`{"type":"session","version":5,"id":"s1","timestamp":"2026-08-12T16:17:22.275Z","cwd":"/myproject","title":"a title"}`,
	}
	path := writeGjcFile(t, lines)

	meta, err := ScanMetadataContext(context.Background(), path, 0)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if meta.CWD != "/myproject" || meta.Title != "a title" {
		t.Fatalf("meta = %+v, want cwd/title from the line after the skipped one", meta)
	}
}
