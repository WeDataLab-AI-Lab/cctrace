package codexlog

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

// Metadata is session-level context found in Codex state records.
type Metadata struct {
	CWD                    string
	Model                  string
	Originator             string
	TokenUsageScanned      bool
	HasTotalTokenUsage     bool
	TotalInputTokens       int
	TotalCachedInputTokens int
	TotalOutputTokens      int
	HasTokenUsageRecord    bool
	ForkMetadataScanned    bool
	IsSubagentFork         bool
	ForkHistoryCopied      bool
	ForkBoundaryReached    bool
	ForkHasTriggerTurn     bool
	// OriginatorScanned records that the head of the file has been read for
	// its originator (see HeadOriginator). The scanners neither set nor clear
	// it: the Originator they return is the first one in the bytes they
	// covered, which is the file's own only when those bytes start at its head.
	OriginatorScanned bool
}

// ScanFile reads Codex JSONL records from path starting at fromOffset.
// It returns only complete lines — partial lines at EOF are left for the next call.
// sessionID is the UUID extracted from the filename.
func ScanFile(path string, fromOffset int64, sessionID string) ([]*Record, int64, error) {
	records, newOffset, _, err := ScanFileWithMetadata(path, fromOffset, sessionID, Metadata{})
	return records, newOffset, err
}

// ScanFileWithMetadata is ScanFile plus initial/final session metadata. It is
// used for incremental scans that start after earlier token_count snapshots.
func ScanFileWithMetadata(path string, fromOffset int64, sessionID string, initial Metadata) ([]*Record, int64, Metadata, error) {
	return ScanFileWithMetadataContext(context.Background(), path, fromOffset, sessionID, initial)
}

func ScanFileWithMetadataContext(ctx context.Context, path string, fromOffset int64, sessionID string, initial Metadata) ([]*Record, int64, Metadata, error) {
	records, _, newOffset, meta, err := ScanFileWithSamplesContext(ctx, path, fromOffset, sessionID, initial)
	return records, newOffset, meta, err
}

// ScanFileWithSamplesContext is ScanFileWithMetadataContext plus the
// account-scoped rate-limit readings found in the same pass.
//
// The readings travel on their own return value rather than as Records because
// they are not session-scoped: toStoreRecord forwards any non-empty record type
// into session_records with no whitelist, so a reading shaped as a Record would
// land in a table it does not belong to and nothing in the pipeline would
// object. A separate channel makes that impossible rather than merely
// discouraged.
//
// The existing entry points keep their signatures and simply drop the samples,
// so callers that only want records are unaffected.
func ScanFileWithSamplesContext(ctx context.Context, path string, fromOffset int64, sessionID string, initial Metadata) ([]*Record, []RateLimitSample, int64, Metadata, error) {
	state := scanStateFromMetadata(sessionID, initial)

	// The gate that suppresses inherited fork history keys off whether the file
	// contains a trigger_turn marker (copied-history fork) or not (no-copy fork).
	// The marker is written in the preamble, before the child's own records, so a
	// lightweight forward look at the new content settles it before emission
	// begins. Once known it is sticky across incremental scans.
	if !state.forkHasTriggerTurn {
		found, err := scanForTriggerTurn(ctx, path, fromOffset)
		if err != nil {
			return nil, nil, fromOffset, metadataFromState(state), err
		}
		if found {
			state.forkHasTriggerTurn = true
		}
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fromOffset, metadataFromState(state), err
	}
	defer f.Close()

	if fromOffset > 0 {
		if _, err := f.Seek(fromOffset, io.SeekStart); err != nil {
			return nil, nil, fromOffset, metadataFromState(state), err
		}
	}

	var records []*Record
	newOffset := fromOffset
	reader := bufio.NewReaderSize(f, 64*1024)
	for {
		lineBytes, consumed, complete, readErr := jsonlscan.ReadLine(ctx, reader)
		if errors.Is(readErr, jsonlscan.ErrLineTooLong) {
			log.Printf("[codexlog] %s: skipping oversized jsonl line (%d bytes) at offset %d", path, consumed, newOffset)
			newOffset += consumed
			continue
		}
		if readErr != nil && readErr != io.EOF {
			return records, state.rateLimits, newOffset, metadataFromState(state), readErr
		}
		if complete {
			line := bytes.TrimRight(lineBytes, "\r\n")
			if len(line) > 0 {
				if state.format == FormatUnknown {
					var probe struct {
						Type string `json:"type"`
					}
					_ = json.Unmarshal(line, &probe)
					f := DetectFormat(probe.Type)
					if f == FormatUnknown {
						f = FormatOld
					}
					state.format = f
					if f == FormatOld {
						state.forkMetadataScanned = true
					}
				}

				var rec *Record
				switch state.format {
				case FormatNew:
					rec = parseNewFormatLine(line, state)
				case FormatOld:
					rec = parseOldFormatLine(line, state)
				}
				if rec != nil {
					records = append(records, rec)
				}
			}
			newOffset += consumed
		}
		if readErr == io.EOF {
			break
		}
	}

	return records, state.rateLimits, newOffset, metadataFromState(state), nil
}

