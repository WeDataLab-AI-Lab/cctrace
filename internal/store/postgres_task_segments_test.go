package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// userTurn produces a raw human message with role set -- task_type classification
// (insights.messagePrompt) requires role="user", unlike the segment-boundary SQL
// condition in the turns CTE, which only inspects content shape.
func userTurn(text string) json.RawMessage {
	return json.RawMessage(`{"message":{"role":"user","content":"` + text + `"}}`)
}

func TestTaskSegmentsByType_FiltersByTaskTypeWithinSegmentWindow(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Date(2026, 8, 24, 15, 0, 0, 0, time.UTC)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		// Segment "ts1": a testing-classified turn and a plain follow-up turn share
		// one segment (same project, no idle gap) -- the segment must surface once.
		{Ts: base, SessionID: "ts1", RecordType: "user", Agent: "claude", ProjectHash: "proj-a", ProfileEmail: "u@example.com", UserID: "u1", UUID: "seg1-t1", Raw: userTurn("please add a test for this")},
		{Ts: base.Add(90 * time.Second), SessionID: "ts1", RecordType: "assistant", Agent: "claude", ProfileEmail: "u@example.com", UserID: "u1", UUID: "seg1-a1", InputTokens: ptrInt(5), OutputTokens: ptrInt(9)},
		{Ts: base.Add(time.Minute), SessionID: "ts1", RecordType: "user", Agent: "claude", ProjectHash: "proj-a", ProfileEmail: "u@example.com", UserID: "u1", UUID: "seg1-t2", Raw: userTurn("thanks")},

		// Segment "ts2": classified as documentation, not testing -- must not appear.
		{Ts: base.Add(2 * time.Hour), SessionID: "ts2", RecordType: "user", Agent: "claude", ProjectHash: "proj-b", ProfileEmail: "u@example.com", UserID: "u1", UUID: "seg2-t1", Raw: userTurn("write the readme")},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	page, err := s.TaskSegmentsByType(ctx, base.Add(-time.Hour), base.Add(3*time.Hour), "testing", "", "u1")
	if err != nil {
		t.Fatalf("TaskSegmentsByType: %v", err)
	}
	if len(page.Segments) != 1 {
		t.Fatalf("segments = %+v, want 1", page.Segments)
	}
	if page.Segments[0].SessionID != "ts1" {
		t.Fatalf("session_id = %q, want ts1", page.Segments[0].SessionID)
	}
	if page.Segments[0].OutputTokens != 9 {
		t.Fatalf("output_tokens = %d, want 9", page.Segments[0].OutputTokens)
	}
	if page.Segments[0].ProjectHash != "proj-a" {
		t.Fatalf("project_hash = %q, want proj-a", page.Segments[0].ProjectHash)
	}
}

func TestTaskSegmentsByType_ScopesToCaller(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Date(2026, 8, 24, 16, 0, 0, 0, time.UTC)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: base, SessionID: "other-user", RecordType: "user", Agent: "claude", ProjectHash: "proj-a", ProfileEmail: "other@example.com", UserID: "other", UUID: "ou-t1", Raw: userTurn("please add a test for this")},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	page, err := s.TaskSegmentsByType(ctx, base.Add(-time.Hour), base.Add(time.Hour), "testing", "", "someone-else")
	if err != nil {
		t.Fatalf("TaskSegmentsByType: %v", err)
	}
	if len(page.Segments) != 0 {
		t.Fatalf("segments = %+v, want 0 (out of caller scope)", page.Segments)
	}
}

func TestTaskSegmentsByType_ReportsTheCeilingWithTheRows(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Date(2026, 8, 24, 15, 0, 0, 0, time.UTC)

	// Two segments, both carrying a testing prompt.
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: base, SessionID: "c1", RecordType: "user", Agent: "claude", ProjectHash: "p1", UserID: "u1", UUID: "c1-t1", TaskType: "testing", Raw: userTurn("add a test")},
		{Ts: base.Add(2 * time.Hour), SessionID: "c2", RecordType: "user", Agent: "claude", ProjectHash: "p2", UserID: "u1", UUID: "c2-t1", TaskType: "testing", Raw: userTurn("add another test")},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	page, err := s.TaskSegmentsByType(ctx, base.Add(-time.Hour), base.Add(4*time.Hour), "testing", "", "u1")
	if err != nil {
		t.Fatalf("TaskSegmentsByType: %v", err)
	}
	if page.Total != int64(len(page.Segments)) {
		t.Fatalf("total = %d, rows = %d: they must agree when nothing was cut",
			page.Total, len(page.Segments))
	}
	// The ceiling is not in play here, and saying it is would be its own lie.
	if page.Truncated {
		t.Fatalf("truncated = true for %d rows under the cap", len(page.Segments))
	}
	if page.Total == 0 {
		t.Fatalf("total = 0 -- the fixture stopped producing segments")
	}
}

// prod held 42 segments starting inside one second on 2026-09-09, one of them
// with 3,274 turns -- a bulk conversion that stamped every line with its own run
// time. The row must not present that moment as when the work happened (#686).
func TestTaskSegmentsByType_MarksASessionWithNoUsableTimeline(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Date(2026, 8, 24, 15, 0, 0, 0, time.UTC)

	imported := []*SessionRecord{}
	for i := 0; i < 25; i++ {
		imported = append(imported, &SessionRecord{
			Ts: base.Add(time.Duration(i) * time.Microsecond), SessionID: "imported",
			UUID: fmt.Sprintf("imported-%d", i), RecordType: "user", Agent: "claude",
			ProjectHash: "p1", UserID: "u1", TaskType: "testing", Raw: userTurn("add a test"),
		})
	}
	lived := []*SessionRecord{}
	for i := 0; i < 25; i++ {
		lived = append(lived, &SessionRecord{
			Ts: base.Add(3*time.Hour + time.Duration(i)*time.Minute), SessionID: "lived",
			UUID: fmt.Sprintf("lived-%d", i), RecordType: "user", Agent: "claude",
			ProjectHash: "p2", UserID: "u1", TaskType: "testing", Raw: userTurn("add a test"),
		})
	}
	if err := s.InsertSessionRecords(ctx, append(imported, lived...)); err != nil {
		t.Fatalf("insert: %v", err)
	}

	page, err := s.TaskSegmentsByType(ctx, base.Add(-time.Hour), base.Add(6*time.Hour), "testing", "", "u1")
	if err != nil {
		t.Fatalf("TaskSegmentsByType: %v", err)
	}
	seen := map[string]bool{}
	for _, seg := range page.Segments {
		seen[seg.SessionID] = seg.TimelineCollapsed
	}
	if len(seen) != 2 {
		t.Fatalf("sessions = %v, want both", seen)
	}
	if !seen["imported"] {
		t.Fatalf("imported session not marked -- its 25 records span microseconds")
	}
	// And the ordinary one is left alone; marking everything would say nothing.
	if seen["lived"] {
		t.Fatalf("lived session marked, but its records span 24 minutes")
	}
}
