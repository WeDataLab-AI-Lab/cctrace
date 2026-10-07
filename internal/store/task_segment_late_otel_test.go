package store

import (
	"context"
	"testing"
	"time"
)

// Facts are only maintained once the backfill marker says the table is built;
// a fresh test database has neither, so writes would produce no facts at all
// and every assertion below would pass against an empty table.
func enableSegmentFacts(t *testing.T, s *PgStore) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, taskSegmentFactsBackfill); err != nil {
		t.Fatalf("clear fact marker: %v", err)
	}
	if err := s.BackfillTaskSegmentFacts(ctx); err != nil {
		t.Fatalf("complete empty backfill: %v", err)
	}
}

func segmentToolCount(t *testing.T, s *PgStore, sessionID string) (calls, fails int) {
	t.Helper()
	if err := s.pool.QueryRow(context.Background(),
		`SELECT COALESCE(sum(tool_call_count), 0), COALESCE(sum(tool_fail_count), 0)
		   FROM task_segment_facts WHERE session_id = $1`, sessionID).Scan(&calls, &fails); err != nil {
		t.Fatalf("read segment facts for %s: %v", sessionID, err)
	}
	return calls, fails
}

// The failure #435 is about. A segment's tokens come from session_records and
// its tool counts from otel_events, on separate ingest paths, and only a
// session_records write recomputes the fact. When the tool events land second
// the fact keeps saying zero -- and a session that has finished has no next
// write, so its last segment stays wrong for good. The user sees a segment with
// half a million output tokens and "0 calls".
func TestSegmentFactsPickUpToolEventsThatArriveLate(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	enableSegmentFacts(t, s)

	base := recentDay(39).Add(9 * time.Hour)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: base, SessionID: "late-otel", RecordType: "user", Agent: "claude",
			ProjectHash: "proj-a", ProfileEmail: "u@example.com", UUID: "l1", Raw: str("start")},
		{Ts: base.Add(2 * time.Minute), SessionID: "late-otel", RecordType: "user", Agent: "claude",
			ProjectHash: "proj-a", ProfileEmail: "u@example.com", UUID: "l2", Raw: str("more")},
	}); err != nil {
		t.Fatal(err)
	}

	// The session is over. Nothing will write session_records for it again.
	if calls, _ := segmentToolCount(t, s, "late-otel"); calls != 0 {
		t.Fatalf("tool_call_count = %d before any OTEL arrived, want 0", calls)
	}

	// Now the machine comes back online and uploads what it collected.
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: base.Add(30 * time.Second), EventName: "tool_result", SessionID: "late-otel",
			ProfileEmail: "u@example.com", ToolName: "Read", ToolSuccess: ptrBool(true), Agent: "claude"},
		{Ts: base.Add(45 * time.Second), EventName: "tool_result", SessionID: "late-otel",
			ProfileEmail: "u@example.com", ToolName: "Bash", ToolSuccess: ptrBool(true), Agent: "claude"},
		{Ts: base.Add(60 * time.Second), EventName: "tool_result", SessionID: "late-otel",
			ProfileEmail: "u@example.com", ToolName: "Edit", ToolSuccess: ptrBool(false), Agent: "claude"},
	}); err != nil {
		t.Fatal(err)
	}

	n, err := s.ReconcileTaskSegmentFactsForLateOTEL(ctx)
	if err != nil {
		t.Fatalf("ReconcileTaskSegmentFactsForLateOTEL: %v", err)
	}
	if n != 1 {
		t.Errorf("recomputed %d session(s), want 1", n)
	}

	calls, fails := segmentToolCount(t, s, "late-otel")
	if calls != 3 {
		t.Errorf("tool_call_count = %d, want 3 -- the segment still does not count events that arrived after it was built", calls)
	}
	if fails != 1 {
		t.Errorf("tool_fail_count = %d, want 1", fails)
	}
}

