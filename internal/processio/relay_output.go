package processio

import "io"

func relayOutput(source io.Reader, destination io.Writer, count func([]byte), failed func(error), isEnd func(error) bool) error {
	buffer := make([]byte, 32<<10)
	for {
		n, readErr := source.Read(buffer)
		if n > 0 {
			remaining := buffer[:n]
			for len(remaining) > 0 {
				written, writeErr := destination.Write(remaining)
				if written > 0 {
					count(remaining[:written])
					remaining = remaining[written:]
				}
				if writeErr == nil && written == 0 {
					writeErr = io.ErrShortWrite
				}
				if writeErr != nil {
					failed(writeErr)
					return writeErr
				}
			}
		}
		if readErr != nil {
			if isEnd(readErr) {
				return nil
			}
			failed(readErr)
			return readErr
		}
		if n == 0 {
			failed(io.ErrNoProgress)
			return io.ErrNoProgress
		}
	}
}
