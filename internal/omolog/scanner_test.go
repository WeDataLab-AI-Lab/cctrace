package omolog

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func sampleOmoLines() []string {
	return []string{
		headerLine("/Users/alice/repo", "my title"),
		`{"type":"model_change","id":"m1","parentId":null,"timestamp":"2026-08-12T16:31:09.145Z","provider":"claude-sdk-oauth","modelId":"claude-opus-4-8"}`,
		`{"id":"a1","parentId":"m1","timestamp":"2026-08-12T16:33:31.269Z","type":"message","message":{"role":"user","content":[{"type":"text","text":"do the thing"}]}}`,
		`{"id":"a2","parentId":"a1","timestamp":"2026-08-12T16:33:43.677Z","type":"message","message":{"role":"assistant","content":[{"type":"text","text":"working on it"}],"model":"claude-opus-4-8","provider":"claude-sdk-oauth","usage":{"input":2,"output":822,"cost":{"total":0.26}}}}`,
		`{"id":"a3","parentId":"a2","timestamp":"2026-08-12T16:33:43.927Z","type":"message","message":{"role":"toolResult","toolCallId":"toolu_1","toolName":"bash","content":[{"type":"text","text":"done"}],"isError":false}}`,
	}
}

func TestScanFile_ProducesExpectedRecords(t *testing.T) {
	path := writeOmoFile(t, sampleOmoLines())

	records, newOffset, err := ScanFile(path, 0, "sess-1")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(records) != 3 {
		t.Fatalf("records = %d, want 3 (model_change skipped)", len(records))
	}
	if records[0].RecordType != "user" || records[1].RecordType != "assistant" || records[2].RecordType != "tool_result" {
		t.Fatalf("record types = %q/%q/%q, want user/assistant/tool_result",
			records[0].RecordType, records[1].RecordType, records[2].RecordType)
	}
	for _, r := range records {
		if r.CWD != "/Users/alice/repo" {
			t.Errorf("CWD = %q, want /Users/alice/repo", r.CWD)
		}
		if r.Title != "my title" {
			t.Errorf("Title = %q, want my title", r.Title)
		}
		if r.SessionID != "sess-1" {
			t.Errorf("SessionID = %q, want sess-1", r.SessionID)
		}
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if newOffset != fi.Size() {
		t.Errorf("newOffset = %d, want %d (end of file)", newOffset, fi.Size())
	}
}

func TestScanFileWithMetadata_RescanAtEOFYieldsNoMoreRecords(t *testing.T) {
	lines := sampleOmoLines()
	path := writeOmoFile(t, lines)

	firstRecords, firstOffset, meta, err := ScanFileWithMetadata(path, 0, "sess-1", Metadata{})
	if err != nil {
		t.Fatalf("first scan err = %v, want nil", err)
	}
	if len(firstRecords) != 3 {
		t.Fatalf("first scan records = %d, want 3", len(firstRecords))
	}
	if meta.CWD != "/Users/alice/repo" || meta.Title != "my title" {
		t.Fatalf("meta = %+v, want cwd/title from header", meta)
	}

	// Rescanning from the end offset with carried-forward metadata should
	// yield no further records and an unchanged offset.
	moreRecords, secondOffset, _, err := ScanFileWithMetadata(path, firstOffset, "sess-1", meta)
	if err != nil {
		t.Fatalf("second scan err = %v, want nil", err)
	}
	if len(moreRecords) != 0 {
		t.Fatalf("second scan records = %d, want 0 at EOF", len(moreRecords))
	}
	if secondOffset != firstOffset {
		t.Fatalf("secondOffset = %d, want %d", secondOffset, firstOffset)
	}
}

func TestScanFileWithMetadata_IncrementalScanSplitMidFile(t *testing.T) {
	lines := sampleOmoLines()
	path := writeOmoFile(t, lines)

	// Offset that lands exactly after the header + model_change + user
	// message lines (first 3 lines).
	var prefix string
	for i := 0; i < 3; i++ {
		prefix += lines[i] + "\n"
	}
	splitOffset := int64(len(prefix))

	metaAtSplit, err := ScanMetadata(path, splitOffset)
	if err != nil {
		t.Fatal(err)
	}
	if metaAtSplit.CWD != "/Users/alice/repo" {
		t.Fatalf("metaAtSplit.CWD = %q, want /Users/alice/repo", metaAtSplit.CWD)
	}

	restRecords, finalOffset, _, err := ScanFileWithMetadata(path, splitOffset, "sess-1", metaAtSplit)
	if err != nil {
		t.Fatal(err)
	}
	if len(restRecords) != 2 {
		t.Fatalf("restRecords = %d, want 2 (assistant + tool_result)", len(restRecords))
	}
	if restRecords[0].RecordType != "assistant" || restRecords[1].RecordType != "tool_result" {
		t.Fatalf("restRecords types = %q/%q, want assistant/tool_result", restRecords[0].RecordType, restRecords[1].RecordType)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if finalOffset != fi.Size() {
		t.Fatalf("finalOffset = %d, want %d", finalOffset, fi.Size())
	}
}

func TestScanMetadata_TitleAbsent(t *testing.T) {
	lines := []string{
		headerLine("/Users/alice/notitle", ""),
		`{"id":"a1","parentId":null,"timestamp":"2026-08-12T16:33:31.269Z","type":"message","message":{"role":"user","content":"hi"}}`,
	}
	path := writeOmoFile(t, lines)

	meta, err := ScanMetadata(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if meta.CWD != "/Users/alice/notitle" {
		t.Errorf("CWD = %q, want /Users/alice/notitle", meta.CWD)
	}
	if meta.Title != "" {
		t.Errorf("Title = %q, want empty", meta.Title)
	}
}

func TestScanFile_PartialLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	full := `{"id":"a1","parentId":null,"timestamp":"2026-08-12T16:33:31.269Z","type":"message","message":{"role":"user","content":"hello"}}`
	firstHalf, secondHalf := full[:len(full)/2], full[len(full)/2:]

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(f, firstHalf) // partial, no trailing newline
	f.Close()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	records, offset, err := ScanFile(path, 0, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("records = %d, want 0 (partial line not returned as a record)", len(records))
	}
	if offset != 0 {
		t.Fatalf("offset = %d, want 0 (partial line not consumed)", offset)
	}
	if fi.Size() == 0 {
		t.Fatal("test setup: file must contain the partial line")
	}

	// Appending the missing remainder plus a newline completes the
	// previously-partial line; a resumed scan from the earlier offset must
	// now return it.
	f, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, secondHalf)
	f.Close()

	moreRecords, finalOffset, err := ScanFile(path, offset, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(moreRecords) != 1 {
		t.Fatalf("records after completing the line = %d, want 1", len(moreRecords))
	}
	if moreRecords[0].Content != "hello" {
		t.Fatalf("Content = %q, want hello", moreRecords[0].Content)
	}
	fi2, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if finalOffset != fi2.Size() {
		t.Fatalf("finalOffset = %d, want %d", finalOffset, fi2.Size())
	}
}
