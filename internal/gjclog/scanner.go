package gjclog

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

// Metadata is session-level context found in the gjc session header.
type Metadata struct {
	// SessionID carries the header-derived session id across incremental
	// scans. Subagent transcripts have arbitrary basenames (e.g.
	// 0-TokenLogProbe.jsonl), so SessionIDFromPath cannot recover the id from
	// the filename - it exists only in the transcript's own header line. Once
	// an incremental scan has consumed that header, Metadata is the only way
	// a later resume (with no filename-derived id to pass in) can recover it.
	SessionID string
	CWD       string
	Title     string
}

// ScanFile reads gjc session JSONL records from path starting at fromOffset.
// It returns only complete lines - partial lines at EOF are left for the next
// call. sessionID is the UUID extracted from the filename (main sessions) or
// from the transcript's own header (subagent transcripts).
func ScanFile(path string, fromOffset int64, sessionID string) ([]*Record, int64, error) {
	records, newOffset, _, err := ScanFileWithMetadata(path, fromOffset, sessionID, Metadata{})
	return records, newOffset, err
}

// ScanFileWithMetadata is ScanFile plus initial/final session metadata. It is
// used for incremental scans that resume after an earlier header parse.
func ScanFileWithMetadata(path string, fromOffset int64, sessionID string, initial Metadata) ([]*Record, int64, Metadata, error) {
	return ScanFileWithMetadataContext(context.Background(), path, fromOffset, sessionID, initial)
}

func ScanFileWithMetadataContext(ctx context.Context, path string, fromOffset int64, sessionID string, initial Metadata) ([]*Record, int64, Metadata, error) {
	state := scanStateFromMetadata(sessionID, initial)

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
			log.Printf("[gjclog] %s: skipping oversized jsonl line (%d bytes) at offset %d", path, consumed, newOffset)
			newOffset += consumed
			continue
		}
		if readErr != nil && readErr != io.EOF {
			return records, newOffset, metadataFromState(state), readErr
		}
		if complete {
			line := bytes.TrimRight(lineBytes, "\r\n")
			if len(line) > 0 {
				if recs := parseLine(line, state); recs != nil {
					records = append(records, recs...)
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
// session context (cwd, title from the header line).
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
			log.Printf("[gjclog] %s: skipping oversized jsonl line during metadata scan", path)
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

// scanStateFromMetadata seeds a scanState for a scan resume. sessionID (from
// the filename, when derivable) takes priority; otherwise the id carried in
// Metadata from an earlier header parse is used, so a subagent transcript
// resume with no filename-derived id does not lose session tagging.
func scanStateFromMetadata(sessionID string, meta Metadata) *scanState {
	sid := sessionID
	if sid == "" {
		sid = meta.SessionID
	}
	return &scanState{sessionID: sid, cwd: meta.CWD, title: meta.Title}
}

func metadataFromState(state *scanState) Metadata {
	return Metadata{SessionID: state.sessionID, CWD: state.cwd, Title: state.title}
}

// ScanTokenLog reads gjc project-local token-log.jsonl records from path
// starting at fromOffset, following the same incremental jsonlscan pattern as
// ScanFile. Malformed lines are skipped without aborting the scan.
func ScanTokenLog(path string, fromOffset int64) ([]*TokenLogRecord, int64, error) {
	return ScanTokenLogContext(context.Background(), path, fromOffset)
}

func ScanTokenLogContext(ctx context.Context, path string, fromOffset int64) ([]*TokenLogRecord, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fromOffset, err
	}
	defer f.Close()

	if fromOffset > 0 {
		if _, err := f.Seek(fromOffset, io.SeekStart); err != nil {
			return nil, fromOffset, err
		}
	}

	var records []*TokenLogRecord
	newOffset := fromOffset
	reader := bufio.NewReaderSize(f, 64*1024)
	for {
		lineBytes, consumed, complete, readErr := jsonlscan.ReadLine(ctx, reader)
		if errors.Is(readErr, jsonlscan.ErrLineTooLong) {
			log.Printf("[gjclog] %s: skipping oversized token-log line (%d bytes) at offset %d", path, consumed, newOffset)
			newOffset += consumed
			continue
		}
		if readErr != nil && readErr != io.EOF {
			return records, newOffset, readErr
		}
		if complete {
			line := bytes.TrimRight(lineBytes, "\r\n")
			if len(line) > 0 {
				if rec := parseTokenLogLine(line); rec != nil {
					records = append(records, rec)
				}
			}
			newOffset += consumed
		}
		if readErr == io.EOF {
			break
		}
	}

	return records, newOffset, nil
}
