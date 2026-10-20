package codexlog

import (
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"cctrace/internal/jsonlscan"
)

func TestScanReaderMatchesFile(t *testing.T) {
	t.Parallel()
	header := `{"type":"session_meta","payload":{"id":"abc","cwd":"/myproject","originator":"codex_exec"}}`
	turn := `{"type":"turn_context","payload":{"cwd":"/myproject","model":"gpt-6-astra"}}`
	message := `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`
	usage := `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`
	child := `{"type":"session_meta","payload":{"id":"child-session","forked_from_id":"parent-session","thread_source":"subagent","cwd":"/myproject"}}`
	trigger := `{"type":"inter_agent_communication_metadata","payload":{"trigger_turn":true}}`
	for name, lines := range map[string][]string{
		"old":                {`{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}`, `{"type":"function_call","name":"shell","call_id":"call1","arguments":"{}"}`},
		"new":                {header, turn, message, `{"type":"event_msg","payload":{"type":"task_started"}}`, "not-json", ""},
		"unknown-first":      {"{}", header, message},
		"token-count":        {header, turn, usage, usage, strings.ReplaceAll(usage, "100", "175")},
		"token-usage-record": {header, `{"type":"token_usage_record","payload":{"usage":{"input_tokens":30,"output_tokens":5}}}`, usage},
		"copied-fork":        {child, header, turn, message, usage, trigger, message, strings.ReplaceAll(usage, "100", "175")},
		"legacy-fork":        {child, turn, message, usage},
		"no-copy-fork":       {child, header, turn, message, usage},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := writeCodexFile(t, lines)
			want, wantOffset, wantMeta, err := ScanFileWithMetadataContext(context.Background(), path, 0, "test-session", Metadata{})
			if err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if _, err := file.Seek(3, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			var got []*Record
			var lastMeta Metadata
			var lastOffset int64
			calls := 0
			meta, err := ScanReaderContext(context.Background(), file, wantOffset, "test-session", func(rec *Record, line []byte, offset, next int64, meta Metadata) error {
				if string(line) != lines[calls] || offset != lastOffset || next != offset+int64(len(lines[calls]))+1 {
					t.Fatalf("line %d: raw = %q, offsets = %d/%d", calls, line, offset, next)
				}
				// The file scanner's prepass sees trigger_turn even before that line.
				prefix := writeCodexFile(t, lines[:calls+1])
				_, _, prefixMeta, err := ScanFileWithMetadataContext(context.Background(), prefix, 0, "test-session", Metadata{ForkHasTriggerTurn: wantMeta.ForkHasTriggerTurn})
				if err != nil || meta != prefixMeta {
					t.Fatalf("line %d metadata = %+v, want %+v, err = %v", calls, meta, prefixMeta, err)
				}
				if rec != nil {
					got = append(got, rec)
				}
				calls++
				lastMeta, lastOffset = meta, next
				return nil
			})
			if err != nil || !reflect.DeepEqual(got, want) || meta != wantMeta || lastMeta != wantMeta || lastOffset != wantOffset || calls != len(lines) {
				t.Fatalf("reader differs: records equal = %v, meta = %+v, want %+v, calls = %d, err = %v", reflect.DeepEqual(got, want), meta, wantMeta, calls, err)
			}
		})
	}
}

func TestScanReaderSizeBound(t *testing.T) {
	t.Parallel()
	prefix := "{\"type\":\"session_meta\",\"payload\":{\"cwd\":\"/myproject\"}}\n"
	tail := "{\"type\":\"inter_agent_communication_metadata\",\"payload\":{\"trigger_turn\":true}}\n"
	for _, size := range []int64{0, int64(len(prefix)), int64(len(prefix) + len(tail) - 1)} {
		calls := 0
		meta, err := ScanReaderContext(context.Background(), strings.NewReader(prefix+tail), size, "test-session", func(*Record, []byte, int64, int64, Metadata) error { calls++; return nil })
		wantCalls := 1
		if size == 0 {
			wantCalls = 0
		}
		if err != nil || calls != wantCalls || meta.ForkHasTriggerTurn {
			t.Fatalf("size %d: calls = %d, meta = %+v, err = %v", size, calls, meta, err)
		}
	}
}

func TestScanReaderErrorsAndCancellation(t *testing.T) {
	t.Parallel()
	for _, cancelBefore := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if cancelBefore {
			cancel()
		}
		calls := 0
		_, err := ScanReaderContext(ctx, strings.NewReader("{}\n{}\n"), 6, "test-session", func(*Record, []byte, int64, int64, Metadata) error { calls++; cancel(); return nil })
		cancel()
		wantCalls := 1
		if cancelBefore {
			wantCalls = 0
		}
		if !errors.Is(err, context.Canceled) || calls != wantCalls {
			t.Fatalf("cancel before %v: calls = %d, err = %v", cancelBefore, calls, err)
		}
	}
	wantErr := errors.New("visit failed")
	_, err := ScanReaderContext(context.Background(), strings.NewReader("{}\n"), 3, "test-session", func(*Record, []byte, int64, int64, Metadata) error { return wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

func TestScanReaderOversizedLine(t *testing.T) {
	// MaxLineBytes is package-global, so this test must not run in parallel.
	previous := jsonlscan.MaxLineBytes
	jsonlscan.MaxLineBytes = 8
	t.Cleanup(func() { jsonlscan.MaxLineBytes = previous })
	data := "0123456789\n{}\n"
	calls := 0
	_, err := ScanReaderContext(context.Background(), strings.NewReader(data), int64(len(data)), "test-session", func(rec *Record, line []byte, offset, next int64, meta Metadata) error {
		calls++
		if string(line) != "{}" || offset != 11 || next != 14 {
			t.Fatalf("line = %q, offsets = %d/%d", line, offset, next)
		}
		return nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("calls = %d, err = %v", calls, err)
	}
}

func TestScanReaderCompleteLines(t *testing.T) {
	t.Parallel()
	data := "{}\r\n\n{\"partial\":true}"
	var offsets [][2]int64
	_, err := ScanReaderContext(context.Background(), strings.NewReader(data), int64(len(data)), "test-session", func(rec *Record, line []byte, offset, next int64, meta Metadata) error {
		offsets = append(offsets, [2]int64{offset, next})
		if rec != nil || strings.ContainsAny(string(line), "\r\n") {
			t.Fatalf("unexpected record or newline: %v %q", rec, line)
		}
		return nil
	})
	if err != nil || len(offsets) != 2 || offsets[0] != [2]int64{0, 4} || offsets[1] != [2]int64{4, 5} {
		t.Fatalf("offsets = %v, err = %v", offsets, err)
	}
}

func TestScanReaderCallbackEOF(t *testing.T) {
	t.Parallel()
	data := "{}\n{}\n"
	calls := 0
	_, err := ScanReaderContext(context.Background(), strings.NewReader(data), int64(len(data)), "test-session", func(*Record, []byte, int64, int64, Metadata) error {
		calls++
		return io.EOF
	})
	if err != nil || calls != 1 {
		t.Fatalf("calls = %d, err = %v", calls, err)
	}
}
