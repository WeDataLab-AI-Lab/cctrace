package gjclog

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writeGjcFile(t *testing.T, lines []string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "session-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		fmt.Fprintln(f, l)
	}
	f.Close()
	return f.Name()
}

func TestScanFile_Empty(t *testing.T) {
	path := writeGjcFile(t, nil)
	recs, offset, err := ScanFile(path, 0, "test-session")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Errorf("expected 0 records, got %d", len(recs))
	}
	if offset != 0 {
		t.Errorf("expected offset 0, got %d", offset)
	}
}

func TestScanFile_HeaderAndMessages(t *testing.T) {
	lines := []string{
		`{"type":"session","version":5,"id":"019f0000-0000-7000-8000-000000000001","timestamp":"2026-01-01T16:17:22.275Z","cwd":"/myproject","title":"a title"}`,
		`{"id":"m1","parentId":null,"timestamp":"2026-01-01T16:17:23.000Z","type":"message","message":{"role":"user","content":"hello"}}`,
		`{"type":"model_change","timestamp":"2026-01-01T16:17:23.500Z","model":"claude-opus-5"}`,
		`{"id":"m2","parentId":"m1","timestamp":"2026-01-01T16:17:24.000Z","type":"message","message":{"role":"assistant","model":"claude-opus-5","provider":"anthropic","content":[{"type":"text","text":"hi there"}],"usage":{"input":2,"output":190,"cacheRead":0,"cacheWrite":0,"totalTokens":192,"cost":{"total":0.01}}}}`,
		`{"id":"m3","parentId":"m2","timestamp":"2026-01-01T16:17:25.000Z","type":"message","message":{"role":"toolResult","toolName":"bash","toolCallId":"c1","isError":false,"content":"ok"}}`,
	}
	path := writeGjcFile(t, lines)

	recs, offset, err := ScanFile(path, 0, "test-session")
	if err != nil {
		t.Fatal(err)
	}
	// header and model_change are skipped; 3 message records returned
	if len(recs) != 3 {
		t.Fatalf("expected 3 records, got %d", len(recs))
	}
	if recs[0].RecordType != "user" || recs[0].CWD != "/myproject" || recs[0].Title != "a title" {
		t.Errorf("recs[0] = %+v, want user/myproject/a title", recs[0])
	}
	if recs[1].RecordType != "assistant" || recs[1].Model != "claude-opus-5" {
		t.Errorf("recs[1] = %+v, want assistant/claude-opus-5", recs[1])
	}
	if recs[1].CostUSD == nil || *recs[1].CostUSD != 0.01 {
		t.Fatalf("recs[1].CostUSD = %v, want 0.01", recs[1].CostUSD)
	}
	if recs[2].RecordType != "tool_result" || recs[2].ToolName != "bash" {
		t.Errorf("recs[2] = %+v, want tool_result/bash", recs[2])
	}
	fi, _ := os.Stat(path)
	if offset != fi.Size() {
		t.Errorf("offset = %d, want %d (file size)", offset, fi.Size())
	}
}

func TestScanFile_SkipsMalformedLine(t *testing.T) {
	lines := []string{
		`{"id":"m1","parentId":null,"timestamp":"2026-01-01T16:17:23.000Z","type":"message","message":{"role":"user","content":"before"}}`,
		`{not valid json`,
		`{"id":"m2","parentId":"m1","timestamp":"2026-01-01T16:17:24.000Z","type":"message","message":{"role":"user","content":"after"}}`,
	}
	path := writeGjcFile(t, lines)

	recs, _, err := ScanFile(path, 0, "test-session")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 records (malformed line skipped), got %d", len(recs))
	}
	if recs[0].Content != "before" || recs[1].Content != "after" {
		t.Errorf("unexpected content: %q, %q", recs[0].Content, recs[1].Content)
	}
}

