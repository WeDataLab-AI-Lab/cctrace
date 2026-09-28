package sessionlog

import (
	"os"
	"path/filepath"
	"testing"
)

// Identity for records that carry no uuid is derived from the bytes they were read
// from, so the scanner has to keep them. Without this the derivation silently falls
// back to a re-marshalled struct that has already dropped the payload.
func TestScannerKeepsTheLineItParsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	line := `{"type":"last-prompt","content":"keep me"}`
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	records, _, err := ScanFile(path, 0)
	if err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	if string(records[0].RawLine) != line {
		t.Errorf("RawLine = %q, want %q", records[0].RawLine, line)
	}
}
