package codexlog

import (
	"context"
	"os"
	"strings"
	"testing"

	"cctrace/internal/jsonlscan"
)

func oversizedCodexLines() []string {
	return []string{
		`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"before"}]}}`,
		strings.Repeat("x", jsonlscan.MaxLineBytes+1),
		`{"type":"response_item","timestamp":"2026-04-23T11:30:13.000Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"after"}]}}`,
	}
}

func TestScanFileWithMetadataContextSkipsOversizedLineAndKeepsSurroundingRecords(t *testing.T) {
	path := writeCodexFile(t, oversizedCodexLines())

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
	path := writeCodexFile(t, oversizedCodexLines())

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

func TestScanMetadataContextSkipsOversizedLine(t *testing.T) {
	lines := []string{
		strings.Repeat("x", jsonlscan.MaxLineBytes+1),
		`{"type":"turn_context","timestamp":"2026-04-23T11:30:11.000Z","payload":{"cwd":"/myproject","model":"gpt-5"}}`,
	}
	path := writeCodexFile(t, lines)

	meta, err := ScanMetadataContext(context.Background(), path, 0)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if meta.CWD != "/myproject" || meta.Model != "gpt-5" {
		t.Fatalf("meta = %+v, want cwd/model from the line after the skipped one", meta)
	}
}
