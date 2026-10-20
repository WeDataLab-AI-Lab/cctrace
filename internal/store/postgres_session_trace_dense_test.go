package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTraceDenseChunkAndContextPaging(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE session_trace_snapshots CASCADE`); err != nil {
		t.Fatal(err)
	}
	id := "trace-dense-test"
	a := SessionAnalysisAccess{User: true, UserID: "owner", DashboardUserID: 86}
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	_, err := s.pool.Exec(ctx, `INSERT INTO session_records(ts,session_id,uuid,record_type,profile_email,user_id,agent,source_file,raw)
 SELECT $1::timestamptz+make_interval(secs=>g),$2,g::text,'assistant','owner@example.test','owner','claude','main.jsonl',
 '{"message":{"content":"synthetic point"}}'::jsonb FROM generate_series(1,2000)g`, base, id)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = bumpSessionTimelineSourceRevisions(ctx, tx, []string{id}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	d, err := s.TraceDemand(ctx, id, a, TraceDemandRequest{Mode: "overview"})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	worked, err := s.RunTraceChunk(ctx)
	elapsed := time.Since(start)
	if !worked || err != nil {
		t.Fatalf("dense chunk=%v duration=%s err=%v", worked, elapsed, err)
	}
	v, err := s.TraceOverview(ctx, id, a, d.SnapshotID)
	if err != nil || v.Coverage.ProcessedRows != traceDefaultChunkRows || v.EventsCount != traceDefaultChunkRows {
		t.Fatalf("dense progress=%d events=%d duration=%s err=%v", v.Coverage.ProcessedRows, v.EventsCount, elapsed, err)
	}
	t.Logf("dense metadata chunk: %d rows/events, duration=%s", traceDefaultChunkRows, elapsed)
	for i := 0; i < 10; i++ {
		ready, err := s.TraceOverview(ctx, id, a, d.SnapshotID)
		if err != nil {
			t.Fatal(err)
		}
		if ready.State == "ready" {
			break
		}
		if worked, err := s.RunTraceChunk(ctx); !worked || err != nil {
			t.Fatalf("dense final=%v %v", worked, err)
		}
	}
	ready, err := s.TraceOverview(ctx, id, a, d.SnapshotID)
	if err != nil || ready.State != "ready" || ready.Coverage.ProcessedRows != 2000 || ready.EventsCount != 2000 {
		t.Fatalf("dense completion=%+v %v", ready, err)
	}
	if _, err = s.pool.Exec(ctx, `UPDATE session_trace_events SET turn_key='synthetic-turn' WHERE snapshot_id=$1`, d.SnapshotID); err != nil {
		t.Fatal(err)
	}
	page, err := s.TraceContext(ctx, id, a, TraceContextRequest{SnapshotID: d.SnapshotID, TurnKey: "synthetic-turn"})
	if err != nil || len(page.Records) != 100 || page.NextCursor == nil {
		t.Fatalf("context page records=%d cursor=%v err=%v", len(page.Records), page.NextCursor != nil, err)
	}
	next, err := s.TraceContext(ctx, id, a, TraceContextRequest{SnapshotID: d.SnapshotID, TurnKey: "synthetic-turn", Cursor: *page.NextCursor})
	if err != nil || len(next.Records) != 100 || !page.Records[99].Ts.Before(next.Records[0].Ts) {
		t.Fatalf("context next=%d err=%v", len(next.Records), err)
	}
	from, to := base, base.Add(time.Hour)
	cursor := encodeTraceCursor(traceCursor{Snapshot: d.SnapshotID, Scope: traceScope(a), Selection: traceSelection(&from, &to), TS: base, Key: "absent"})
	if _, err = s.TraceEvents(ctx, id, a, TraceEventsRequest{SnapshotID: d.SnapshotID, From: &from, To: &to, Cursor: cursor}); !errors.Is(err, ErrTraceInvalid) {
		t.Fatalf("forged tuple cursor=%v", err)
	}
}

func TestTraceDenseCallsAndResultsWithinBudget(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE session_trace_snapshots CASCADE`); err != nil {
		t.Fatal(err)
	}
	id := "trace-dense-pairs-test"
	a := SessionAnalysisAccess{User: true, UserID: "owner", DashboardUserID: 92}
	base := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	_, err := s.pool.Exec(ctx, `INSERT INTO session_records(ts,session_id,uuid,record_type,profile_email,user_id,agent,source_file,tool_call_id,tool_name,raw)
 SELECT $1::timestamptz+make_interval(secs=>g),$2,'start'||g,'tool_call','owner@example.test','owner','codex','rollout.jsonl',g::text,'Read',
 jsonb_build_object('payload',jsonb_build_object('type','function_call','call_id',g::text,'name','Read')) FROM generate_series(1,2000)g
 UNION ALL SELECT $1::timestamptz+make_interval(secs=>2000+g),$2,'end'||g,'tool_output','owner@example.test','owner','codex','rollout.jsonl',g::text,'Read',
 jsonb_build_object('payload',jsonb_build_object('type','function_call_output','call_id',g::text,'status','completed')) FROM generate_series(1,2000)g`, base, id)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = bumpSessionTimelineSourceRevisions(ctx, tx, []string{id}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	d, err := s.TraceDemand(ctx, id, a, TraceDemandRequest{Mode: "overview"})
	if err != nil {
		t.Fatal(err)
	}
	var prior int64
	var maxDuration time.Duration
	done := false
	for i := 0; i < 30; i++ {
		start := time.Now()
		worked, err := s.RunTraceChunk(ctx)
		duration := time.Since(start)
		if duration > maxDuration {
			maxDuration = duration
		}
		if !worked || err != nil {
			t.Fatalf("dense pair chunk=%v time=%s err=%v", worked, duration, err)
		}
		v, err := s.TraceOverview(ctx, id, a, d.SnapshotID)
		if err != nil {
			t.Fatal(err)
		}
		// Diagnostic only: keep this read outside the measured worker call and
		// leave every progress/completion condition unchanged if it fails.
		var chunkLimit int
		limitErr := s.pool.QueryRow(ctx, `SELECT COALESCE((after_key->>'ChunkLimit')::integer,$2)
 FROM session_trace_snapshots WHERE snapshot_id=$1`, d.SnapshotID, traceDefaultChunkRows).Scan(&chunkLimit)
		reason := ""
		if v.Coverage.Reason != nil {
			reason = *v.Coverage.Reason
		}
		t.Logf("dense pair iteration=%d rows=%d delta=%d state=%s coverage=%s reason=%q duration=%s chunk_limit=%d chunk_limit_error=%v",
			i+1, v.Coverage.ProcessedRows, v.Coverage.ProcessedRows-prior, v.State, v.Coverage.State, reason, duration, chunkLimit, limitErr)
		if v.Coverage.ProcessedRows < prior || v.Coverage.ProcessedRows-prior > TraceChunkRows {
			t.Fatal("chunk progress reset or exceeded cap")
		}
		prior = v.Coverage.ProcessedRows
		if v.State == "ready" {
			if v.Coverage.ProcessedRows != 4000 || v.EventsCount != 2000 {
				t.Fatalf("dense pairs rows=%d events=%d", v.Coverage.ProcessedRows, v.EventsCount)
			}
			done = true
			break
		}
		if v.State != "building" {
			t.Fatalf("dense ordinary pairing became %s", v.State)
		}
	}
	if !done {
		t.Fatal("dense pairs did not finish bounded chunks")
	}
	t.Logf("4000 start/result rows, 2000 exact spans, default chunk=%d max call duration=%s", traceDefaultChunkRows, maxDuration)
}
