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
	f, err := os.Open(path)
	if err != nil {
		return nil, fromOffset, err
	}
	defer f.Close()

	newOffset = fromOffset
	if fromOffset > 0 {
		if _, err := f.Seek(fromOffset, io.SeekStart); err != nil {
			return nil, fromOffset, err
		}
	}

	reader := bufio.NewReaderSize(f, 64*1024)
	for {
		lineBytes, consumed, complete, readErr := jsonlscan.ReadLine(ctx, reader)
		if errors.Is(readErr, jsonlscan.ErrLineTooLong) {
			log.Printf("[sessionlog] %s: skipping oversized jsonl line (%d bytes) at offset %d", path, consumed, newOffset)
			newOffset += consumed
			continue
		}
		if readErr != nil && readErr != io.EOF {
			return records, newOffset, readErr
		}
		if complete {
			line := bytes.TrimRight(lineBytes, "\r\n")
			if len(line) > 0 {
				var r Record
				if jsonErr := json.Unmarshal(line, &r); jsonErr == nil {
					r.RawLine = append([]byte(nil), line...)
					records = append(records, &r)
				}
			}
			newOffset += consumed
		}
		if readErr == io.EOF {
			break
		}
	}
	return records, newOffset, err
}
