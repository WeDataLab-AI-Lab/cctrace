package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestTraceSuccessfulCommitDoesNotRecoverExpiredCleanupContext(t *testing.T) {
	t.Parallel()
	parent := context.Background()
	expired, cancel := context.WithDeadline(parent, time.Now().Add(-time.Second))
	defer cancel()
	if traceChunkBudgetFailed(parent, expired, nil) {
		t.Fatal("successful commit recovered just because cleanup outlived chunk context")
	}
	if !traceChunkBudgetFailed(parent, expired, context.DeadlineExceeded) {
		t.Fatal("actual chunk deadline did not recover")
	}
	if traceChunkBudgetFailed(expired, expired, context.DeadlineExceeded) {
		t.Fatal("parent cancellation recovered cache")
	}
}

func TestTraceBudgetPreservesPartialAndResumesOnlyOnVisit(t *testing.T) {
	// The shared testcontainers fixture is leased serially by acquireTestStore.
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE session_trace_snapshots CASCADE`); err != nil {
		t.Fatal(err)
	}
	id := "trace-budget-resume-test"
	a := SessionAnalysisAccess{User: true, UserID: "owner", DashboardUserID: 104}
	base := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{SessionID: id, Ts: base, UUID: "a", RecordType: "user", UserID: "owner", ProfileEmail: "owner@example.test", Agent: "claude", Raw: json.RawMessage(`{"message":{"content":"synthetic"}}`)},
		{SessionID: id, Ts: base.Add(time.Second), UUID: "b", RecordType: "assistant", UserID: "owner", ProfileEmail: "owner@example.test", Agent: "claude", Raw: json.RawMessage(`{"message":{"content":"synthetic"}}`)},
		{SessionID: id, Ts: base.Add(2 * time.Second), UUID: "c", RecordType: "assistant", UserID: "owner", ProfileEmail: "owner@example.test", Agent: "claude", Raw: json.RawMessage(`{"message":{"content":"synthetic"}}`)},
	}); err != nil {
		t.Fatal(err)
	}
	d, err := s.TraceDemand(ctx, id, a, TraceDemandRequest{Mode: "overview"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, `UPDATE session_trace_snapshots SET after_key='{"ChunkLimit":2}' WHERE snapshot_id=$1`, d.SnapshotID); err != nil {
		t.Fatal(err)
	}
	if worked, err := s.RunTraceChunk(ctx); !worked || err != nil {
		t.Fatalf("prefix worked=%v err=%v", worked, err)
	}
	before, err := scanTraceSnapshot(s.pool.QueryRow(ctx, `SELECT `+traceSnapshotColumns+` FROM session_trace_snapshots WHERE snapshot_id=$1`, d.SnapshotID))
	if err != nil || before.Rows != 2 || before.Events != 2 || len(before.Bins) == 0 {
		t.Fatalf("prefix rows=%d events=%d err=%v", before.Rows, before.Events, err)
	}
	if err = s.recoverTraceBudget(ctx, d.SnapshotID, 1); err != nil {
		t.Fatal(err)
	}
	partial, err := s.TraceOverview(ctx, id, a, d.SnapshotID)
	if err != nil || partial.State != "ready" || partial.Coverage.State != "truncated" || partial.Coverage.Reason == nil || *partial.Coverage.Reason != "database_budget" || partial.EventsCount != before.Events || !reflect.DeepEqual(partial.Bins, before.Bins) || partial.Coverage.ProcessedRows != before.Rows {
		t.Fatalf("budget discarded prefix: state=%s coverage=%s rows=%d events=%d bins=%d err=%v", partial.State, partial.Coverage.State, partial.Coverage.ProcessedRows, partial.EventsCount, len(partial.Bins), err)
	}
	from, to := base, base.Add(time.Hour)
	page, err := s.TraceEvents(ctx, id, a, TraceEventsRequest{SnapshotID: d.SnapshotID, From: &from, To: &to, Limit: 1})
	if err != nil || len(page.Events) != 1 || page.NextCursor == nil {
		t.Fatalf("partial events=%d next=%v err=%v", len(page.Events), page.NextCursor != nil, err)
	}
	heartbeat, err := s.TraceDemand(ctx, id, a, TraceDemandRequest{Mode: "overview", LeaseID: d.LeaseID})
	if err != nil || heartbeat.State != "ready" {
		t.Fatalf("heartbeat resumed terminal budget: state=%s err=%v", heartbeat.State, err)
	}
	if worked, err := s.RunTraceChunk(ctx); worked || err != nil {
		t.Fatalf("budget retry loop worked=%v err=%v", worked, err)
	}
	visit, err := s.TraceDemand(ctx, id, a, TraceDemandRequest{Mode: "overview"})
	if err != nil || visit.State != "building" || visit.SnapshotID != d.SnapshotID {
		t.Fatalf("visit did not resume saved generation: state=%s err=%v", visit.State, err)
	}
	after, err := scanTraceSnapshot(s.pool.QueryRow(ctx, `SELECT `+traceSnapshotColumns+` FROM session_trace_snapshots WHERE snapshot_id=$1`, d.SnapshotID))
	if err != nil || after.Rows != before.Rows || after.After.TS != before.After.TS || after.After.UUID != before.After.UUID || after.After.ID != before.After.ID || after.After.ChunkLimit != traceDefaultChunkRows || after.Events != before.Events {
		t.Fatalf("resume reset saved prefix: rows=%d chunk=%d err=%v", after.Rows, after.After.ChunkLimit, err)
	}
	if _, err = s.TraceEvents(ctx, id, a, TraceEventsRequest{SnapshotID: d.SnapshotID, From: &from, To: &to, Cursor: *page.NextCursor}); !errors.Is(err, ErrTraceSnapshot) {
		t.Fatalf("old cursor survived resume: %v", err)
	}
	if worked, err := s.RunTraceChunk(ctx); !worked || err != nil {
		t.Fatalf("resume worked=%v err=%v", worked, err)
	}
	ready, err := s.TraceOverview(ctx, id, a, d.SnapshotID)
	if err != nil || ready.State != "ready" || ready.Coverage.ProcessedRows != 3 || ready.EventsCount != 3 {
		t.Fatalf("resume reread/skipped prefix: rows=%d events=%d err=%v", ready.Coverage.ProcessedRows, ready.EventsCount, err)
	}
	fresh, err := s.TraceEvents(ctx, id, a, TraceEventsRequest{SnapshotID: d.SnapshotID, From: &from, To: &to, Limit: 1})
	if err != nil || len(fresh.Events) != 1 || fresh.NextCursor == nil {
		t.Fatalf("new first page: events=%d err=%v", len(fresh.Events), err)
	}
	next, err := s.TraceEvents(ctx, id, a, TraceEventsRequest{SnapshotID: d.SnapshotID, From: &from, To: &to, Cursor: *fresh.NextCursor})
	if err != nil || len(next.Events) != 2 {
		t.Fatalf("new cursor: events=%d err=%v", len(next.Events), err)
	}
	if _, err = s.TraceEvents(ctx, id, a, TraceEventsRequest{SnapshotID: d.SnapshotID, From: &from, To: &to, Cursor: *page.NextCursor}); !errors.Is(err, ErrTraceSnapshot) {
		t.Fatalf("old cursor revived after completion: %v", err)
	}
	// The old daemon cleared summary fields, but left normalized event rows and
	// the source cursor behind. A new visit must repair that legacy cache too.
	if _, err = s.pool.Exec(ctx, `UPDATE session_trace_snapshots SET state='unavailable',coverage='unavailable',reason='database_budget',bins='[]',events_count=0 WHERE snapshot_id=$1`, d.SnapshotID); err != nil {
		t.Fatal(err)
	}
	legacy, err := s.TraceDemand(ctx, id, a, TraceDemandRequest{Mode: "overview"})
	if err != nil || legacy.State != "building" || legacy.SnapshotID != d.SnapshotID {
		t.Fatalf("legacy budget did not resume: %s %v", legacy.State, err)
	}
	if worked, err := s.RunTraceChunk(ctx); !worked || err != nil {
		t.Fatalf("legacy repair worked=%v err=%v", worked, err)
	}
	repaired, err := s.TraceOverview(ctx, id, a, d.SnapshotID)
	if err != nil || repaired.EventsCount != 3 || len(repaired.Bins) == 0 || repaired.Coverage.ProcessedRows != 3 {
		t.Fatalf("legacy events lost: rows=%d events=%d bins=%d err=%v", repaired.Coverage.ProcessedRows, repaired.EventsCount, len(repaired.Bins), err)
	}
}
