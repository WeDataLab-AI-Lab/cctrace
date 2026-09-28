package jsonlscan

import (
	"bufio"
	"context"
	"errors"
	"io"
)

const MaxLineBytes = 16 * 1024 * 1024

var ErrLineTooLong = errors.New("jsonl line exceeds maximum size")

// ReadLine reads one JSONL line (newline included) from reader.
//
// Return contract:
//   - err == nil: line is complete, consumed is its byte length.
//   - err == io.EOF: the file ended; line holds the trailing partial line (may be empty).
//   - err == ErrLineTooLong: the line exceeded MaxLineBytes. The rest of the line
//     is drained up to and including its newline (or EOF), line is nil, and
//     consumed is the byte length of the whole skipped line. Callers may advance
//     their offset by consumed and keep scanning the file.
//   - any other err: reading failed; consumed counts the bytes read so far.
func ReadLine(ctx context.Context, reader *bufio.Reader) ([]byte, int64, bool, error) {
	return readLine(ctx, reader, MaxLineBytes)
}

func readLine(ctx context.Context, reader *bufio.Reader, maxLineBytes int) ([]byte, int64, bool, error) {
	var line []byte
	var consumed int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, consumed, false, err
		}
		chunk, err := reader.ReadSlice('\n')
		if len(chunk) > 0 {
			consumed += int64(len(chunk))
			if len(line)+len(chunk) > maxLineBytes {
				drained, drainErr := drainLine(ctx, reader, err)
				consumed += drained
				if drainErr != nil {
					return nil, consumed, false, drainErr
				}
				return nil, consumed, false, ErrLineTooLong
			}
			line = append(line, chunk...)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, consumed, false, ctxErr
			}
		}
		switch {
		case err == nil:
			return line, consumed, true, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			return line, consumed, false, io.EOF
		default:
			return line, consumed, false, err
		}
	}
}

// drainLine discards the remainder of an oversized line so the reader is left
// positioned at the start of the next line. lastErr is the error returned along
// with the chunk that tripped the size limit: a nil or io.EOF value means the
// line already ended, so there is nothing left to drain. It returns the number
// of extra bytes consumed.
func drainLine(ctx context.Context, reader *bufio.Reader, lastErr error) (int64, error) {
	if lastErr == nil || errors.Is(lastErr, io.EOF) {
		return 0, nil
	}
	if !errors.Is(lastErr, bufio.ErrBufferFull) {
		return 0, lastErr
	}

	var drained int64
	for {
		if err := ctx.Err(); err != nil {
			return drained, err
		}
		chunk, err := reader.ReadSlice('\n')
		drained += int64(len(chunk))
		switch {
		case err == nil:
			return drained, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			return drained, nil
		default:
			return drained, err
		}
	}
}