// ScanMetadata reads complete records up to uptoOffset and returns the latest
// session context. It is used when incremental sync starts after turn_context.
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
		lineBytes, consumed, complete, readErr := jsonlscan.ReadLine(ctx, reader)
		if errors.Is(readErr, jsonlscan.ErrLineTooLong) {
			log.Printf("[codexlog] %s: skipping oversized jsonl line (%d bytes) during metadata scan", path, consumed)
			continue
		}
		if readErr != nil && readErr != io.EOF {
			return metadataFromState(state), readErr
		}
		if complete {
			line := bytes.TrimRight(lineBytes, "\r\n")
			if len(line) > 0 {
				if state.format == FormatUnknown {
					var probe struct {
						Type string `json:"type"`
					}
					_ = json.Unmarshal(line, &probe)
					f := DetectFormat(probe.Type)
					if f == FormatUnknown {
						f = FormatOld
					}
					state.format = f
					if f == FormatOld {
						state.forkMetadataScanned = true
					}
				}
				switch state.format {
				case FormatNew:
					_ = parseNewFormatLine(line, state)
				case FormatOld:
					_ = parseOldFormatLine(line, state)
				}
			}
		}
		if readErr == io.EOF {
			break
		}
	}

	return metadataFromState(state), nil
}

// scanForTriggerTurn does a lightweight forward pass over path from fromOffset to
// EOF, reporting whether any inter_agent_communication_metadata line carries
// trigger_turn=true. It reuses the shared line reader and honors ctx so it costs
// no more than the main scan already does over the same bytes.
func scanForTriggerTurn(ctx context.Context, path string, fromOffset int64) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	if fromOffset > 0 {
		if _, err := f.Seek(fromOffset, io.SeekStart); err != nil {
			return false, err
		}
	}

	reader := bufio.NewReaderSize(f, 64*1024)
	for {
		lineBytes, _, complete, readErr := jsonlscan.ReadLine(ctx, reader)
		if errors.Is(readErr, jsonlscan.ErrLineTooLong) {
			continue
		}
		if readErr != nil && readErr != io.EOF {
			return false, readErr
		}
		if complete {
			line := bytes.TrimRight(lineBytes, "\r\n")
			if len(line) > 0 {
				var probe struct {
					Type    string `json:"type"`
					Payload struct {
						TriggerTurn bool `json:"trigger_turn"`
					} `json:"payload"`
				}
				if json.Unmarshal(line, &probe) == nil &&
					probe.Type == "inter_agent_communication_metadata" &&
					probe.Payload.TriggerTurn {
					return true, nil
				}
			}
		}
		if readErr == io.EOF {
			return false, nil
		}
	}
}

// HeadOriginator returns the originator of the session_meta that opens path,
// or "" when the first line is not a session_meta or carries none. It reads
// that line and nothing after it, so a caller resuming deep in a rollout can
// ask without paying for the rollout's size.
func HeadOriginator(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	line, _, _, err := jsonlscan.ReadLine(ctx, bufio.NewReaderSize(f, 64*1024))
	// An oversized line is one the scanners skip too: it has no originator to
	// give, which is an answer rather than a failure to read one.
	if err != nil && err != io.EOF && !errors.Is(err, jsonlscan.ErrLineTooLong) {
		return "", err
	}
	var head struct {
		Type    string             `json:"type"`
		Payload sessionMetaPayload `json:"payload"`
	}
	if json.Unmarshal(line, &head) != nil || head.Type != "session_meta" {
		return "", nil
	}
	return head.Payload.Originator, nil
}

func scanStateFromMetadata(sessionID string, meta Metadata) *scanState {
	return &scanState{
		sessionID:              sessionID,
		cwd:                    meta.CWD,
		originator:             meta.Originator,
		model:                  meta.Model,
		hasTotalTokenUsage:     meta.HasTotalTokenUsage,
		totalInputTokens:       meta.TotalInputTokens,
		totalCachedInputTokens: meta.TotalCachedInputTokens,
		totalOutputTokens:      meta.TotalOutputTokens,
		hasTokenUsageRecord:    meta.HasTokenUsageRecord,
		forkMetadataScanned:    meta.ForkMetadataScanned,
		isSubagentFork:         meta.IsSubagentFork,
		forkHistoryCopied:      meta.ForkHistoryCopied,
		forkBoundaryReached:    meta.ForkBoundaryReached,
		forkHasTriggerTurn:     meta.ForkHasTriggerTurn,
	}
}

func metadataFromState(state *scanState) Metadata {
	return Metadata{
		CWD:                    state.cwd,
		Model:                  state.model,
		Originator:             state.originator,
		TokenUsageScanned:      true,
		HasTotalTokenUsage:     state.hasTotalTokenUsage,
		TotalInputTokens:       state.totalInputTokens,
		TotalCachedInputTokens: state.totalCachedInputTokens,
		TotalOutputTokens:      state.totalOutputTokens,
		HasTokenUsageRecord:    state.hasTokenUsageRecord,
		ForkMetadataScanned:    state.forkMetadataScanned,
		IsSubagentFork:         state.isSubagentFork,
		ForkHistoryCopied:      state.forkHistoryCopied,
		ForkBoundaryReached:    state.forkBoundaryReached,
		ForkHasTriggerTurn:     state.forkHasTriggerTurn,
	}
}
