package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

func seedTurnContext(t *testing.T, s *PgStore) (string, SessionAnalysisAccess) {
	t.Helper()
	ctx := context.Background()
	id := t.Name() + "-" + uuid.NewString()
	a := SessionAnalysisAccess{User: true, UserID: "turn-owner", DashboardUserID: 991}
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	records := []*SessionRecord{{SessionID: id, Ts: base, UUID: "opener", RecordType: "user", UserID: a.UserID, ProfileEmail: "turn-owner@example.test", Agent: "claude", SourceFile: "main.jsonl", PromptSource: "typed", Raw: json.RawMessage(`{"message":{"role":"user","content":"first turn"}}`)}}
	for i := 0; i < 21; i++ {
		parent := "opener"
		if i > 0 {
			parent = fmt.Sprintf("assistant-%02d", i-1)
		}
		records = append(records, &SessionRecord{SessionID: id, Ts: base.Add(time.Second), UUID: fmt.Sprintf("assistant-%02d", i), ParentUUID: parent, RecordType: "assistant", UserID: a.UserID, ProfileEmail: "turn-owner@example.test", Agent: "claude", SourceFile: "main.jsonl", Raw: json.RawMessage(`{"message":{"role":"assistant","content":"answer"}}`)})
	}
	records = append(records, &SessionRecord{SessionID: id, Ts: base.Add(time.Second), UUID: "bookkeeping", ParentUUID: "assistant-20", RecordType: "last-prompt", UserID: a.UserID, ProfileEmail: "turn-owner@example.test", Agent: "claude", SourceFile: "main.jsonl", Raw: json.RawMessage(`{"secret":"hidden bookkeeping"}`)}, &SessionRecord{SessionID: id, Ts: base.Add(2 * time.Second), UUID: "second", ParentUUID: "assistant-20", RecordType: "user", UserID: a.UserID, ProfileEmail: "turn-owner@example.test", Agent: "claude", SourceFile: "main.jsonl", PromptSource: "typed", Raw: json.RawMessage(`{"message":{"role":"user","content":"second turn"}}`)})
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	return id, a
}

func publishTurnContext(t *testing.T, s *PgStore, id string) {
	t.Helper()
	var generation int64
	if err := s.pool.QueryRow(context.Background(), `SELECT generation FROM session_fact_dirty WHERE session_id=$1`, id).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if _, err := s.processSessionFact(context.Background(), id, generation); err != nil {
		t.Fatal(err)
	}
}

