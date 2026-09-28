package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestAISegmentsScopeVisibilityAndBoundaries(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	sc := AISegmentScope{UserID: "owner", Since: base, Until: base.AddDate(0, 0, 7)}
	records := []*SessionRecord{}
	for i, ts := range []time.Time{base.Add(-time.Second), base, base.Add(time.Hour), sc.Until} {
		records = append(records, &SessionRecord{Ts: ts, SessionID: fmt.Sprintf("s%d", i), UUID: fmt.Sprintf("u%d", i), RecordType: "user", Agent: "claude", UserID: "owner", ProfileEmail: "owner@example.com", Raw: userTurn("write a test")})
	}
	records = append(records, &SessionRecord{Ts: base, SessionID: "other", UUID: "other", RecordType: "user", Agent: "claude", UserID: "other", Raw: userTurn("private")})
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	// Pin the conversation end independently of the fact builder's idle-gap policy.
	if _, err := s.pool.Exec(ctx, `UPDATE task_segment_facts SET end_ts=$1, tool_fail_count=2, had_compact=true WHERE session_id='s1'`, base.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records (ts,session_id,uuid,record_type,user_id,raw) VALUES ($1,'s1','inside','assistant','owner','{"message":{"content":"inside"}}'),($2,'s1','end','assistant','owner','{"message":{"content":"outside"}}')`, base.Add(time.Minute), base.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	all, err := s.AIWeekSegments(ctx, sc, AISegmentFilter{})
	if err != nil || len(all) != 2 {
		t.Fatalf("week=%+v err=%v", all, err)
	}
	var id int64
	for _, v := range all {
		if v.SessionID == "s1" {
			id = v.ID
		}
	}
	if id == 0 {
		t.Fatal("since boundary missing")
	}
	agg, err := s.AIWeekAggregate(ctx, sc)
	if err != nil || agg.SegmentCount != 2 || agg.SessionCount != 2 || agg.TypedTurnCount != 2 || agg.ToolFailCount != 2 || agg.CompactedSegmentCount != 1 {
		t.Fatalf("aggregate=%+v err=%v", agg, err)
	}
	selected, err := s.AISegmentsByIDs(ctx, sc, []int64{id})
	if err != nil || len(selected) != 1 {
		t.Fatalf("IDs=%+v %v", selected, err)
	}
	conv, err := s.AISegmentConversation(ctx, sc, id, 0, 50)
	if err != nil || len(conv) != 2 || conv[0].UUID != "u1" || conv[1].UUID != "inside" {
		t.Fatalf("conversation=%+v err=%v", conv, err)
	}
	page, err := s.AISegmentConversation(ctx, sc, id, 1, 1)
	if err != nil || len(page) != 1 || page[0].UUID != "inside" {
		t.Fatalf("page=%+v %v", page, err)
	}
	yes := true
	filtered, err := s.AIWeekSegments(ctx, sc, AISegmentFilter{HadCompact: &yes, MinToolFail: 2, MinTypedTurns: 1, Agent: "claude", OrderBy: AISegmentOrderToolFailCount, Limit: 1})
	if err != nil || len(filtered) != 1 || filtered[0].ID != id {
		t.Fatalf("filter=%+v %v", filtered, err)
	}
	for _, filter := range []AISegmentFilter{{MinToolFail: 3}, {MinTypedTurns: 2}, {Agent: "codex"}} {
		got, err := s.AIWeekSegments(ctx, sc, filter)
		if err != nil || len(got) != 0 {
			t.Fatalf("filter=%+v got=%+v %v", filter, got, err)
		}
	}
	for _, scope := range []AISegmentScope{{UserID: "other", Since: sc.Since, Until: sc.Until}, {Since: sc.Since, Until: sc.Until}, {UserID: "owner", Since: sc.Until, Until: sc.Until.AddDate(0, 0, 7)}} {
		got, err := s.AISegmentsByIDs(ctx, scope, []int64{id})
		if err != nil || len(got) != 0 {
			t.Fatalf("scope IDs=%+v %v", got, err)
		}
		gotConv, err := s.AISegmentConversation(ctx, scope, id, 0, 50)
		if err != nil || len(gotConv) != 0 {
			t.Fatalf("scope conversation=%+v %v", gotConv, err)
		}
	}
	emailScope := sc
	emailScope.UserID = "wrong"
	emailScope.ProfileEmail = "owner@example.com"
	got, err := s.AISegmentsByIDs(ctx, emailScope, []int64{id})
	if err != nil || len(got) != 1 {
		t.Fatalf("email OR scope=%+v %v", got, err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO excluded_sessions(session_id) VALUES ('s1')`); err != nil {
		t.Fatal(err)
	}
	got, err = s.AISegmentsByIDs(ctx, sc, []int64{id})
	if err != nil || len(got) != 0 {
		t.Fatalf("excluded IDs=%+v %v", got, err)
	}
	conv, err = s.AISegmentConversation(ctx, sc, id, 0, 50)
	if err != nil || len(conv) != 0 {
		t.Fatalf("excluded conversation=%+v %v", conv, err)
	}
	all, err = s.AIWeekSegments(ctx, sc, AISegmentFilter{})
	if err != nil || len(all) != 1 {
		t.Fatalf("excluded list=%+v %v", all, err)
	}
	agg, err = s.AIWeekAggregate(ctx, sc)
	if err != nil || agg.SegmentCount != 1 {
		t.Fatalf("excluded aggregate=%+v %v", agg, err)
	}
}
