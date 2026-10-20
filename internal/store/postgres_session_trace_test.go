package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"cctrace/internal/sessiontrace"
)

func TestTraceDemandLeaseFreshnessAndReadOnly(t *testing.T) {
	// Shared testcontainers database is leased per test; fixture cleanup is serial.
	s := acquireTestStore(t)
	truncateTables(t, s)
	if _, err := s.pool.Exec(context.Background(), `TRUNCATE session_trace_snapshots CASCADE`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	id := "trace-lease-test"
	a := SessionAnalysisAccess{User: true, UserID: "trace-owner", OwnerUserID: "trace-owner", DashboardUserID: 73}
	records := []*SessionRecord{{SessionID: id, Ts: base, UUID: "one", UserID: a.UserID, ProfileEmail: "owner@example.test", Agent: "claude", RecordType: "user", SourceFile: "main.jsonl", Raw: json.RawMessage(`{"message":{"content":"PRIVATE"}}`)}}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	read, err := s.TraceOverview(ctx, id, a, "")
	if err != nil || read.State != "unavailable" {
		t.Fatalf("initial=%+v err=%v", read, err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM session_trace_leases WHERE session_id=$1`, id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("GET enqueued demand=%d err=%v", n, err)
	}
	first, err := s.TraceDemand(ctx, id, a, TraceDemandRequest{Mode: "overview"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.TraceDemand(ctx, id, a, TraceDemandRequest{Mode: "overview", LeaseID: first.LeaseID})
	if err != nil || first.SnapshotID != second.SnapshotID || first.LeaseID != second.LeaseID {
		t.Fatalf("heartbeat reset=%+v %+v %v", first, second, err)
	}
	shared, err := s.TraceDemand(ctx, id, a, TraceDemandRequest{Mode: "overview"})
	if err != nil || shared.SnapshotID != first.SnapshotID || shared.LeaseID == first.LeaseID {
		t.Fatalf("shared=%+v err=%v", shared, err)
	}
	if err := s.TraceRelease(ctx, id, a, first.LeaseID); err != nil {
		t.Fatal(err)
	}
	if err := s.TraceRelease(ctx, id, a, first.LeaseID); err != nil {
		t.Fatal(err)
	}
	if worked, err := s.RunTraceChunk(ctx); !worked || err != nil {
		t.Fatalf("worker=%v %v", worked, err)
	}
	ready, err := s.TraceOverview(ctx, id, a, shared.SnapshotID)
	if err != nil || ready.State != "ready" || ready.Coverage.ProcessedRows != 1 {
		t.Fatalf("ready=%+v err=%v", ready, err)
	}
	from, to := base.Add(-time.Second), base.Add(time.Minute)
	events, err := s.TraceEvents(ctx, id, a, TraceEventsRequest{SnapshotID: ready.SnapshotID, From: &from, To: &to, Limit: 1})
	if err != nil || len(events.Events) != 1 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	if _, err := s.TraceOverview(ctx, id, SessionAnalysisAccess{User: true, UserID: "other", DashboardUserID: 74}, ready.SnapshotID); !errors.Is(err, ErrSessionAnalysisNotFound) {
		t.Fatalf("foreign read=%v", err)
	}
	records[0].Ts = base.Add(time.Second)
	records[0].UUID = "two"
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TraceOverview(ctx, id, a, ready.SnapshotID); !errors.Is(err, ErrTraceSnapshot) {
		t.Fatalf("stale source cache=%v", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO excluded_sessions(session_id) VALUES($1) ON CONFLICT DO NOTHING`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TraceOverview(ctx, id, a, ""); !errors.Is(err, ErrSessionAnalysisNotFound) {
		t.Fatalf("excluded cache=%v", err)
	}
}
func TestTraceCursorBoundToScopeSnapshotAndRange(t *testing.T) {
	t.Parallel()
	c := traceCursor{Snapshot: "one", Scope: "owner", Selection: "range", TS: time.Now(), Key: "event"}
	raw := encodeTraceCursor(c)
	if _, err := decodeTraceCursor(raw, "one", "owner", "range"); err != nil {
		t.Fatal(err)
	}
	for _, v := range [][3]string{{"two", "owner", "range"}, {"one", "other", "range"}, {"one", "owner", "different"}} {
		if _, err := decodeTraceCursor(raw, v[0], v[1], v[2]); !errors.Is(err, ErrTraceInvalid) {
			t.Fatal("cursor escaped binding")
		}
	}
}

func TestTraceChunkRestartMetadataScopeAndContext(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	if _, err := s.pool.Exec(context.Background(), `TRUNCATE session_trace_snapshots CASCADE`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	id := "trace-chunk-test"
	a := SessionAnalysisAccess{DashboardUserID: 81, UserID: "owner"}
	_, err := s.pool.Exec(ctx, `INSERT INTO session_records(ts,session_id,uuid,record_type,profile_email,user_id,agent,source_file,raw)
 SELECT $1::timestamptz+make_interval(secs=>g),$2,g::text,'assistant','owner@example.test','owner','claude','main.jsonl','{"message":{"content":"synthetic point"}}'::jsonb FROM generate_series(1,$3)g`, base, id, traceDefaultChunkRows-1)
	if err != nil {
		t.Fatal(err)
	}
	records := []*SessionRecord{
		{SessionID: id, Ts: base.Add(time.Duration(traceDefaultChunkRows) * time.Second), UUID: "start", RecordType: "assistant", ProfileEmail: "owner@example.test", UserID: "owner", Agent: "claude", SourceFile: "main.jsonl", Raw: json.RawMessage(`{"message":{"content":[{"type":"tool_use","id":"exact","name":"Read","input":{"file_path":"PRIVATE ARG"}}]}}`)},
		{SessionID: id, Ts: base.Add(time.Duration(traceDefaultChunkRows+1) * time.Second), UUID: "end", RecordType: "user", ProfileEmail: "owner@example.test", UserID: "owner", Agent: "claude", SourceFile: "main.jsonl", Raw: json.RawMessage(`{"message":{"content":[{"type":"tool_result","tool_use_id":"exact","is_error":false,"content":"PRIVATE OUTPUT"}]}}`)},
		{SessionID: id, Ts: base.Add(time.Duration(traceDefaultChunkRows+2) * time.Second), UUID: "foreign", RecordType: "user", ProfileEmail: "foreign@example.test", UserID: "foreign", Agent: "claude", SourceFile: "main.jsonl", Raw: json.RawMessage(`{"message":{"content":"FOREIGN TEXT"}}`)},
	}
	if err = s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	d, err := s.TraceDemand(ctx, id, a, TraceDemandRequest{Mode: "overview"})
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := s.RunTraceChunk(ctx); !worked || err != nil {
		t.Fatalf("chunk1=%v %v", worked, err)
	}
	pending, err := s.TraceOverview(ctx, id, a, d.SnapshotID)
	if err != nil || pending.State != "building" || pending.Coverage.ProcessedRows != traceDefaultChunkRows || len(pending.Bins) == 0 {
		t.Fatalf("progress=%+v %v", pending, err)
	}
	// A new store object resumes database progress; no process-local partial map.
	resumed := &PgStore{pool: s.pool}
	if worked, err := resumed.RunTraceChunk(ctx); !worked || err != nil {
		t.Fatalf("restart=%v %v", worked, err)
	}
	ready, err := s.TraceOverview(ctx, id, a, d.SnapshotID)
	if err != nil || ready.State != "ready" || ready.Coverage.ProcessedRows != traceDefaultChunkRows+1 || ready.To == nil || !ready.To.Equal(records[1].Ts) {
		t.Fatalf("ready=%+v %v", ready, err)
	}
	from, to := records[0].Ts.Add(-500*time.Millisecond), base.Add(time.Hour)
	page, err := s.TraceEvents(ctx, id, a, TraceEventsRequest{SnapshotID: d.SnapshotID, From: &from, To: &to, Limit: 1000})
	if err != nil || len(page.Events) != 1 || page.Events[0].EndedAt == nil || page.Events[0].Status != "completed" {
		t.Fatalf("exact merge=%+v %v", page, err)
	}
	raw, err := s.TraceContext(ctx, id, a, TraceContextRequest{SnapshotID: d.SnapshotID, EventID: page.Events[0].EventID})
	if err != nil || len(raw.Records) != 2 {
		t.Fatalf("context=%d err=%v", len(raw.Records), err)
	}
	var leaked bool
	err = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_trace_events WHERE snapshot_id=$1 AND (metadata::text LIKE '%PRIVATE%' OR event::text LIKE '%PRIVATE%' OR source_key::text LIKE '%PRIVATE%'))`, d.SnapshotID).Scan(&leaked)
	if err != nil || leaked {
		t.Fatalf("raw persisted=%v err=%v", leaked, err)
	}
}

func TestTraceRangeIndependentExactBoundaryAndEpoch(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	if _, err := s.pool.Exec(context.Background(), `TRUNCATE session_trace_snapshots CASCADE`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	id := "trace-range-test"
	a := SessionAnalysisAccess{User: true, UserID: "owner", DashboardUserID: 82}
	records := []*SessionRecord{
		{SessionID: id, Ts: base, UUID: "call", RecordType: "tool_call", UserID: "owner", ProfileEmail: "owner@example.test", Agent: "codex", SourceFile: "rollout.jsonl", ToolCallID: "exact", ToolName: "exec_command", CodexTurnID: "turn", CodexTurnMetadataKnown: true, Raw: json.RawMessage(`{"payload":{"type":"function_call","call_id":"exact","name":"exec_command","arguments":"PRIVATE ARGS"}}`)},
		{SessionID: id, Ts: base.Add(10 * time.Second), UUID: "output", RecordType: "tool_output", UserID: "owner", ProfileEmail: "owner@example.test", Agent: "codex", SourceFile: "rollout.jsonl", ToolCallID: "exact", ToolName: "exec_command", CodexTurnID: "turn", CodexTurnMetadataKnown: true, Raw: json.RawMessage(`{"payload":{"type":"function_call_output","call_id":"exact","status":"completed","output":"PRIVATE OUTPUT"}}`)},
		{SessionID: id, Ts: base.Add(11 * time.Second), UUID: "input", RecordType: "user", UserID: "owner", ProfileEmail: "owner@example.test", Agent: "codex", SourceFile: "rollout.jsonl", Raw: json.RawMessage(`{"payload":{"type":"user_message","message":"PRIVATE INPUT"}}`)},
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	from, to := base.Add(9*time.Second), base.Add(12*time.Second)
	d, err := s.TraceDemand(ctx, id, a, TraceDemandRequest{Mode: "range", From: &from, To: &to})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		v, err := s.TraceOverview(ctx, id, a, d.SnapshotID)
		if err != nil {
			t.Fatal(err)
		}
		if v.State == "ready" {
			break
		}
		if worked, err := s.RunTraceChunk(ctx); !worked || err != nil {
			t.Fatalf("range chunk=%v %v", worked, err)
		}
	}
	page, err := s.TraceEvents(ctx, id, a, TraceEventsRequest{SnapshotID: d.SnapshotID, From: &from, To: &to, Limit: 1})
	if err != nil || len(page.Events) != 1 || page.Events[0].StartedAt == nil || !page.Events[0].StartedAt.Equal(base) || page.Events[0].EndedAt == nil || page.NextCursor == nil {
		t.Fatalf("boundary=%+v err=%v", page, err)
	}
	next, err := s.TraceEvents(ctx, id, a, TraceEventsRequest{SnapshotID: d.SnapshotID, From: &from, To: &to, Limit: 1, Cursor: *page.NextCursor})
	if err != nil || len(next.Events) != 1 || next.Events[0].Kind != "input" {
		t.Fatalf("next=%+v err=%v", next, err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = bumpSessionTimelinePolicyEpoch(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.TraceEvents(ctx, id, a, TraceEventsRequest{SnapshotID: d.SnapshotID, From: &from, To: &to}); !errors.Is(err, ErrTraceSnapshot) {
		t.Fatalf("policy cache=%v", err)
	}
}

func TestTraceExpiryCapPriorityAndPoolIsolation(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	if _, err := s.pool.Exec(context.Background(), `TRUNCATE session_trace_snapshots CASCADE`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	id := "trace-cap-test"
	a := SessionAnalysisAccess{User: true, UserID: "owner", DashboardUserID: 83}
	records := []*SessionRecord{{SessionID: id, Ts: base, UUID: "first", RecordType: "user", UserID: "owner", ProfileEmail: "owner@example.test", Agent: "claude", Raw: json.RawMessage(`{"message":{"content":"first"}}`)}, {SessionID: id, Ts: base.Add(time.Second), UUID: "second", RecordType: "user", UserID: "owner", ProfileEmail: "owner@example.test", Agent: "claude", Raw: json.RawMessage(`{"message":{"content":"second"}}`)}}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	d, err := s.TraceDemand(ctx, id, a, TraceDemandRequest{Mode: "overview"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, `UPDATE session_trace_snapshots SET processed_rows=$2 WHERE snapshot_id=$1`, d.SnapshotID, TraceOverviewRows-1); err != nil {
		t.Fatal(err)
	}
	from, to := base.Add(time.Second), base.Add(2*time.Second)
	r, err := s.TraceDemand(ctx, id, a, TraceDemandRequest{Mode: "range", From: &from, To: &to})
	if err != nil {
		t.Fatal(err)
	}
	// Occupy every ordinary query slot. The demand/read/worker paths use their
	// own pool or dedicated connection, so basic pool pressure cannot deadlock it.
	conns := make([]interface{ Release() }, 0, 10)
	for i := 0; i < int(s.pool.Config().MaxConns); i++ {
		c, err := s.pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	release := func() {
		for _, c := range conns {
			c.Release()
		}
		conns = nil
	}
	defer release()
	if worked, err := s.RunTraceChunk(ctx); !worked || err != nil {
		t.Fatalf("range priority=%v err=%v", worked, err)
	}
	progress, err := s.TraceOverview(ctx, id, a, r.SnapshotID)
	if err != nil || progress.Coverage.ProcessedRows != 1 {
		t.Fatalf("range=%+v err=%v", progress, err)
	}
	if err = s.TraceRelease(ctx, id, a, r.LeaseID); err != nil {
		t.Fatal(err)
	}
	if worked, err := s.RunTraceChunk(ctx); !worked || err != nil {
		t.Fatalf("cap chunk=%v err=%v", worked, err)
	}
	capped, err := s.TraceOverview(ctx, id, a, d.SnapshotID)
	if err != nil || capped.Coverage.State != "truncated" || capped.Coverage.ProcessedRows != TraceOverviewRows || capped.Coverage.Reason == nil || *capped.Coverage.Reason != "row_limit" {
		t.Fatalf("cap=%+v err=%v", capped, err)
	}
	release()
	if _, err = s.pool.Exec(ctx, `UPDATE session_trace_leases SET expires_at=now()-interval '1 second' WHERE session_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if worked, err := s.RunTraceChunk(ctx); worked || err != nil {
		t.Fatalf("expired processed=%v err=%v", worked, err)
	}
	revived, err := s.TraceDemand(ctx, id, a, TraceDemandRequest{Mode: "range", From: &from, To: &to, LeaseID: r.LeaseID})
	// A deleted screen lease cannot be resurrected using someone else's opaque ID.
	if !errors.Is(err, ErrTraceInvalid) || revived.LeaseID != "" {
		t.Fatalf("released lease reuse=%+v err=%v", revived, err)
	}
}

func TestTraceConcurrentDemandCopyGateCancellationAndDeletion(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE session_trace_snapshots CASCADE`); err != nil {
		t.Fatal(err)
	}
	id := "trace-safety-test"
	a := SessionAnalysisAccess{User: true, UserID: "owner", DashboardUserID: 84}
	base := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{SessionID: id, Ts: base, UUID: "user", RecordType: "user", UserID: "owner", ProfileEmail: "owner@example.test", Agent: "claude", Raw: json.RawMessage(`{"message":{"content":"body"}}`)}}); err != nil {
		t.Fatal(err)
	}
	snapshots := make(chan string, 6)
	failures := make(chan error, 6)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := s.TraceDemand(ctx, id, a, TraceDemandRequest{Mode: "overview"})
			if err != nil {
				failures <- err
				return
			}
			snapshots <- d.SnapshotID
		}()
	}
	wg.Wait()
	close(snapshots)
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if t.Failed() {
		return
	}
	snapshot := ""
	for id := range snapshots {
		if snapshot != "" && snapshot != id {
			t.Fatal("concurrent demand duplicated generation")
		}
		snapshot = id
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = lockSessionDeletionTargets(ctx, tx, []string{id}); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	_, blockedErr := s.RunTraceChunk(short)
	cancel()
	if blockedErr == nil {
		t.Fatal("blocked chunk ignored cancellation")
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var rows int64
	if err = s.pool.QueryRow(ctx, `SELECT processed_rows FROM session_trace_snapshots WHERE snapshot_id=$1`, snapshot).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("canceled chunk published rows=%d err=%v", rows, err)
	}
	var recordID int64
	if err = s.pool.QueryRow(ctx, `SELECT id FROM session_records WHERE session_id=$1`, id).Scan(&recordID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name='session_fact_copies_v1'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, `INSERT INTO session_record_copies(srec_id,session_id,original_session_id,record_type,ts) VALUES($1,$2,'original','user',$3)`, recordID, id, base); err != nil {
		t.Fatal(err)
	}
	if worked, err := s.RunTraceChunk(ctx); !worked || err != nil {
		t.Fatalf("copy gate off=%v %v", worked, err)
	}
	result, err := s.TraceOverview(ctx, id, a, snapshot)
	if err != nil || result.EventsCount != 1 {
		t.Fatalf("copy gate off=%+v %v", result, err)
	}
	if _, err = s.pool.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES('session_fact_copies_v1') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM schema_backfills WHERE name='session_fact_copies_v1'`)
	})
	tx, err = s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = bumpSessionTimelinePolicyEpoch(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	d, err := s.TraceDemand(ctx, id, a, TraceDemandRequest{Mode: "overview"})
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := s.RunTraceChunk(ctx); !worked || err != nil {
		t.Fatalf("copy gate on=%v %v", worked, err)
	}
	result, err = s.TraceOverview(ctx, id, a, d.SnapshotID)
	if err != nil || result.EventsCount != 0 {
		t.Fatalf("copy gate on=%+v %v", result, err)
	}
	if _, err = s.DeleteSession(ctx, id, "fixture", "test", false, false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.TraceOverview(ctx, id, a, d.SnapshotID); !errors.Is(err, ErrSessionAnalysisNotFound) {
		t.Fatalf("deleted cache=%v", err)
	}
	if _, err = s.SweepDeletedSessions(ctx, 10); err != nil {
		t.Fatal(err)
	}
	var retained int
	if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM session_trace_snapshots WHERE session_id=$1`, id).Scan(&retained); err != nil || retained != 0 {
		t.Fatalf("deleted trace retained=%d %v", retained, err)
	}
}

func TestTraceLateExactTurnMappingFencesSnapshot(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE session_trace_snapshots CASCADE`); err != nil {
		t.Fatal(err)
	}
	id := "trace-turn-mapping-test"
	a := SessionAnalysisAccess{User: true, UserID: "owner", DashboardUserID: 85}
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{SessionID: id, Ts: base, UUID: "exact-opener", RecordType: "user", UserID: "owner", ProfileEmail: "owner@example.test", Agent: "claude", SourceFile: "main.jsonl", PromptSource: "typed", Raw: json.RawMessage(`{"message":{"role":"user","content":"point"}}`)}}); err != nil {
		t.Fatal(err)
	}
	d, err := s.TraceDemand(ctx, id, a, TraceDemandRequest{Mode: "overview"})
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := s.RunTraceChunk(ctx); !worked || err != nil {
		t.Fatalf("trace=%v %v", worked, err)
	}
	var generation int64
	if err = s.pool.QueryRow(ctx, `SELECT generation FROM session_fact_dirty WHERE session_id=$1`, id).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if _, err = s.processSessionFact(ctx, id, generation); err != nil {
		t.Fatal(err)
	}
	if _, err = s.TraceOverview(ctx, id, a, d.SnapshotID); !errors.Is(err, ErrTraceSnapshot) {
		t.Fatalf("late mapping did not fence immutable snapshot: %v", err)
	}
	next, err := s.TraceDemand(ctx, id, a, TraceDemandRequest{Mode: "overview", LeaseID: d.LeaseID})
	if err != nil || next.SnapshotID == d.SnapshotID {
		t.Fatalf("new mapping generation=%+v %v", next, err)
	}
	if worked, err := s.RunTraceChunk(ctx); !worked || err != nil {
		t.Fatalf("remap=%v %v", worked, err)
	}
	from, to := base.Add(-time.Second), base.Add(time.Second)
	events, err := s.TraceEvents(ctx, id, a, TraceEventsRequest{SnapshotID: next.SnapshotID, From: &from, To: &to})
	if err != nil || len(events.Events) != 1 || events.Events[0].TurnKey == nil || *events.Events[0].TurnKey != "exact-opener" {
		t.Fatalf("exact turn missing: %+v %v", events, err)
	}
	context, err := s.TraceContext(ctx, id, a, TraceContextRequest{SnapshotID: next.SnapshotID, TurnKey: "exact-opener"})
	if err != nil || len(context.Records) != 1 || context.Records[0].UUID != "exact-opener" {
		t.Fatalf("canonical context records=%d err=%v", len(context.Records), err)
	}

}

