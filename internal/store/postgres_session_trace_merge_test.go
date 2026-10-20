package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"cctrace/internal/sessiontrace"
)

func TestTraceMergeAtLimit(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE session_trace_snapshots CASCADE`); err != nil {
		t.Fatal(err)
	}
	id := "trace-exact-2000-test"
	a := SessionAnalysisAccess{User: true, UserID: "owner", DashboardUserID: 91}
	base := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{SessionID: id, Ts: base, UUID: "owner", RecordType: "user", UserID: "owner", ProfileEmail: "owner@example.test", Agent: "claude", Raw: json.RawMessage(`{"message":{"content":"point"}}`)}}); err != nil {
		t.Fatal(err)
	}
	d, err := s.TraceDemand(ctx, id, a, TraceDemandRequest{Mode: "overview"})
	if err != nil {
		t.Fatal(err)
	}
	starts, ends := []sessiontrace.Record{}, []sessiontrace.Record{}
	for i := 1; i <= 2000; i++ {
		r := sessiontrace.Record{ID: int64(i), SessionID: id, Agent: "codex", TS: base.Add(time.Duration(i) * time.Second), UUID: fmt.Sprint("start", i), RecordType: "tool_call", SourceFile: "synthetic.jsonl", PayloadType: "function_call", ToolCallID: fmt.Sprint(i), ToolName: "Read", SourceScope: `{"profile_email":"owner@example.test","user_id":"owner"}`}
		starts = append(starts, r)
		r.ID += 2000
		r.UUID = fmt.Sprint("end", i)
		r.TS = r.TS.Add(time.Hour)
		r.RecordType = "tool_output"
		r.PayloadType = "function_call_output"
		r.ToolStatus = "completed"
		ends = append(ends, r)
	}
	// Direct storage-component fixture checks the exact legal metadata boundary;
	// production uses smaller raw chunks to retain the one-second budget.
	for _, records := range [][]sessiontrace.Record{starts, ends} {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = mergeTraceEvents(ctx, tx, d.SnapshotID, sessiontrace.Normalize(records)); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	err = s.pool.QueryRow(ctx, `SELECT count(*) FROM session_trace_events WHERE snapshot_id=$1 AND ended_at IS NOT NULL AND event->>'status'='completed'`, d.SnapshotID).Scan(&count)
	if err != nil || count != 2000 {
		t.Fatalf("exact2000 paired=%d err=%v", count, err)
	}
}
