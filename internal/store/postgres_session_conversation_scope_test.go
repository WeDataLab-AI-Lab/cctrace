package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestSessionConversationAccountScopeAndLineage(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	records := []*SessionRecord{}
	for _, session := range []string{"scope-parent", "scope-child"} {
		for i, login := range []string{"a@example.test", "b@example.test"} {
			r := &SessionRecord{SessionID: session, Ts: base.Add(time.Duration(i) * time.Second), UUID: session + login, RecordType: "user", UserID: "owner", ProfileEmail: "profile@example.test", LoginEmail: login, Agent: "claude", Raw: json.RawMessage(`{"message":{"content":"synthetic"}}`)}
			if session == "scope-child" {
				r.ForkedFromSession = "scope-parent"
			}
			records = append(records, r)
		}
		records = append(records, &SessionRecord{SessionID: session, Ts: base.Add(2 * time.Second), UUID: session + "foreign", RecordType: "user", UserID: "other", ProfileEmail: "other@example.test", LoginEmail: "a@example.test", Agent: "claude", Raw: json.RawMessage(`{"message":{"content":"foreign"}}`)})
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	f := SessionRecordFilter{SessionID: "scope-child", UserID: "owner", ProfileEmail: "profile@example.test", LoginEmail: "a@example.test", Order: "asc", Limit: 100}
	rows, err := s.ListSessionRecords(ctx, f)
	if err != nil || len(rows) != 1 || rows[0].UUID != "scope-childa@example.test" {
		t.Fatalf("session scoped rows=%d err=%v", len(rows), err)
	}
	rows, err = s.ListSessionLineage(ctx, f)
	if err != nil || len(rows) != 2 {
		t.Fatalf("lineage scoped rows=%d err=%v", len(rows), err)
	}
	for _, r := range rows {
		if r.UUID != "scope-childa@example.test" && r.UUID != "scope-parenta@example.test" {
			t.Fatalf("crossed lineage scope: %s", r.UUID)
		}
	}
	f.LoginEmail = ""
	rows, err = s.ListSessionRecords(ctx, f)
	if err != nil || len(rows) != 2 {
		t.Fatalf("default profile/owner rows=%d err=%v", len(rows), err)
	}
	f.ProfileEmail = "wrong@example.test"
	rows, err = s.ListSessionRecords(ctx, f)
	if err != nil || len(rows) != 0 {
		t.Fatalf("combined scope escaped profile=%d %v", len(rows), err)
	}
}
