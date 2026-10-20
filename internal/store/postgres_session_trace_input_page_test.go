package store

import (
	"errors"
	"testing"

	"cctrace/internal/sessiontrace"
)

func TestTraceInputCursorFencesSelectionRevisionAndTampering(t *testing.T) {
	t.Parallel()
	s := &PgStore{}
	cursor := traceInputCursor{Session: "session", Scope: "scope", Node: "node", Position: "next", Revision: 3, Epoch: 4}
	token, err := s.encodeInputCursor(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.decodeInputCursor(token, cursor); err != nil || got != "next" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	other := cursor
	other.Node = "other"
	if _, err := s.decodeInputCursor(token, other); !errors.Is(err, ErrTraceInvalid) {
		t.Fatal(err)
	}
	other = cursor
	other.Revision++
	if _, err := s.decodeInputCursor(token, other); !errors.Is(err, ErrTraceSnapshot) {
		t.Fatal(err)
	}
	if _, err := s.decodeInputCursor("x"+token, cursor); !errors.Is(err, ErrTraceInvalid) {
		t.Fatal(err)
	}
}

func TestTraceInputProvenanceRetainsRepeatedSourcesAndFragmentOrdinals(t *testing.T) {
	t.Parallel()
	start, end := 3, 5
	l := nativeNodeLookup{spans: map[string][]nativeSourceSpan{"merged": {
		{location: sessiontrace.SourceLocation{SourceRef: "left", UUID: "part-a", BlockStart: &start, BlockEnd: &end}, start: 0, end: 2},
		{location: sessiontrace.SourceLocation{SourceRef: "copy", UUID: "part-copy", BlockStart: &start, BlockEnd: &end}, start: 0, end: 2},
	}}}
	block, message := 1, 7
	request := sessiontrace.NativeRequest{Provenance: []sessiontrace.RequestProvenance{{RequestPath: "messages[7].content[1]", SourceRef: "merged", MessageOrdinal: &message, BlockOrdinal: &block, State: "observed"}}, InputPage: &sessiontrace.RequestInputPage{Items: []sessiontrace.RequestInputItem{{RequestPath: "messages[7].content[1]"}}}}
	recipe := traceNodeRow{id: 1, record: &SessionRecord{SessionID: "s"}}
	l.enrich(&request, recipe)
	if len(request.Provenance) != 2 {
		t.Fatalf("sources=%+v", request.Provenance)
	}
	for _, p := range request.Provenance {
		if *p.MessageOrdinal != 7 || *p.BlockOrdinal != 1 || *p.BlockStart != 4 || *p.BlockEnd != 5 || p.RawPath != "message.content[4]" {
			t.Fatalf("ordinal=%+v", p)
		}
	}
	if len(request.InputPage.Items[0].SourceRefs) != 2 {
		t.Fatalf("input refs=%+v", request.InputPage.Items)
	}
}

func TestTraceAuditValueSeparatesAbsentWithheldAndObservedEmpty(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		value any
		state string
	}{{nil, "not_collected"}, {"", "observed"}, {map[string]any{"stdout": ""}, "observed"}, {"[withheld by cctrace client]", "withheld"}, {map[string]any{"text": "[redacted by cctrace client]"}, "redacted"}} {
		if got := traceAuditValue(tc.value); got.State != tc.state {
			t.Fatalf("value=%v state=%q want=%q", tc.value, got.State, tc.state)
		}
	}
}