func TestTurnContextCanonicalPagingWithoutTrace(t *testing.T) {
	// The shared DB policy epoch and fact publisher are mutable; these fixtures
	// run serially with the existing store integration tests.
	s := acquireTestStore(t)
	id, a := seedTurnContext(t, s)
	ctx := context.Background()
	v, err := s.TurnContext(ctx, id, "opener", a, TurnContextRequest{})
	if err != nil || v.State != "pending" || len(v.Records) != 0 {
		t.Fatalf("pending=%+v err=%v", v, err)
	}
	publishTurnContext(t, s, id)
	// Published bookkeeping may belong to the turn but must still be filtered
	// before pagination, without filling pages with invisible rows.
	if _, err := s.pool.Exec(ctx, `UPDATE session_timeline_members SET turn_key='opener',unmapped_reason=NULL WHERE session_id=$1 AND source_uuid='bookkeeping'`, id); err != nil {
		t.Fatal(err)
	}
	v, err = s.TurnContext(ctx, id, "opener", a, TurnContextRequest{})
	if err != nil || v.State != "ready" || v.Revision == "" || len(v.Records) != 20 || v.NextCursor == nil {
		t.Fatalf("ready=%+v err=%v", v, err)
	}
	next, err := s.TurnContext(ctx, id, "opener", a, TurnContextRequest{Revision: v.Revision, Cursor: *v.NextCursor})
	if err != nil || next.State != "ready" || len(next.Records) != 2 || next.NextCursor != nil {
		t.Fatalf("next=%+v err=%v", next, err)
	}
	seen := map[string]bool{}
	for _, record := range append(v.Records, next.Records...) {
		if seen[record.UUID] || record.UUID == "bookkeeping" || record.UUID == "second" {
			t.Fatalf("duplicate/foreign/hidden record=%s", record.UUID)
		}
		seen[record.UUID] = true
	}
	second, err := s.TurnContext(ctx, id, "second", a, TurnContextRequest{Revision: v.Revision})
	if err != nil || len(second.Records) != 1 || second.Records[0].UUID != "second" {
		t.Fatalf("session revision across turn=%+v %v", second, err)
	}
	if _, err = s.TurnContext(ctx, id, "second", a, TurnContextRequest{Cursor: *v.NextCursor}); !errors.Is(err, ErrTurnContextRevision) {
		t.Fatalf("cross-turn cursor=%v", err)
	}
	if _, err = s.TurnContext(ctx, id, "absent", a, TurnContextRequest{}); !errors.Is(err, ErrTurnContextTurnNotFound) {
		t.Fatalf("unknown turn=%v", err)
	}
	if _, err = s.TurnContext(ctx, id, "opener", a, TurnContextRequest{Limit: 101}); !errors.Is(err, ErrTurnContextInvalid) {
		t.Fatalf("limit=%v", err)
	}
	analysis, err := s.ReadSessionAnalysis(ctx, id, a)
	if err != nil || analysis["turn_context_revision"] != v.Revision {
		t.Fatalf("analysis revision=%v err=%v", analysis["turn_context_revision"], err)
	}
	var demand, lease int
	if err := s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM session_trace_snapshots WHERE session_id=$1),(SELECT count(*) FROM session_trace_leases WHERE session_id=$1)`, id).Scan(&demand, &lease); err != nil || demand != 0 || lease != 0 {
		t.Fatalf("read created trace demand=%d lease=%d err=%v", demand, lease, err)
	}
}

func TestTurnContextFreshnessScopeAndUnavailable(t *testing.T) {
	// Serial: this test advances the shared policy epoch.
	s := acquireTestStore(t)
	id, a := seedTurnContext(t, s)
	publishTurnContext(t, s, id)
	ctx := context.Background()
	v, err := s.TurnContext(ctx, id, "opener", a, TurnContextRequest{Limit: 1})
	if err != nil || v.State != "ready" || v.NextCursor == nil {
		t.Fatalf("ready=%+v %v", v, err)
	}
	other := a
	other.DashboardUserID++
	if _, err := s.TurnContext(ctx, id, "opener", other, TurnContextRequest{Revision: v.Revision}); !errors.Is(err, ErrTurnContextRevision) {
		t.Fatalf("caller revision=%v", err)
	}
	other = a
	other.UserID = "foreign"
	if _, err := s.TurnContext(ctx, id, "opener", other, TurnContextRequest{}); !errors.Is(err, ErrSessionAnalysisNotFound) {
		t.Fatalf("foreign owner=%v", err)
	}
	other = SessionAnalysisAccess{DashboardUserID: 9, ProfileEmail: "missing"}
	if _, err := s.TurnContext(ctx, id, "opener", other, TurnContextRequest{}); !errors.Is(err, ErrSessionAnalysisNotFound) {
		t.Fatalf("foreign account=%v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{SessionID: id, Ts: time.Date(2026, 10, 1, 0, 0, 3, 0, time.UTC), UUID: "late", ParentUUID: "second", RecordType: "assistant", UserID: a.UserID, ProfileEmail: "turn-owner@example.test", Agent: "claude", SourceFile: "main.jsonl", Raw: json.RawMessage(`{"message":{"role":"assistant","content":"late"}}`)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TurnContext(ctx, id, "opener", a, TurnContextRequest{Revision: v.Revision}); !errors.Is(err, ErrTurnContextRevision) {
		t.Fatalf("source revision=%v", err)
	}
	stale, err := s.TurnContext(ctx, id, "opener", a, TurnContextRequest{})
	if err != nil || stale.State != "pending" {
		t.Fatalf("source pending=%+v %v", stale, err)
	}
	publishTurnContext(t, s, id)
	v, err = s.TurnContext(ctx, id, "opener", a, TurnContextRequest{Limit: 1})
	if err != nil || v.State != "ready" || v.NextCursor == nil {
		t.Fatalf("refreshed mapping=%+v %v", v, err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_timeline_provenance SET timeline_hash=decode(repeat('ab',32),'hex') WHERE session_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TurnContext(ctx, id, "opener", a, TurnContextRequest{Cursor: *v.NextCursor}); !errors.Is(err, ErrTurnContextRevision) {
		t.Fatalf("mapping revision=%v", err)
	}
	v, err = s.TurnContext(ctx, id, "opener", a, TurnContextRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_timeline_policy_epoch SET epoch=epoch+1 WHERE only_row`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TurnContext(ctx, id, "opener", a, TurnContextRequest{Revision: v.Revision}); !errors.Is(err, ErrTurnContextRevision) {
		t.Fatalf("policy revision=%v", err)
	}
	pending, err := s.TurnContext(ctx, id, "opener", a, TurnContextRequest{})
	if err != nil || pending.State != "pending" {
		t.Fatalf("stale mapping=%+v %v", pending, err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_analysis_facts SET coverage='truncated' WHERE session_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	unavailable, err := s.TurnContext(ctx, id, "opener", a, TurnContextRequest{})
	if err != nil || unavailable.State != "unavailable" || len(unavailable.Records) != 0 {
		t.Fatalf("truncated=%+v %v", unavailable, err)
	}
	if _, err := s.DeleteSession(ctx, id, "fixture", "test", false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TurnContext(ctx, id, "opener", a, TurnContextRequest{}); !errors.Is(err, ErrSessionAnalysisNotFound) {
		t.Fatalf("deleted=%v", err)
	}
}

func TestTurnContextEmptyUnknownAndUnlinked(t *testing.T) {
	// Serial: uses the shared store fixture and publisher.
	s := acquireTestStore(t)
	id, a := seedTurnContext(t, s)
	publishTurnContext(t, s, id)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `UPDATE session_timeline_members SET turn_key=NULL,roles='{}',unmapped_reason='missing_links' WHERE session_id=$1 AND turn_key='opener'`, id); err != nil {
		t.Fatal(err)
	}
	// A known empty turn is distinct from an unknown turn.
	empty, err := s.TurnContext(ctx, id, "opener", a, TurnContextRequest{})
	if err != nil || empty.State != "ready" || len(empty.Records) != 0 || empty.NextCursor != nil {
		t.Fatalf("empty=%+v %v", empty, err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_timeline_provenance SET timeline_state='undeterminable',timeline_reason='missing_links' WHERE session_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	unlinked, err := s.TurnContext(ctx, id, "opener", a, TurnContextRequest{})
	if err != nil || unlinked.State != "unavailable" || len(unlinked.Records) != 0 {
		t.Fatalf("unlinked=%+v %v", unlinked, err)
	}
	if _, err := s.TurnContext(ctx, id, "unknown", a, TurnContextRequest{}); !errors.Is(err, ErrTurnContextTurnNotFound) {
		t.Fatalf("unknown=%v", err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_timeline_provenance SET timeline_state='prepared',timeline_reason='',timeline_hash=NULL,fact_output_hash=NULL,source_generation=NULL,rules_version=NULL WHERE session_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	pending, err := s.TurnContext(ctx, id, "opener", a, TurnContextRequest{})
	if err != nil || pending.State != "pending" {
		t.Fatalf("prepared=%+v %v", pending, err)
	}
}
