package localapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"time"

	"cctrace/internal/codexlog"
	"cctrace/internal/jsonlscan"
	"cctrace/internal/sessionfacts"
	"cctrace/internal/sessionlog"
)

// Entry is one session file under a configured home, as the local API lists it.
type Entry struct {
	Agent   string // "claude" or "codex"
	Path    string // the file, symlinks resolved
	Home    string // the home it was found under, symlinks resolved
	LocalID string
	ModTime time.Time
	file    fileEntry
}

// Find lists the session files of the given homes under the local API's rules:
// the same directories, depth limits and symlink containment. A home that does
// not exist is skipped.
func Find(claudeDirs, codexDirs []string) ([]Entry, error) {
	roots, err := resolveRoots(claudeDirs, codexDirs)
	if err != nil {
		return nil, err
	}
	files, err := findEntries(roots)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, len(files))
	for i, e := range files {
		entries[i] = Entry{Agent: e.agent, Path: e.path, Home: e.root, LocalID: e.id, ModTime: e.info.ModTime(), file: e}
	}
	return entries, nil
}

// ErrChanged reports a file that changed or disappeared while it was read.
var ErrChanged = errors.New("session changed")

// RowRef places one physical line. ID is the analysed row the line became (an
// Input.Rows or Input.CodexRows ID), zero when it became none. Line counts the
// file's lines from 1, skipped oversized lines included; Offset is the line's
// first byte.
type RowRef struct {
	ID     int64
	Line   int
	Offset int64
}

type AnalyzeOptions struct {
	// ReadAt is the time an unfinished Codex turn is judged against.
	ReadAt time.Time
	// Visit sees every physical line once, in file order, while the file is
	// read: lines that become no row (Codex lifecycle events, unrecognised
	// lines) included, and a skipped oversized line as nil. The file is not yet
	// known to be stable then, so a caller keeps what it derives apart until
	// Analyze returns without error.
	Visit func(row RowRef, raw []byte)
}

// Analysis is the computation behind the analysis endpoint for one file.
type Analysis struct {
	Input sessionfacts.Input
	Facts sessionfacts.TimelineCommitOutput
	// Incomplete is oversized_jsonl or invalid_jsonl when a line could not be
	// read, which leaves the turns undeterminable; empty otherwise.
	Incomplete string
}

// Analyze reads one file and computes its facts as the analysis endpoint does.
// A file that changes while it is read returns ErrChanged.
func Analyze(ctx context.Context, e Entry, opts AnalyzeOptions) (Analysis, error) {
	return analyze(ctx, e.file, opts)
}

