package sessionlog

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"

	"cctrace/internal/jsonlscan"
)

// PeekCWD reads the first few lines of a JSONL file looking for a non-empty CWD field.
func PeekCWD(filePath string) string {
	f, err := os.Open(filePath)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)
	for i := 0; i < 10 && scanner.Scan(); i++ {
		var r Record
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			continue
		}
		if r.CWD != "" {
			return r.CWD
		}
	}
	return ""
}

// ScanFile reads new records from a JSONL file starting at fromOffset.
// It returns parsed records, the new offset (only advances for complete lines),
// and any I/O error encountered.
func ScanFile(path string, fromOffset int64) (records []*Record, newOffset int64, err error) {
	return ScanFileContext(context.Background(), path, fromOffset)
}

func ScanFileContext(ctx context.Context, path string, fromOffset int64) (records []*Record, newOffset int64, err error) {
	records, newOffset, _, err = ScanFileWithSkips(ctx, path, fromOffset)
	return records, newOffset, err
}

// SkippedSpan is the byte range of one line a scan passed over without
// yielding a record because it was longer than jsonlscan.MaxLineBytes.
type SkippedSpan struct {
	Start, End int64
}

// ScanFileWithSkips is ScanFileContext that also reports, in file order, the
// oversized lines it skipped. A skipped line is not just a missing record: it
// may have been the one where the session's cwd changed, so a caller that
// carries the cwd from one record to the next has to know the chain is broken
// there.
func ScanFileWithSkips(ctx context.Context, path string, fromOffset int64) (records []*Record, newOffset int64, skipped []SkippedSpan, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fromOffset, nil, err
	}
	defer f.Close()

	newOffset = fromOffset
	if fromOffset > 0 {
		if _, err := f.Seek(fromOffset, io.SeekStart); err != nil {
			return nil, fromOffset, nil, err
		}
	}

	reader := bufio.NewReaderSize(f, 64*1024)
	for {
		lineBytes, consumed, complete, readErr := jsonlscan.ReadLine(ctx, reader)
		if errors.Is(readErr, jsonlscan.ErrLineTooLong) {
			log.Printf("[sessionlog] %s: skipping oversized jsonl line (%d bytes) at offset %d", path, consumed, newOffset)
			skipped = append(skipped, SkippedSpan{Start: newOffset, End: newOffset + consumed})
			newOffset += consumed
			continue
		}
		if readErr != nil && readErr != io.EOF {
			return records, newOffset, skipped, readErr
		}
		if complete {
			line := bytes.TrimRight(lineBytes, "\r\n")
			if len(line) > 0 {
				var r Record
				if jsonErr := json.Unmarshal(line, &r); jsonErr == nil {
					r.RawLine = append([]byte(nil), line...)
					r.Offset = newOffset
					records = append(records, &r)
				}
			}
			newOffset += consumed
		}
		if readErr == io.EOF {
			break
		}
	}
	return records, newOffset, skipped, err
}
