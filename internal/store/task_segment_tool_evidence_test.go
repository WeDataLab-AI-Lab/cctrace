package store

import (
	"context"
	"testing"
	"time"
)

// "0 calls" carries two different meanings and the screen showed one label for
// both: a session that used no tools, and a session collected on a machine with
// no OTEL exporter. Measured on production, the second is 7,223 of the 7,989
// zero-call segments -- so the common case was the one being misreported.
//
// The token counts come from the JSONL sync and survive either way, which is
// what makes the wrong reading plausible: half a million output tokens beside
// "0 calls" looks like a measurement, not a gap.
func TestSegmentsReportWhetherToolUseWasMeasuredAtAll(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	enableSegmentFacts(t, s)

	base := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	mk := func(session, uuid1, uuid2 string) []*SessionRecord {
		return []*SessionRecord{
			{Ts: base, SessionID: session, RecordType: "user", Agent: "claude",
				ProjectHash: "proj-a", ProfileEmail: "u@example.com", UserID: "uid-1", UUID: uuid1, Raw: str("start")},
			{Ts: base.Add(time.Minute), SessionID: session, RecordType: "user", Agent: "claude",
				ProjectHash: "proj-a", ProfileEmail: "u@example.com", UserID: "uid-1", UUID: uuid2, Raw: str("more")},
		}
	}
	records := append(mk("instrumented", "a1", "a2"), mk("uninstrumented", "b1", "b2")...)
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	// task_type is assigned by the server's classifier, not carried on the record,
	// so a test that wants the by-type read path has to set it directly.
	if _, err := s.pool.Exec(ctx,
		`UPDATE session_records SET task_type = 'build' WHERE session_id IN ('instrumented','uninstrumented')`); err != nil {
		t.Fatal(err)
	}
	// Only one of the two machines was exporting. Note this session's tool events
	// carry no failures, so its counts are legitimately zero -- the point is that
	// its zero means something different from the other's.
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: base.Add(10 * time.Second), EventName: "user_prompt", SessionID: "instrumented",
			ProfileEmail: "u@example.com", UserID: "uid-1", Agent: "claude"},
	}); err != nil {
		t.Fatal(err)
	}

	page, err := s.TaskSegmentsByType(ctx, base.Add(-time.Hour), base.Add(time.Hour), "build", "u@example.com", "uid-1")
	if err != nil {
		t.Fatalf("TaskSegmentsByType: %v", err)
	}
	got := map[string]bool{}
	for _, seg := range page.Segments {
		got[seg.SessionID] = seg.ToolEvidence
		if seg.ToolCallCount != 0 {
			t.Errorf("%s tool_call_count = %d, want 0 for both", seg.SessionID, seg.ToolCallCount)
		}
	}
	if len(got) != 2 {
		t.Fatalf("sessions returned = %v, want both", got)
	}
	if !got["instrumented"] {
		t.Error("the session that emitted OTEL reports no tool evidence; its zero would read as a gap")
	}
	if got["uninstrumented"] {
		t.Error("the session with no OTEL reports tool evidence; its zero would read as a measurement")
	}
}