func analyze(ctx context.Context, e fileEntry, opts AnalyzeOptions) (Analysis, error) {
	f, err := openEntry(e)
	if err != nil {
		return Analysis{}, ErrChanged
	}
	defer f.Close()
	input := sessionfacts.Input{Agent: e.agent, ReadAt: opts.ReadAt}
	empty := ""
	snapshot := sessionfacts.Snapshot{AITitle: &empty, CustomTitle: &empty, AgentName: &empty}
	headIndex := map[string]int{}
	incomplete := ""
	physical := 0
	visit := func(id, offset int64, raw []byte) {
		physical++
		if opts.Visit != nil {
			opts.Visit(RowRef{ID: id, Line: physical, Offset: offset}, raw)
		}
	}
	if e.agent == "claude" {
		project := func(line []byte, offset int64) int64 {
			if line == nil {
				incomplete = "oversized_jsonl"
				return 0
			}
			if len(bytes.TrimSpace(line)) == 0 {
				return 0
			}
			var rec sessionlog.Record
			if json.Unmarshal(line, &rec) != nil {
				incomplete = "invalid_jsonl"
				return 0
			}
			projectTitle(&snapshot, rec.Type, line)
			if rec.Type != "user" && rec.Type != "assistant" && rec.Type != "system" && rec.Type != "progress" {
				return 0
			}
			rec.RawLine = line
			st := claudeStoreRecord(e, rec, line)
			row := projectClaude(&rec, &st, offset+1)
			input.Rows = append(input.Rows, row)
			addClaudeHead(&input, headIndex, &rec, &st, row.ID)
			return row.ID
		}
		err = readClaudeLines(ctx, f, e.info.Size(), 0, func(line []byte, offset, _ int64) error {
			visit(project(line, offset), offset, line)
			return nil
		})
	} else {
		activeTurn := ""
		life := map[string]int{}
		lastNext := int64(0)
		project := func(rec *codexlog.Record, line []byte, offset, next int64, meta codexlog.Metadata) int64 {
			if offset != lastNext {
				incomplete = "oversized_jsonl"
			}
			lastNext = next
			if len(bytes.TrimSpace(line)) == 0 {
				return 0
			}
			if !json.Valid(line) {
				incomplete = "invalid_jsonl"
				return 0
			}
			projectCodexLifecycle(&snapshot, life, &activeTurn, line)
			if rec == nil {
				return 0
			}
			row := codexProjection(rec, offset)
			row.UUID = hash(e.id, strconv.FormatInt(offset, 10))
			// The uploader sends this mapping (codex-tui is "cli"); the raw originator
			// made every interactive turn a non-request in the local analysis.
			row.Entrypoint = codexlog.EntrypointFromOriginator(meta.Originator, meta.IsSubagentFork)
			if row.RecordType == "usage" && row.TurnID == "" {
				row.TurnID = activeTurn
			}
			input.CodexRows = append(input.CodexRows, row)
			if row.RecordType == "usage" {
				input.Heads = append(input.Heads, sessionfacts.Head{ID: row.ID, UUID: row.UUID, TS: row.TS, Model: row.Model, InputTokens: max(0, row.InputTokens-row.CacheReadTokens), OutputTokens: row.OutputTokens, CacheReadTokens: row.CacheReadTokens})
			}
			return row.ID
		}
		skipped := func(from, to int64) error {
			if opts.Visit == nil {
				return nil
			}
			return skippedLines(f, from, to, func(offset int64) { visit(0, offset, nil) })
		}
		_, err = codexlog.ScanReaderContext(ctx, f, e.info.Size(), nativeID(e), func(rec *codexlog.Record, line []byte, offset, next int64, meta codexlog.Metadata) error {
			if offset != lastNext {
				// The scanner passes over an oversized line without a call.
				if err := skipped(lastNext, offset); err != nil {
					return err
				}
			}
			visit(project(rec, line, offset, next, meta), offset, line)
			return nil
		})
		if err == nil && lastNext < e.info.Size() {
			// Distinguish an ordinary unfinished line from a skipped oversized line.
			if _, seekErr := f.Seek(lastNext, io.SeekStart); seekErr == nil {
				_, _, _, tailErr := jsonlscan.ReadLine(ctx, bufio.NewReader(io.LimitReader(f, e.info.Size()-lastNext)))
				if errors.Is(tailErr, jsonlscan.ErrLineTooLong) {
					incomplete = "oversized_jsonl"
				}
			}
			err = skipped(lastNext, e.info.Size())
		}
	}
	if err != nil {
		return Analysis{}, err
	}
	if stable(e) != nil {
		return Analysis{}, ErrChanged
	}
	input.Snapshots = []sessionfacts.Snapshot{snapshot}
	// The worker's computation: it places Codex inputs by their turn id.
	return Analysis{Input: input, Facts: sessionfacts.ComputeWithTimelineAndCommits(input), Incomplete: incomplete}, nil
}

// skippedLines calls skip with the first byte of each line in [from, to) the
// Codex scanner passed over: every newline-terminated line there, and a final
// unterminated one only when it is over the line limit (a shorter one is an
// unfinished line, which no reader reports).
func skippedLines(f *os.File, from, to int64, skip func(offset int64)) error {
	buf := make([]byte, 64*1024)
	start := from
	for pos := from; pos < to; {
		n, err := f.ReadAt(buf[:min(int64(len(buf)), to-pos)], pos)
		for i, b := range buf[:n] {
			if b == '\n' {
				skip(start)
				start = pos + int64(i) + 1
			}
		}
		pos += int64(n)
		if n == 0 && err != nil {
			return err
		}
	}
	if to-start > int64(jsonlscan.MaxLineBytes) {
		skip(start)
	}
	return nil
}
