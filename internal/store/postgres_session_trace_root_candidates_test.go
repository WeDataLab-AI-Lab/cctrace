package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestTraceFlowRootCandidatesBoundAcrossSources(t *testing.T) {
	// The shared fixture is truncated, so these database tests cannot run in parallel.
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	at := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	var records []*SessionRecord
	for source := 0; source < 12; source++ {
		for row := 0; row < 6; row++ {
			records = append(records, &SessionRecord{SessionID: "root-bound", UUID: fmt.Sprintf("%02d-%02d", row, source), RecordType: "system", Ts: at.Add(time.Duration(row) * time.Second), Agent: "claude", UserID: "owner", ProfileEmail: "p", SourceFile: fmt.Sprint(source), Raw: json.RawMessage(`{}`)})
		}
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	query := "WITH " + traceInputSetCTE() + traceRootCandidatesCTE(4, false) + `selected AS (SELECT uuid FROM trace_root_candidates ORDER BY ts,uuid,id,source_order) SELECT uuid FROM selected`
	rows, err := s.pool.Query(ctx, query, "root-bound", "owner", "p", "")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var uuid string
		if err := rows.Scan(&uuid); err != nil {
			t.Fatal(err)
		}
		got = append(got, uuid)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"00-00", "00-01", "00-02", "00-03"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("orphan candidates must be bounded globally: got %v want %v", got, want)
	}
}

func TestTraceFlowRootPagingKeepsNativeAndSourceMembership(t *testing.T) {
	// This test owns the shared database contents until all cursor reads finish.
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	at := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	var records []*SessionRecord
	add := func(uuid, agent, file, kind, turn string, offset int) {
		raw, _ := json.Marshal(map[string]any{"payload": map[string]any{"turn_id": turn, "role": "user", "content": uuid}})
		records = append(records, &SessionRecord{SessionID: "root-membership", UUID: uuid, RecordType: kind, Ts: at.Add(time.Duration(offset) * time.Second), Agent: agent, UserID: "owner", ProfileEmail: "p", LoginEmail: "allowed", AccountID: "acct", SourceFile: file, Raw: raw})
	}
	add("claude-before", "claude", "main", "system", "", 0)
	add("codex-matched-before", "codex", "main", "system", "known", 1)
	add("codex-unmatched-before", "codex", "main", "system", "unknown", 2)
	add("orphan-a", "claude", "no-input-a", "system", "", 3)
	add("orphan-b", "codex", "no-input-b", "system", "known", 3)
	add("account-copy", "claude", "main", "system", "", 4)
	records[len(records)-1].AccountID = "other"
	add("claude-input", "claude", "main", "user", "", 10)
	add("codex-input", "codex", "main", "user", "known", 11)
	add("codex-followup", "codex", "main", "user", "known", 12)
	add("claude-after", "claude", "main", "system", "", 20)
	add("codex-matched-after", "codex", "main", "system", "known", 21)
	add("codex-unmatched-after", "codex", "main", "system", "unknown", 22)
	add("foreign-login", "claude", "foreign", "system", "", 0)
	records[len(records)-1].LoginEmail = "foreign"
	add("foreign-profile", "claude", "foreign", "system", "", 0)
	records[len(records)-1].ProfileEmail = "foreign"
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	a := SessionAnalysisAccess{UserID: "owner", ProfileEmail: "p", LoginEmail: "allowed"}
	read := func(cursor string) TraceNodesResponse {
		t.Helper()
		page, err := s.TraceNodes(ctx, "root-membership", a, TraceNodesRequest{View: "flow", Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	var pages []TraceNodesResponse
	var got []string
	for cursor := ""; ; {
		page := read(cursor)
		pages = append(pages, page)
		for _, node := range page.Nodes {
			key, err := decodeNodeKey(node.NodeID)
			if err != nil {
				t.Fatal(err)
			}
			var uuid string
			if err := s.pool.QueryRow(ctx, `SELECT uuid FROM session_records WHERE session_id='root-membership' AND ts=$1 AND id=$2`, key.TS, key.ID).Scan(&uuid); err != nil {
				t.Fatal(err)
			}
			got = append(got, uuid)
		}
		if page.NextCursor == nil {
			break
		}
		if len(pages) > len(records) {
			t.Fatal("root pagination did not terminate")
		}
		cursor = *page.NextCursor
	}
	want := []string{"claude-before", "codex-unmatched-before", "orphan-a", "orphan-b", "account-copy", "claude-input", "codex-input", "codex-unmatched-after"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("source/native root membership: got %v want %v", got, want)
	}
	for i := len(pages) - 1; i > 0; i-- {
		if pages[i].PrevCursor == nil {
			t.Fatalf("page %d has no previous cursor", i)
		}
		back := read(*pages[i].PrevCursor)
		if !reflect.DeepEqual(back.Nodes, pages[i-1].Nodes) {
			t.Fatalf("backward page %d differs: got %+v want %+v", i, back.Nodes, pages[i-1].Nodes)
		}
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO blocked_projects(project_hash) VALUES('root-blocked'); UPDATE session_records SET project_hash='root-blocked' WHERE session_id='root-membership' AND uuid='foreign-login'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM blocked_projects WHERE project_hash='root-blocked'`)
	})
	if _, err := s.TraceNodes(ctx, "root-membership", a, TraceNodesRequest{View: "flow"}); err != ErrSessionAnalysisNotFound {
		t.Fatalf("filtered source must still participate in complete session authorization: %v", err)
	}
}