func TestTracePersistentParentDependencies(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE session_trace_snapshots CASCADE`); err != nil {
		t.Fatal(err)
	}
	id := "trace-parent-test"
	a := SessionAnalysisAccess{User: true, UserID: "owner", DashboardUserID: 87}
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	records := []*SessionRecord{
		{SessionID: id, Ts: base, UUID: "parent", RecordType: "assistant", UserID: "owner", ProfileEmail: "owner@example.test", Agent: "claude", SourceFile: "main.jsonl", Raw: json.RawMessage(`{"message":{"content":[{"type":"tool_use","id":"parent","name":"Agent"}]}}`)},
		{SessionID: id, Ts: base.Add(time.Second), UUID: "child", RecordType: "assistant", UserID: "owner", ProfileEmail: "owner@example.test", Agent: "claude", SourceFile: "child.jsonl", AgentID: "child", Raw: json.RawMessage(`{"parent_tool_use_id":"parent","parent_source_file":"main.jsonl","message":{"content":[{"type":"tool_use","id":"read","name":"Read"}]}}`)},
		{SessionID: id, Ts: base.Add(2 * time.Second), UUID: "result", RecordType: "user", UserID: "owner", ProfileEmail: "owner@example.test", Agent: "claude", SourceFile: "child.jsonl", AgentID: "child", Raw: json.RawMessage(`{"message":{"content":[{"type":"tool_result","tool_use_id":"read","is_error":false}]}}`)},
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	d, err := s.TraceDemand(ctx, id, a, TraceDemandRequest{Mode: "overview"})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.traceTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	v, err := loadTraceSnapshot(ctx, tx, id, a, d.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	projected, _, err := loadTraceRecords(ctx, tx, v, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if err = mergeTraceEvents(ctx, tx, v.ID, sessiontrace.Normalize(projected[:2])); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	merge := func(events []sessiontrace.Event) {
		t.Helper()
		tx, err := s.traceTx(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = mergeTraceEvents(ctx, tx, v.ID, events); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	assertParent := func() {
		t.Helper()
		var raw []byte
		if err = s.pool.QueryRow(ctx, `SELECT event FROM session_trace_events WHERE snapshot_id=$1 AND event->>'label'='Read'`, v.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var e TraceEvent
		if err = json.Unmarshal(raw, &e); err != nil || e.ParentEventID == nil {
			t.Fatalf("parent lost on persistent merge: %v", err)
		}
	}
	merge(sessiontrace.Normalize(projected[2:]))
	assertParent()
	if _, err = s.pool.Exec(ctx, `DELETE FROM session_trace_events WHERE snapshot_id=$1`, v.ID); err != nil {
		t.Fatal(err)
	}
	merge(sessiontrace.Normalize(projected[1:2]))
	merge(sessiontrace.Normalize(projected[:1]))
	assertParent()
}

func TestTraceRangeSeedLimitAndNegativeDensity(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE session_trace_snapshots CASCADE`); err != nil {
		t.Fatal(err)
	}
	id := "trace-seed-limit-test"
	a := SessionAnalysisAccess{User: true, UserID: "owner", DashboardUserID: 88}
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{SessionID: id, Ts: base.Add(20 * time.Second), UUID: "owner", RecordType: "user", UserID: "owner", ProfileEmail: "owner@example.test", Agent: "claude", Raw: json.RawMessage(`{"message":{"content":"point"}}`)}}); err != nil {
		t.Fatal(err)
	}
	d, err := s.TraceDemand(ctx, id, a, TraceDemandRequest{Mode: "overview"})
	if err != nil {
		t.Fatal(err)
	}
	// Synthetic normalized cache fixture isolates range seed accounting from raw
	// ingestion. Six open crossings exceed an adaptively reduced four-row seed.
	fixture := []sessiontrace.Record{}
	for i := 1; i <= 6; i++ {
		fixture = append(fixture, sessiontrace.Record{ID: int64(i), SessionID: id, Agent: "codex", TS: base.Add(-time.Duration(i) * time.Second), UUID: fmt.Sprint(i), RecordType: "tool_call", SourceFile: "synthetic.jsonl", PayloadType: "function_call", ToolCallID: fmt.Sprint(i), ToolName: "Read", SourceScope: `{"profile_email":"owner@example.test","user_id":"owner"}`})
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = mergeTraceEvents(ctx, tx, d.SnapshotID, sessiontrace.Normalize(fixture)); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE session_trace_snapshots SET state='ready',coverage='complete' WHERE snapshot_id=$1`, d.SnapshotID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	from, to := base.Add(5*time.Second), base.Add(10*time.Second)
	r, err := s.TraceDemand(ctx, id, a, TraceDemandRequest{Mode: "range", From: &from, To: &to})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, `UPDATE session_trace_snapshots SET after_key='{"ChunkLimit":4}' WHERE snapshot_id=$1`, r.SnapshotID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if worked, err := s.RunTraceChunk(ctx); !worked || err != nil {
			t.Fatalf("seed chunk=%v %v", worked, err)
		}
		v, err := s.TraceOverview(ctx, id, a, r.SnapshotID)
		if err != nil {
			t.Fatal(err)
		}
		if v.State == "ready" {
			if v.Coverage.State != "truncated" || v.Coverage.Reason == nil || *v.Coverage.Reason != "boundary_limit" || v.EventsCount != 4 || len(v.Bins) != 1 {
				t.Fatalf("seed cap/density=%+v", v)
			}
			return
		}
	}
	t.Fatal("range seed never finished")
}

func TestTraceEpochBookkeepingDoesNotExpandBounds(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE session_trace_snapshots CASCADE`); err != nil {
		t.Fatal(err)
	}
	id := "trace-epoch-test"
	a := SessionAnalysisAccess{User: true, UserID: "owner", DashboardUserID: 90}
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	rows := []*SessionRecord{{SessionID: id, Ts: time.Unix(0, 0), UUID: "metadata", RecordType: "last-prompt", UserID: "owner", ProfileEmail: "owner@example.test", Agent: "claude", Raw: json.RawMessage(`{}`)}, {SessionID: id, Ts: base, UUID: "actual", RecordType: "user", UserID: "owner", ProfileEmail: "owner@example.test", Agent: "claude", Raw: json.RawMessage(`{"message":{"content":"actual point"}}`)}}
	if err := s.InsertSessionRecords(ctx, rows); err != nil {
		t.Fatal(err)
	}
	d, err := s.TraceDemand(ctx, id, a, TraceDemandRequest{Mode: "overview"})
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := s.RunTraceChunk(ctx); !worked || err != nil {
		t.Fatalf("epoch chunk=%v %v", worked, err)
	}
	v, err := s.TraceOverview(ctx, id, a, d.SnapshotID)
	if err != nil || v.From == nil || !v.From.Equal(base) || v.Coverage.ProcessedRows != 1 || v.EventsCount != 1 {
		t.Fatalf("epoch bounds=%+v err=%v", v, err)
	}
}
