package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestTraceRootResolvedDispatchesKeepUnprovenRoots(t *testing.T) {
	// The store fixture is shared, so all scopes and cap cases remain sequential.
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	at := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	var records []*SessionRecord
	add := func(session, uuid, call, account, login, kind string, parent bool) {
		raw := `{"message":{"content":"Observed child"}}`
		if parent {
			body, _ := json.Marshal(map[string]any{"message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": call, "name": "Agent"}}}})
			raw = string(body)
		}
		rec := &SessionRecord{SessionID: session, UUID: uuid, RecordType: kind, Ts: at.Add(time.Duration(len(records)) * time.Millisecond), Agent: "claude", ProfileEmail: "p", UserID: "owner", AccountID: account, LoginEmail: login, SourceFile: "child", IsSidechain: true, ToolUseID: call, Raw: json.RawMessage(raw)}
		if parent {
			rec.SourceFile, rec.ToolUseID = "main", ""
		}
		records = append(records, rec)
	}
	const session = "root-dispatch-filter"
	for _, call := range []string{"known", "missing", "ambiguous", "wrong-account", "wrong-login", "excluded-session"} {
		add(session, "child-"+call, call, "acct", "login", "system", false)
	}
	add(session, "known-parent", "known", "acct", "login", "assistant", true)
	// One row matches both typed and native identity branches, but is one parent.
	records[len(records)-1].RecordType, records[len(records)-1].ToolCallID = "tool_call", "known"
	add(session, "ambiguous-a", "ambiguous", "acct", "login", "assistant", true)
	add(session, "ambiguous-b", "ambiguous", "acct", "login", "assistant", true)
	records[len(records)-1].SourceFile = "copy"
	add(session, "foreign-account-parent", "wrong-account", "other", "login", "assistant", true)
	add(session, "foreign-login-parent", "wrong-login", "acct", "other-login", "assistant", true)
	add("other-session", "foreign-session-parent", "excluded-session", "acct", "login", "assistant", true)
	add(session, "tool-output", "known", "acct", "login", "tool_output", false)
	add(session, "codex-child", "known", "acct", "login", "system", false)
	records[len(records)-1].Agent = "codex"
	for i := 0; i < traceRootDispatchRefLimit+1; i++ {
		add("ref-overflow", fmt.Sprint("child-", i), fmt.Sprint("call-", i), "acct", "login", "system", false)
	}
	add("ref-overflow", "parent", "call-0", "acct", "login", "assistant", true)
	for i := 0; i < traceRootDispatchRefLimit; i++ {
		add("ref-boundary", fmt.Sprint("child-", i), fmt.Sprint("call-", i), "acct", "login", "system", false)
	}
	add("ref-boundary", "parent", "call-0", "acct", "login", "assistant", true)
	add("parent-overflow", "child", "crowded", "acct", "login", "system", false)
	for i := 0; i < 501; i++ {
		add("parent-overflow", fmt.Sprint("parent-", i), "crowded", "acct", "login", "assistant", true)
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	excluded := false
	read := func(sid string, access SessionAnalysisAccess) []byte {
		t.Helper()
		tx, cleanup, err := s.turnContextTx(ctx, sid)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		r := traceNodeReader{ctx: ctx, tx: tx, session: sid, access: access, agentParents: map[string]*traceNodeKey{}}
		resolved, err := r.rootResolvedDispatches()
		if err != nil {
			t.Fatal(err)
		}
		if sid == session && !excluded {
			rows, err := tx.Query(ctx, `WITH `+traceRootResolvedDispatchCTE("$2")+` kept AS (SELECT r.uuid FROM session_records r WHERE r.session_id=$1 AND r.tool_use_id<>'' AND `+traceRootDispatchFilterSQL("r")+`) SELECT uuid FROM kept ORDER BY uuid`, sid, resolved)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var kept []string
			for rows.Next() {
				var uuid string
				if err := rows.Scan(&uuid); err != nil {
					t.Fatal(err)
				}
				kept = append(kept, uuid)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			want := []string{"child-ambiguous", "child-excluded-session", "child-missing", "child-wrong-account", "child-wrong-login", "codex-child", "tool-output"}
			if access.LoginEmail == "" {
				want = []string{"child-ambiguous", "child-excluded-session", "child-missing", "child-wrong-account", "codex-child", "tool-output"}
			}
			if !reflect.DeepEqual(kept, want) {
				t.Fatalf("unproven root keys changed: got %v want %v", kept, want)
			}
		}
		return resolved
	}
	access := SessionAnalysisAccess{UserID: "owner", ProfileEmail: "p", LoginEmail: "login"}
	read(session, access)
	access.LoginEmail = ""
	read(session, access)
	var boundary []traceRootDispatchRef
	if err := json.Unmarshal(read("ref-boundary", access), &boundary); err != nil || len(boundary) != 1 || boundary[0].CallID != "call-0" {
		t.Fatalf("128 references lost the proven parent: resolved=%v err=%v", boundary, err)
	}
	for _, sid := range []string{"ref-overflow", "parent-overflow"} {
		if got := string(read(sid, access)); got != "[]" {
			t.Fatalf("%s: incomplete relationship set filtered roots: %s", sid, got)
		}
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO excluded_sessions(session_id) VALUES($1)`, session); err != nil {
		t.Fatal(err)
	}
	excluded = true
	if got := string(read(session, access)); got != "[]" {
		t.Fatalf("excluded parent was treated as visible: %s", got)
	}
}

func TestTraceRootDispatchFilterPrecedesCandidateLimit(t *testing.T) {
	// The shared database fixture is used sequentially.
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	at := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	var records []*SessionRecord
	add := func(uuid, kind, source, call string, offset int, raw string) {
		records = append(records, &SessionRecord{SessionID: "root-filter-limit", UUID: uuid, RecordType: kind, Agent: "claude", UserID: "owner", ProfileEmail: "p", SourceFile: source, ToolUseID: call, Ts: at.Add(time.Duration(offset) * time.Second), Raw: json.RawMessage(raw)})
	}
	for i := 0; i < 50; i++ {
		add(fmt.Sprint("child-", i), "system", "child", "known", i, `{"content":"Recorded child"}`)
	}
	add("unresolved", "system", "child", "unknown", 50, `{"content":"Keep this unproven root"}`)
	// Canonical inputs stay in their union even when a dispatch reference exists.
	add("canonical", "user", "main", "known", 100, `{"message":{"content":"Actual user input"}}`)
	add("parent", "assistant", "main", "", 101, `{"message":{"content":[{"type":"tool_use","id":"known","name":"Agent"}]}}`)
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	tx, cleanup, err := s.turnContextTx(ctx, "root-filter-limit")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	access := SessionAnalysisAccess{UserID: "owner", ProfileEmail: "p"}
	if err = authorizeTrace(ctx, tx, "root-filter-limit", access); err != nil {
		t.Fatal(err)
	}
	r := traceNodeReader{ctx: ctx, tx: tx, session: "root-filter-limit", access: access, rootPage: true, flow: true, metadata: true, agentParents: map[string]*traceNodeKey{}}
	rows, err := r.rows("TRUE", nil, 22)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, row := range rows {
		got = append(got, row.record.UUID)
	}
	if want := []string{"unresolved", "canonical"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("proven children consumed the raw candidate limit: got %v want %v", got, want)
	}
}
