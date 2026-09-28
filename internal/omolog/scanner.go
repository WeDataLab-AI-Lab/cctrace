package omolog

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"os"

	"cctrace/internal/jsonlscan"
)

// Metadata is session-level context found in the omo session header line.
type Metadata struct {
	CWD   string
	Title string
}

// ScanFile reads omo JSONL records from path starting at fromOffset. It
// returns only complete lines - partial lines at EOF are left for the next
// call. sessionID is the UUID extracted from the filename.
func ScanFile(path string, fromOffset int64, sessionID string) ([]*Record, int64, error) {
	records, newOffset, _, err := ScanFileWithMetadata(path, fromOffset, sessionID, Metadata{})
	return records, newOffset, err
}

// ScanFileWithMetadata is ScanFile plus initial/final session metadata. It is
// used for incremental scans that resume after the header line has already
// been consumed.
func ScanFileWithMetadata(path string, fromOffset int64, sessionID string, initial Metadata) ([]*Record, int64, Metadata, error) {
	return ScanFileWithMetadataContext(context.Background(), path, fromOffset, sessionID, initial)
}

func ScanFileWithMetadataContext(ctx context.Context, path string, fromOffset int64, sessionID string, initial Metadata) ([]*Record, int64, Metadata, error) {
	state := &scanState{sessionID: sessionID, cwd: initial.CWD, title: initial.Title}

	f, err := os.Open(path)
	if err != nil {
		return nil, fromOffset, metadataFromState(state), err
	}
	defer f.Close()

	if fromOffset > 0 {
		if _, err := f.Seek(fromOffset, io.SeekStart); err != nil {
			return nil, fromOffset, metadataFromState(state), err
		}
	}

	var records []*Record
	newOffset := fromOffset
	reader := bufio.NewReaderSize(f, 64*1024)
	for {
		lineBytes, consumed, complete, readErr := jsonlscan.ReadLine(ctx, reader)
		if errors.Is(readErr, jsonlscan.ErrLineTooLong) {
			log.Printf("[omolog] %s: skipping oversized jsonl line (%d bytes) at offset %d", path, consumed, newOffset)
			newOffset += consumed
			continue
		}
		if readErr != nil && readErr != io.EOF {
			return records, newOffset, metadataFromState(state), readErr
		}
		if complete {
			line := bytes.TrimRight(lineBytes, "\r\n")
			if len(line) > 0 {
				if rec := parseLine(line, state); rec != nil {
					records = append(records, rec)
				}
			}
			newOffset += consumed
		}
		if readErr == io.EOF {
			break
		}
	}

	return records, newOffset, metadataFromState(state), nil
}

// ScanMetadata reads complete records up to uptoOffset and returns the latest
// session context (cwd, title). It is used when an incremental sync starts
// after the header line has already been consumed.
func ScanMetadata(path string, uptoOffset int64) (Metadata, error) {
	return ScanMetadataContext(context.Background(), path, uptoOffset)
}

func ScanMetadataContext(ctx context.Context, path string, uptoOffset int64) (Metadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return Metadata{}, err
	}
	defer f.Close()

	var r io.Reader = f
	if uptoOffset > 0 {
		r = io.LimitReader(f, uptoOffset)
	}
	state := &scanState{}
	reader := bufio.NewReaderSize(r, 64*1024)
	for {
		lineBytes, _, complete, readErr := jsonlscan.ReadLine(ctx, reader)
		if errors.Is(readErr, jsonlscan.ErrLineTooLong) {
			log.Printf("[omolog] %s: skipping oversized jsonl line during metadata scan", path)
			continue
		}
		if readErr != nil && readErr != io.EOF {
			return metadataFromState(state), readErr
		}
		if complete {
			line := bytes.TrimRight(lineBytes, "\r\n")
			if len(line) > 0 {
				_ = parseLine(line, state)
			}
		}
		if readErr == io.EOF {
			break
		}
	}

	return metadataFromState(state), nil
}

func metadataFromState(state *scanState) Metadata {
	return Metadata{CWD: state.cwd, Title: state.title}
}
