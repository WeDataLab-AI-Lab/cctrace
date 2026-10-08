package sessionlog

import (
	"encoding/json"
	"testing"
)

// Each record says where its line starts in the file, whatever precedes it:
// earlier records, a blank line, a line that is not JSON, or a scan that began
// part-way in. A caller can then stop consuming at any record -- its Offset is
// the position to resume from -- instead of at the end of the scan only.
func TestScanFile_RecordOffsetIsLineStart(t *testing.T) {
	line1 := sampleLine("session1", true)
	junk := "not json\n\n"
	line2 := sampleLine("session2", true)
	line3 := sampleLine("session3", true)
	path := writeFile(t, line1+junk+line2+line3)

	records, newOffset, err := ScanFile(path, 0)
	if err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	want := []int64{0, int64(len(line1 + junk)), int64(len(line1 + junk + line2))}
	if len(records) != len(want) {
		t.Fatalf("got %d records, want %d", len(records), len(want))
	}
	for i, r := range records {
		if r.Offset != want[i] {
			t.Errorf("records[%d].Offset = %d, want %d", i, r.Offset, want[i])
		}
	}
	if end := int64(len(line1 + junk + line2 + line3)); newOffset != end {
		t.Fatalf("newOffset = %d, want %d", newOffset, end)
	}

	resumed, _, err := ScanFile(path, want[2])
	if err != nil {
		t.Fatalf("ScanFile from %d: %v", want[2], err)
	}
	if len(resumed) != 1 || resumed[0].SessionID != "session3" || resumed[0].Offset != want[2] {
		t.Fatalf("resumed scan = %+v, want session3 at %d", resumed, want[2])
	}
}

// The position is about where the line was read, not what it said: it does not
// travel with the record when the record is marshalled for the server.
func TestRecordOffsetIsNotMarshalled(t *testing.T) {
	a, err := json.Marshal(&Record{Type: "user"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	b, err := json.Marshal(&Record{Type: "user", Offset: 42})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(a) != string(b) {
		t.Fatalf("Offset changed the marshalled record:\n%s\n%s", a, b)
	}
}