func TestScanFile_IncrementalScan(t *testing.T) {
	lines := []string{
		`{"type":"session","version":5,"id":"s1","timestamp":"2026-01-01T16:17:22.275Z","cwd":"/myproject","title":"a title"}`,
		`{"id":"m1","parentId":null,"timestamp":"2026-01-01T16:17:23.000Z","type":"message","message":{"role":"user","content":"first"}}`,
	}
	path := writeGjcFile(t, lines)

	recs, offset, meta, err := ScanFileWithMetadata(path, 0, "s1", Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Content != "first" {
		t.Fatalf("first scan recs = %+v, want one record 'first'", recs)
	}
	if meta.CWD != "/myproject" || meta.Title != "a title" {
		t.Fatalf("meta = %+v, want cwd/title from header", meta)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, `{"id":"m2","parentId":"m1","timestamp":"2026-01-01T16:17:24.000Z","type":"message","message":{"role":"assistant","model":"claude-opus-5","content":"second"}}`)
	f.Close()

	recs2, _, meta2, err := ScanFileWithMetadata(path, offset, "s1", meta)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs2) != 1 || recs2[0].Content != "second" {
		t.Fatalf("second scan recs = %+v, want one record 'second'", recs2)
	}
	// metadata continuity: cwd/title carried across the incremental scan
	if recs2[0].CWD != "/myproject" || recs2[0].Title != "a title" {
		t.Fatalf("recs2[0] = %+v, want cwd/title carried over", recs2[0])
	}
	if meta2.CWD != "/myproject" || meta2.Title != "a title" {
		t.Fatalf("meta2 = %+v, want cwd/title preserved", meta2)
	}
}

func TestScanFile_PartialLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session-test.jsonl")
	full := `{"id":"m1","parentId":null,"timestamp":"2026-01-01T16:17:23.000Z","type":"message","message":{"role":"user","content":"hello"}}`

	f, _ := os.Create(path)
	fmt.Fprintln(f, full)
	fmt.Fprint(f, full[:len(full)/2]) // partial, no trailing newline
	f.Close()

	fi, _ := os.Stat(path)
	recs, offset, err := ScanFile(path, 0, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Errorf("expected 1 record (partial line skipped), got %d", len(recs))
	}
	if offset >= fi.Size() {
		t.Errorf("offset %d should be less than file size %d (partial not consumed)", offset, fi.Size())
	}
}

// Subagent transcripts have arbitrary basenames (e.g. 0-TokenLogProbe.jsonl),
// so SessionIDFromPath returns "" for them and the id exists only in the
// transcript's own header line. Once an incremental scan has consumed that
// header, the id must survive in Metadata so a later resume (which starts
// after the header, with no filename-derived sessionID) still tags records.
func TestScanFile_MetadataCarriesSessionIDAcrossIncrementalScans(t *testing.T) {
	path := writeGjcFile(t, []string{
		`{"type":"session","version":5,"id":"019f0000-0000-7000-8000-000000000001","timestamp":"2026-01-01T16:17:22.275Z","cwd":"/myproject"}`,
	})

	recs, offset, meta, err := ScanFileWithMetadata(path, 0, "", Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Fatalf("header-only scan recs = %+v, want none", recs)
	}
	if meta.SessionID != "019f0000-0000-7000-8000-000000000001" {
		t.Fatalf("meta.SessionID = %q, want the header id", meta.SessionID)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, `{"id":"m1","parentId":null,"timestamp":"2026-01-01T16:17:23.000Z","type":"message","message":{"role":"user","content":"hello"}}`)
	f.Close()

	// Resume with no filename-derived sessionID (as a subagent transcript scan
	// would), relying entirely on the carried-over Metadata.
	recs2, _, _, err := ScanFileWithMetadata(path, offset, "", meta)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs2) != 1 {
		t.Fatalf("resumed recs = %+v, want one record", recs2)
	}
	if recs2[0].SessionID != "019f0000-0000-7000-8000-000000000001" {
		t.Fatalf("resumed record SessionID = %q, want the header id carried via Metadata", recs2[0].SessionID)
	}
}

