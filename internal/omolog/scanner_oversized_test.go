package omolog

import (
	"context"
	"os"
	"strings"
	"testing"

	"cctrace/internal/jsonlscan"
)

func oversizedOmoLines() []string {
	return []string{
		`{"id":"a1","parentId":null,"timestamp":"2026-08-12T16:33:31.269Z","type":"message","message":{"role":"user","content":[{"type":"text","text":"before"}]}}`,
		strings.Repeat("x", jsonlscan.MaxLineBytes+1),
		`{"id":"a2","parentId":"a1","timestamp":"2026-08-12T16:33:43.677Z","type":"message","message":{"role":"assistant","content":[{"type":"text","text":"after"}]}}`,
	}
}

func TestScanFileWithMetadataContextSkipsOversizedLineAndKeepsSurroundingRecords(t *testing.T) {
	path := writeOmoFile(t, oversizedOmoLines())

	records, newOffset, _, err := ScanFileWithMetadataContext(context.Background(), path, 0, "sess-1", Metadata{})
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
	path := writeOmoFile(t, oversizedOmoLines())

	_, firstOffset, meta, err := ScanFileWithMetadataContext(context.Background(), path, 0, "sess-1", Metadata{})
	if err != nil {
		t.Fatalf("first scan err = %v, want nil", err)
	}

	records, secondOffset, _, err := ScanFileWithMetadataContext(context.Background(), path, firstOffset, "sess-1", meta)
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

func TestScanMetadataContextSkipsOversizedLine(t *testing.T) {
	lines := []string{
		strings.Repeat("x", jsonlscan.MaxLineBytes+1),
		headerLine("/myproject", ""),
	}
	path := writeOmoFile(t, lines)

	meta, err := ScanMetadataContext(context.Background(), path, 0)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if meta.CWD != "/myproject" {
		t.Fatalf("meta = %+v, want cwd from the line after the skipped one", meta)
	}
}
