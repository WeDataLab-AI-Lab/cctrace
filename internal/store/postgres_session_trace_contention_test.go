package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestTraceLeaseHeartbeatDoesNotWaitForTraceReader(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	id := "trace-heartbeat-lock-test"
	a := SessionAnalysisAccess{User: true, UserID: "owner", DashboardUserID: 101}
	if _, err := s.pool.Exec(ctx, `TRUNCATE session_trace_snapshots CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{SessionID: id, Ts: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC), UUID: "point", RecordType: "user", UserID: "owner", ProfileEmail: "owner@example.test", Agent: "claude", Raw: json.RawMessage(`{"message":{"content":"synthetic"}}`)}}); err != nil {
		t.Fatal(err)
	}
	d, err := s.TraceDemand(ctx, id, a, TraceDemandRequest{Mode: "overview"})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := s.traceTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Rollback(ctx) //nolint:errcheck
	if err = authorizeTrace(ctx, worker, id, a); err != nil {
		t.Fatal(err)
	}
	// The reader remains active. Renewal must finish without waiting for its
	// transaction to end; the shortened parent budget is the asserted boundary.
	fast, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	renewed, err := s.TraceDemand(fast, id, a, TraceDemandRequest{Mode: "overview", LeaseID: d.LeaseID})
	if err != nil || renewed.LeaseID != d.LeaseID || renewed.SnapshotID != d.SnapshotID {
		t.Fatalf("heartbeat waited for trace reader: %v", err)
	}
}

func TestTraceNewScreenDemandDoesNotWaitForWorkerClaim(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	id := "trace-new-screen-lock-test"
	a := SessionAnalysisAccess{User: true, UserID: "owner", DashboardUserID: 102}
	if _, err := s.pool.Exec(ctx, `TRUNCATE session_trace_snapshots CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{SessionID: id, Ts: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC), UUID: "point", RecordType: "user", UserID: "owner", ProfileEmail: "owner@example.test", Agent: "claude", Raw: json.RawMessage(`{"message":{"content":"synthetic"}}`)}}); err != nil {
		t.Fatal(err)
	}
	d, err := s.TraceDemand(ctx, id, a, TraceDemandRequest{Mode: "overview"})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := s.traceTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Rollback(ctx) //nolint:errcheck
	claimed, err := claimTraceSnapshot(ctx, worker)
	if err != nil || claimed.ID != d.SnapshotID {
		t.Fatalf("claim=%s err=%v", claimed.ID, err)
	}
	fast, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	screen, err := s.TraceDemand(fast, id, a, TraceDemandRequest{Mode: "overview"})
	if err != nil || screen.SnapshotID != d.SnapshotID || screen.LeaseID == d.LeaseID {
		t.Fatalf("new screen waited for worker claim: %v", err)
	}
}