// The cursor counts rows, not timestamps, because a client that was offline
// uploads events that are old by ts and new by arrival -- which is precisely the
// case this reconciler exists for. A timestamp watermark steps over them.
func TestLateOTELReconcilerFollowsArrivalNotTimestamp(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	enableSegmentFacts(t, s)

	recent := recentDay(39).Add(9 * time.Hour)
	stale := recent.AddDate(0, 0, -30)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: recent, SessionID: "recent-ses", RecordType: "user", Agent: "claude",
			ProjectHash: "proj-a", ProfileEmail: "u@example.com", UUID: "r1", Raw: str("now")},
		{Ts: recent.Add(time.Minute), SessionID: "recent-ses", RecordType: "user", Agent: "claude",
			ProjectHash: "proj-a", ProfileEmail: "u@example.com", UUID: "r2", Raw: str("still now")},
		{Ts: stale, SessionID: "stale-ses", RecordType: "user", Agent: "claude",
			ProjectHash: "proj-a", ProfileEmail: "u@example.com", UUID: "s1", Raw: str("a month ago")},
		{Ts: stale.Add(time.Minute), SessionID: "stale-ses", RecordType: "user", Agent: "claude",
			ProjectHash: "proj-a", ProfileEmail: "u@example.com", UUID: "s2", Raw: str("and again")},
	}); err != nil {
		t.Fatal(err)
	}

	// The recent session's events arrive first and move the cursor forward.
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: recent.Add(time.Second), EventName: "tool_result", SessionID: "recent-ses",
			ProfileEmail: "u@example.com", ToolName: "Read", ToolSuccess: ptrBool(true), Agent: "claude"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReconcileTaskSegmentFactsForLateOTEL(ctx); err != nil {
		t.Fatal(err)
	}

	// Then the backlog lands: a month old by timestamp, newest by arrival.
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: stale.Add(time.Second), EventName: "tool_result", SessionID: "stale-ses",
			ProfileEmail: "u@example.com", ToolName: "Bash", ToolSuccess: ptrBool(true), Agent: "claude"},
		{Ts: stale.Add(2 * time.Second), EventName: "tool_result", SessionID: "stale-ses",
			ProfileEmail: "u@example.com", ToolName: "Edit", ToolSuccess: ptrBool(true), Agent: "claude"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReconcileTaskSegmentFactsForLateOTEL(ctx); err != nil {
		t.Fatal(err)
	}

	if calls, _ := segmentToolCount(t, s, "stale-ses"); calls != 2 {
		t.Errorf("stale session tool_call_count = %d, want 2 -- a backlog was stepped over", calls)
	}
}

// A tick with nothing new must not move the cursor and must not rewrite facts:
// this runs on a timer beside the retention reconcilers, and a pass that always
// finds work would recompute the same sessions forever.
func TestLateOTELReconcilerIsIdleWhenNothingArrived(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	enableSegmentFacts(t, s)

	base := recentDay(39).Add(9 * time.Hour)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: base, SessionID: "idle-ses", RecordType: "user", Agent: "claude",
			ProjectHash: "proj-a", ProfileEmail: "u@example.com", UUID: "i1", Raw: str("x")},
		{Ts: base.Add(time.Minute), SessionID: "idle-ses", RecordType: "user", Agent: "claude",
			ProjectHash: "proj-a", ProfileEmail: "u@example.com", UUID: "i2", Raw: str("y")},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: base.Add(time.Second), EventName: "tool_result", SessionID: "idle-ses",
			ProfileEmail: "u@example.com", ToolName: "Read", ToolSuccess: ptrBool(true), Agent: "claude"},
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ReconcileTaskSegmentFactsForLateOTEL(ctx); err != nil || n != 1 {
		t.Fatalf("first pass: n=%d err=%v, want 1", n, err)
	}
	for i := 0; i < 2; i++ {
		n, err := s.ReconcileTaskSegmentFactsForLateOTEL(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("pass %d recomputed %d session(s); nothing new arrived", i+2, n)
		}
	}
}
