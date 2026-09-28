package buffer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDiskSpiller_SpillAndRecover(t *testing.T) {
	dir := t.TempDir()
	ds := NewDiskSpiller(dir, 100<<20) // 100MB, won't rotate
	t.Cleanup(func() { _ = ds.Close() })

	// Spill some records
	records := []string{`{"type":"log","msg":"hello"}`, `{"type":"log","msg":"world"}`, `{"type":"metric","val":42}`}
	for _, r := range records {
		if err := ds.Spill([]byte(r)); err != nil {
			t.Fatalf("Spill: %v", err)
		}
	}

	// Recover and verify
	var recovered []string
	for data := range ds.Recover() {
		recovered = append(recovered, string(data))
	}

	if len(recovered) != len(records) {
		t.Fatalf("expected %d records, got %d", len(records), len(recovered))
	}
	for i, want := range records {
		if recovered[i] != want {
			t.Errorf("record[%d] = %q, want %q", i, recovered[i], want)
		}
	}
}

func TestDiskSpiller_SpillBatchAndRecover(t *testing.T) {
	ds := NewDiskSpiller(t.TempDir(), 100<<20)
	t.Cleanup(func() { _ = ds.Close() })
	records := [][]byte{[]byte("first"), []byte("second"), []byte("third")}
	if err := ds.SpillBatch(records); err != nil {
		t.Fatalf("SpillBatch: %v", err)
	}

	var recovered []string
	for record := range ds.Recover() {
		recovered = append(recovered, string(record))
	}
	if got, want := len(recovered), len(records); got != want {
		t.Fatalf("recovered %d records, want %d", got, want)
	}
	for i, record := range records {
		if recovered[i] != string(record) {
			t.Errorf("record[%d] = %q, want %q", i, recovered[i], record)
		}
	}
}

func TestDiskSpiller_Rotation(t *testing.T) {
	dir := t.TempDir()
	ds := NewDiskSpiller(dir, 50) // tiny max size to force rotation
	t.Cleanup(func() { _ = ds.Close() })

	// Write enough data to trigger rotation
	for i := 0; i < 10; i++ {
		if err := ds.Spill([]byte(`{"idx":` + string(rune('0'+i)) + `}`)); err != nil {
			t.Fatalf("Spill: %v", err)
		}
	}
	ds.Close()

	if fc, err := ds.FileCount(); err != nil || fc < 2 {
		t.Fatalf("expected multiple WAL files due to rotation, got %d", fc)
	}

	// All records should still be recoverable
	count := 0
	for range ds.Recover() {
		count++
	}
	if count != 10 {
		t.Fatalf("expected 10 recovered records, got %d", count)
	}
}

func TestDiskSpiller_Cleanup(t *testing.T) {
	dir := t.TempDir()
	ds := NewDiskSpiller(dir, 100<<20)
	t.Cleanup(func() { _ = ds.Close() })

	ds.Spill([]byte(`{"test":true}`))
	ds.Close()

	if fc, err := ds.FileCount(); err != nil || fc != 1 {
		t.Fatalf("expected 1 file, got %d", fc)
	}

	if err := ds.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	if fc, err := ds.FileCount(); err != nil || fc != 0 {
		t.Fatalf("expected 0 files after cleanup, got %d", fc)
	}
}

func TestDiskSpiller_RecoverEmpty(t *testing.T) {
	dir := t.TempDir()
	ds := NewDiskSpiller(dir, 100<<20)
	t.Cleanup(func() { _ = ds.Close() })

	count := 0
	for range ds.Recover() {
		count++
	}
	if count != 0 {
		t.Fatalf("expected 0 records from empty spiller, got %d", count)
	}
}

