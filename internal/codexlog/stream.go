package codexlog

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"cctrace/internal/jsonlscan"
)

// ScanReaderContext scans complete lines from byte zero through size without
// reopening or closing reader. It visits state and unrecognized lines with a nil
// record, and returns successfully when visit returns io.EOF. Offsets include
// line endings; line and Record.Raw exclude them. Oversized lines are skipped.
func ScanReaderContext(ctx context.Context, reader io.ReadSeeker, size int64, sessionID string, visit func(record *Record, line []byte, offset int64, nextOffset int64, meta Metadata) error) (Metadata, error) {
	state := scanStateFromMetadata(sessionID, Metadata{})
	state.turnMetadataScanned = true
	if err := ctx.Err(); err != nil {
		return metadataFromState(state), err
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return metadataFromState(state), err
	}
	found, err := scanReaderForTriggerTurn(ctx, io.LimitReader(reader, size))
	if err != nil {
		return metadataFromState(state), err
	}
	state.forkHasTriggerTurn = found
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return metadataFromState(state), err
	}

	buffer := bufio.NewReaderSize(io.LimitReader(reader, size), 64*1024)
	var offset int64
	for {
		lineBytes, consumed, complete, readErr := jsonlscan.ReadLine(ctx, buffer)
		if errors.Is(readErr, jsonlscan.ErrLineTooLong) {
			offset += consumed
			continue
		}
		if readErr != nil && readErr != io.EOF {
			return metadataFromState(state), readErr
		}
		if complete {
			line := bytes.TrimRight(lineBytes, "\r\n")
			var rec *Record
			if len(line) > 0 {
				state.offset = offset
				if state.format == FormatUnknown {
					var probe struct {
						Type string `json:"type"`
					}
					_ = json.Unmarshal(line, &probe)
					state.format = DetectFormat(probe.Type)
					if state.format == FormatUnknown {
						state.format = FormatOld
					}
					if state.format == FormatOld {
						state.forkMetadataScanned = true
					}
				}
				switch state.format {
				case FormatNew:
					rec = parseNewFormatLine(line, state)
				case FormatOld:
					rec = parseOldFormatLine(line, state)
				}
			}
			if rec != nil {
				rec.Offset = offset
			}
			state.rateLimits = nil
			meta := metadataFromState(state)
			if err := visit(rec, line, offset, offset+consumed, meta); err != nil {
				if err == io.EOF {
					return meta, nil
				}
				return meta, err
			}
			offset += consumed
		}
		if readErr == io.EOF {
			return metadataFromState(state), nil
		}
	}
}

// Match scanForTriggerTurn's forward look using the caller's bounded reader.
func scanReaderForTriggerTurn(ctx context.Context, reader io.Reader) (bool, error) {
	buffer := bufio.NewReaderSize(reader, 64*1024)
	for {
		line, _, complete, readErr := jsonlscan.ReadLine(ctx, buffer)
		if errors.Is(readErr, jsonlscan.ErrLineTooLong) {
			continue
		}
		if readErr != nil && readErr != io.EOF {
			return false, readErr
		}
		if complete {
			var probe struct {
				Type    string `json:"type"`
				Payload struct {
					TriggerTurn bool `json:"trigger_turn"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &probe) == nil && probe.Type == "inter_agent_communication_metadata" && probe.Payload.TriggerTurn {
				return true, nil
			}
		}
		if readErr == io.EOF {
			return false, nil
		}
	}
}