// The explicit sessionID argument (filename-derived, for main session files)
// takes priority over any sessionID carried in Metadata from a prior scan.
func TestScanFile_ExplicitSessionIDWinsOverMetadataSessionID(t *testing.T) {
	path := writeGjcFile(t, []string{
		`{"id":"m1","parentId":null,"timestamp":"2026-01-01T16:17:23.000Z","type":"message","message":{"role":"user","content":"hello"}}`,
	})

	recs, _, _, err := ScanFileWithMetadata(path, 0, "from-filename", Metadata{SessionID: "from-metadata"})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].SessionID != "from-filename" {
		t.Fatalf("recs = %+v, want SessionID from-filename (explicit arg wins)", recs)
	}
}

func TestScanMetadata(t *testing.T) {
	lines := []string{
		`{"type":"session","version":5,"id":"s1","timestamp":"2026-01-01T16:17:22.275Z","cwd":"/myproject","title":"a title"}`,
		`{"id":"m1","parentId":null,"timestamp":"2026-01-01T16:17:23.000Z","type":"message","message":{"role":"user","content":"hello"}}`,
	}
	path := writeGjcFile(t, lines)

	meta, err := ScanMetadata(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if meta.CWD != "/myproject" || meta.Title != "a title" {
		t.Errorf("meta = %+v, want cwd/myproject title/a title", meta)
	}
}

func TestScanTokenLog(t *testing.T) {
	lines := []string{
		`{"subagentId":"root","agent":"main","turn":1,"at":"2026-01-01T02:50:34.738Z","input":2,"output":367,"cacheRead":0,"cacheWrite":18580,"totalTokens":18949,"model":"claude-opus-5"}`,
		`{"subagentId":"sub-1","agent":"subagent","turn":1,"at":"2026-01-01T02:51:00.000Z","input":10,"output":20,"cacheRead":0,"cacheWrite":0,"totalTokens":30,"model":"claude-opus-5"}`,
	}
	path := writeGjcFile(t, lines)

	recs, offset, err := ScanTokenLog(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
	if recs[0].SubagentID != "root" || recs[1].SubagentID != "sub-1" {
		t.Errorf("unexpected subagent ids: %q, %q", recs[0].SubagentID, recs[1].SubagentID)
	}
	fi, _ := os.Stat(path)
	if offset != fi.Size() {
		t.Errorf("offset = %d, want %d", offset, fi.Size())
	}
}

func TestScanTokenLog_OffsetResume(t *testing.T) {
	path := writeGjcFile(t, []string{
		`{"subagentId":"root","agent":"main","turn":1,"at":"2026-01-01T02:50:34.738Z","input":2,"output":367,"cacheRead":0,"cacheWrite":18580,"totalTokens":18949,"model":"claude-opus-5"}`,
	})

	recs, offset, err := ScanTokenLog(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, `{"subagentId":"root","agent":"main","turn":2,"at":"2026-01-01T02:52:00.000Z","input":5,"output":10,"cacheRead":0,"cacheWrite":0,"totalTokens":15,"model":"claude-opus-5"}`)
	f.Close()

	recs2, _, err := ScanTokenLog(path, offset)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs2) != 1 || recs2[0].Turn != 2 {
		t.Fatalf("resumed recs = %+v, want one record with turn 2", recs2)
	}
}

func TestScanTokenLog_SkipsMalformedLine(t *testing.T) {
	lines := []string{
		`{"subagentId":"root","agent":"main","turn":1,"at":"2026-01-01T02:50:34.738Z","input":2,"output":367,"cacheRead":0,"cacheWrite":18580,"totalTokens":18949,"model":"claude-opus-5"}`,
		`{not valid json`,
		`{"subagentId":"root","agent":"main","turn":2,"at":"2026-01-01T02:52:00.000Z","input":5,"output":10,"cacheRead":0,"cacheWrite":0,"totalTokens":15,"model":"claude-opus-5"}`,
	}
	path := writeGjcFile(t, lines)

	recs, _, err := ScanTokenLog(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 records (malformed line skipped), got %d", len(recs))
	}
}
