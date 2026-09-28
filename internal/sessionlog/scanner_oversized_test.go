package sessionlog

import (
	"context"
	"os"
	"strings"
	"testing"

	"cctrace/internal/jsonlscan"
)

func TestScanFileContextSkipsOversizedLineAndKeepsSurroundingRecords(t *testing.T) {
	content := `{"type":"user","uuid":"u1"}` + "\n" +
		strings.Repeat("x", jsonlscan.MaxLineBytes+1) + "\n" +
		`{"type":"assistant","uuid":"a1"}` + "\n"
	path := writeFile(t, content)

	records, newOffset, err := ScanFileContext(context.Background(), path, 0)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2 (records before and after the skipped line)", len(records))
	}
	if records[0].Type != "user" || records[1].Type != "assistant" {
		t.Fatalf("record types = %q/%q, want user/assistant", records[0].Type, records[1].Type)
	}
	fi, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if newOffset != fi.Size() {
		t.Fatalf("newOffset = %d, want %d (end of file)", newOffset, fi.Size())
	}

	records, secondOffset, err := ScanFileContext(context.Background(), path, newOffset)
	if err != nil {
		t.Fatalf("rescan err = %v, want nil", err)
	}
	if len(records) != 0 || secondOffset != newOffset {
		t.Fatalf("rescan records = %d offset = %d, want 0 records and offset %d", len(records), secondOffset, newOffset)
	}
}
