package store

import (
	"testing"
	"time"

	"cctrace/internal/sessiontrace"
)

func TestTraceCodexToolTitleShowsObservedCommand(t *testing.T) {
	t.Parallel()
	row := traceNodeRow{record: &SessionRecord{Agent: "codex", RecordType: "tool_call", ToolName: "exec_command"}, raw: map[string]any{"payload": map[string]any{"type": "function_call", "name": "exec_command", "arguments": `{"cmd":"go test ./internal/sessiontrace"}`}}}
	if got := nodeToolPreview(row); got != "go test ./internal/sessiontrace" {
		t.Fatalf("preview=%q", got)
	}
	if got := nodeAvailability(map[string]any{"withheld": false}); got != "observed" {
		t.Fatalf("false withheld=%q", got)
	}
}

func TestTraceToolObservedIntervalRequiresUniqueOrderedResult(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Second)
	for _, tc := range []struct {
		name, kind string
		count      int
		end        time.Time
	}{
		{"ambiguous", "tool", 2, end},
		{"out_of_order", "tool", 1, start.Add(-time.Second)},
		{"agent_dispatch", "agent", 1, end},
		{"request", "request", 1, end},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := sessiontrace.Node{Kind: tc.kind, Timestamp: &start, Status: "unknown"}
			applyToolObservation(&n, traceToolObservation{HasChildren: true, ResultCount: tc.count, ResultTS: &tc.end})
			if n.StartedAt != nil || n.EndedAt != nil || n.Status != "unknown" {
				t.Fatalf("unobserved execution interval=%+v", n)
			}
		})
	}
}