func TestDiskSpiller_FileCount(t *testing.T) {
	dir := t.TempDir()
	ds := NewDiskSpiller(dir, 100<<20)
	t.Cleanup(func() { _ = ds.Close() })

	if fc, err := ds.FileCount(); err != nil || fc != 0 {
		t.Fatalf("expected 0 files initially, got %d", fc)
	}

	ds.Spill([]byte(`test`))
	ds.Close()

	if fc, err := ds.FileCount(); err != nil || fc != 1 {
		t.Fatalf("expected 1 file, got %d", fc)
	}
}

func TestDiskSpiller_FileCountReportsDirectoryErrors(t *testing.T) {
	skipPOSIXOnlyOnWindows(t, "reading a regular file as a directory is ENOTDIR here but a not-exist error on Windows, which walFiles deliberately treats as an empty WAL")
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	ds := NewDiskSpiller(path, 100<<20)
	t.Cleanup(func() { _ = ds.Close() })

	if _, err := ds.FileCount(); err == nil {
		t.Fatal("FileCount hid the WAL directory read error as zero files")
	}
}

func TestDiskSpiller_RecoverAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	ds := NewDiskSpiller(dir, 30) // force rotation
	t.Cleanup(func() { _ = ds.Close() })

	data := []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "cccccccccccccccc"}
	for _, d := range data {
		if err := ds.Spill([]byte(d)); err != nil {
			t.Fatalf("Spill: %v", err)
		}
	}
	ds.Close()

	var recovered []string
	for d := range ds.Recover() {
		recovered = append(recovered, string(d))
	}
	if len(recovered) != 3 {
		t.Fatalf("expected 3 records, got %d", len(recovered))
	}
}

func TestDiskSpiller_DirCreation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "wal")
	ds := NewDiskSpiller(dir, 100<<20)
	t.Cleanup(func() { _ = ds.Close() })

	if err := ds.Spill([]byte("test")); err != nil {
		t.Fatalf("Spill with nested dir: %v", err)
	}

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Fatal("expected directory to be created")
	}
}

func TestDiskSpiller_ReplayPreservesFailedAndUnreadRecords(t *testing.T) {
	skipPOSIXOnlyOnWindows(t, "trimAcknowledged replaces the WAL with os.Rename over the name the spiller still holds open; Windows refuses that")
	dir := t.TempDir()
	ds := NewDiskSpiller(dir, 100<<20)
	t.Cleanup(func() { _ = ds.Close() })

	for _, record := range []string{"first", "poison", "last"} {
		if err := ds.Spill([]byte(record)); err != nil {
			t.Fatalf("Spill: %v", err)
		}
	}
	if err := ds.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	count, err := ds.Replay(func(data []byte) error {
		if string(data) == "poison" {
			return errors.New("send failed")
		}
		return nil
	})
	if err == nil {
		t.Fatal("expected replay error")
	}
	if count != 1 {
		t.Fatalf("expected 1 acknowledged record, got %d", count)
	}

	var remaining []string
	for data := range ds.Recover() {
		remaining = append(remaining, string(data))
	}
	if len(remaining) != 2 || remaining[0] != "poison" || remaining[1] != "last" {
		t.Fatalf("remaining records = %v, want [poison last]", remaining)
	}
}

func TestDiskSpiller_ReplayPreservesPartialTail(t *testing.T) {
	skipPOSIXOnlyOnWindows(t, "trimAcknowledged replaces the WAL with os.Rename over the name the spiller still holds open; Windows refuses that")
	dir := t.TempDir()
	path := filepath.Join(dir, "wal-1.ndjson")
	if err := os.WriteFile(path, []byte("complete\npartial"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	ds := NewDiskSpiller(dir, 100<<20)
	t.Cleanup(func() { _ = ds.Close() })

	count, err := ds.Replay(func([]byte) error { return nil })
	if err == nil {
		t.Fatal("expected partial-record error")
	}
	if count != 1 {
		t.Fatalf("expected 1 acknowledged record, got %d", count)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("ReadFile: %v", readErr)
	}
	if string(got) != "partial" {
		t.Fatalf("remaining WAL = %q, want %q", got, "partial")
	}
}
