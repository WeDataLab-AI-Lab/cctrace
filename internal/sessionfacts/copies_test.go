package sessionfacts

import (
	"fmt"
	"testing"
	"time"
)

func copyTestRecord(session string, id int64, sec int64, uuid, request, fork string) CopyRecord {
	return CopyRecord{
		ID: id, SessionID: session, Agent: "claude", RecordType: "assistant",
		UUID: uuid, RequestKey: request, ForkedFromSession: fork,
		TS: time.Unix(sec, 0).UTC(),
	}
}

func TestClassifyCopiesUsesBirthThenKeyCountAndExpandsRequest(t *testing.T) {
	records := []CopyRecord{
		copyTestRecord("original", 1, 10, "shared", "request-a", ""),
		copyTestRecord("original", 2, 11, "unique", "request-a", ""),
		copyTestRecord("copy", 3, 10, "shared", "request-a", ""),
		copyTestRecord("copy", 4, 12, "copy-only-in-same-request", "request-a", ""),
		copyTestRecord("copy", 5, 13, "outside-request", "request-b", ""),
	}
	got := ClassifyCopies(records, nil, 500000)
	if got.Skipped {
		t.Fatal("classification unexpectedly skipped")
	}
	want := []CopyMarker{
		{SRecID: 3, SessionID: "copy", OriginalSessionID: "original", RecordType: "assistant", TS: time.Unix(10, 0).UTC()},
		{SRecID: 4, SessionID: "copy", OriginalSessionID: "original", RecordType: "assistant", TS: time.Unix(12, 0).UTC()},
	}
	if fmt.Sprint(got.Markers) != fmt.Sprint(want) {
		t.Fatalf("markers = %#v, want %#v", got.Markers, want)
	}
}

func TestClassifyCopiesUsesForkAndContinuedInThenDeterministicTieBreak(t *testing.T) {
	records := []CopyRecord{
		copyTestRecord("z-parent", 1, 10, "one", "", ""),
		copyTestRecord("a-child", 2, 10, "one", "", "z-parent"),
		copyTestRecord("m-third", 3, 10, "one", "", ""),
	}
	continued := map[string][]string{"z-parent": {"m-third"}}
	got := ClassifyCopies(records, continued, 500000)
	if len(got.Markers) != 2 || got.Markers[0].SessionID != "a-child" || got.Markers[0].OriginalSessionID != "z-parent" || got.Markers[1].SessionID != "m-third" || got.Markers[1].OriginalSessionID != "z-parent" {
		t.Fatalf("markers = %#v, want forked and continued sessions copied to z-parent", got.Markers)
	}

	tie := []CopyRecord{
		copyTestRecord("z", 4, 10, "same", "", ""),
		copyTestRecord("a", 5, 10, "same", "", ""),
	}
	got = ClassifyCopies(tie, nil, 500000)
	if len(got.Markers) != 1 || got.Markers[0].SessionID != "z" || got.Markers[0].OriginalSessionID != "a" {
		t.Fatalf("tie markers = %#v, want z copied to lexically first a", got.Markers)
	}
}

func TestClassifyCopiesCodexResponseIDAndSkipCap(t *testing.T) {
	records := []CopyRecord{
		{ID: 1, SessionID: "a", Agent: "codex", RecordType: "usage", ResponseID: "response", TS: time.Unix(10, 0).UTC()},
		{ID: 2, SessionID: "b", Agent: "codex", RecordType: "usage", ResponseID: "response", TS: time.Unix(10, 0).UTC()},
		{ID: 3, SessionID: "b", Agent: "codex", RecordType: "assistant", ResponseID: "response", TS: time.Unix(10, 0).UTC()},
	}
	got := ClassifyCopies(records, nil, 2)
	if !got.Skipped || len(got.Markers) != 0 {
		t.Fatalf("capped result = %#v, want skipped with no marker changes", got)
	}
	got = ClassifyCopies(records, nil, 500000)
	if len(got.Markers) != 1 || got.Markers[0].SessionID != "b" || got.Markers[0].OriginalSessionID != "a" {
		t.Fatalf("Codex markers = %#v, want b's usage copied to a", got.Markers)
	}
}
