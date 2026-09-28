package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestPgStore_InsertSessionRecords_classifiesOnlyHumanPrompts(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	records := []*SessionRecord{
		{Ts: ts, SessionID: "task-label", UUID: "prompt", RecordType: "user", ProfileEmail: "user@example.com", Raw: json.RawMessage(`{"message":{"role":"user","content":"implement the endpoint"}}`)},
		{Ts: ts.Add(time.Second), SessionID: "task-label", UUID: "tool", RecordType: "user", ProfileEmail: "user@example.com", Raw: json.RawMessage(`{"message":{"role":"user","content":[{"type":"tool_result","content":"failed"}]}}`)},
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if records[0].TaskType != "implementation" || records[1].TaskType != "" {
		t.Fatalf("record labels = %q, %q", records[0].TaskType, records[1].TaskType)
	}
	var got string
	if err := s.pool.QueryRow(ctx, `SELECT task_type FROM session_records WHERE uuid = 'prompt'`).Scan(&got); err != nil {
		t.Fatalf("query task_type: %v", err)
	}
	if got != "implementation" {
		t.Fatalf("stored task_type = %q, want implementation", got)
	}
	listed, err := s.ListSessionRecords(ctx, SessionRecordFilter{SessionID: "task-label"})
	if err != nil {
		t.Fatalf("ListSessionRecords: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("listed count = %d", len(listed))
	}
	for _, record := range listed {
		if record.UUID == "prompt" && record.TaskType != "implementation" {
			t.Fatalf("listed task_type = %q, want implementation", record.TaskType)
		}
	}
}

func TestPgStore_InsertSessionRecords_preservesCctraceVersion(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{
			Ts:             ts,
			SessionID:      "cctrace-version-s1",
			RecordType:     "assistant",
			ProfileEmail:   "user@example.com",
			UserID:         "u1",
			Agent:          "claude",
			CctraceVersion: "v0.7.8",
			Raw:            json.RawMessage(`{"uuid":"turn-1"}`),
			UUID:           "turn-1",
		},
		{
			Ts:             ts,
			SessionID:      "cctrace-version-s1",
			RecordType:     "assistant",
			ProfileEmail:   "user@example.com",
			UserID:         "u1",
			Agent:          "claude",
			CctraceVersion: "",
			Raw:            json.RawMessage(`{"uuid":"turn-2"}`),
			UUID:           "turn-2",
		},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	rows, err := s.pool.Query(ctx, `SELECT uuid, cctrace_version FROM session_records WHERE session_id = $1 ORDER BY uuid`, "cctrace-version-s1")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	got := map[string]string{}
	for rows.Next() {
		var uuid, version string
		if err := rows.Scan(&uuid, &version); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[uuid] = version
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if got["turn-1"] != "v0.7.8" {
		t.Fatalf("turn-1 cctrace_version = %q, want v0.7.8", got["turn-1"])
	}
	if got["turn-2"] != "" {
		t.Fatalf("turn-2 cctrace_version = %q, want empty", got["turn-2"])
	}
}

// TestPgStore_ReenrichSessionRecords_preservesCctraceVersion guards the decision that
// ReenrichSessionRecords must never write cctrace_version. Re-enrichment happens when a
// later-version client re-reads old JSONL; the enriching client's version is not the
// collecting client's version, and an empty cctrace_version is a confirmed fact ("older
// than CctraceVersionSince"), not "unknown" — overwriting it with the reenrich client's
// version would turn a confirmed fact into a lie.
func TestPgStore_ReenrichSessionRecords_preservesCctraceVersion(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 7, 6, 13, 0, 0, 0, time.UTC)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{
			Ts:             ts,
			SessionID:      "reenrich-cctrace-s1",
			RecordType:     "assistant",
			ProfileEmail:   "user@example.com",
			UserID:         "u1",
			Agent:          "claude",
			CctraceVersion: "",
			Raw:            json.RawMessage(`{"uuid":"turn-1"}`),
			UUID:           "turn-1",
		},
		{
			Ts:             ts,
			SessionID:      "reenrich-cctrace-s1",
			RecordType:     "assistant",
			ProfileEmail:   "user@example.com",
			UserID:         "u1",
			Agent:          "claude",
			CctraceVersion: "v0.7.8",
			Raw:            json.RawMessage(`{"uuid":"turn-2"}`),
			UUID:           "turn-2",
		},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	reenricher := requireSessionRecordReenricher(t, s)
	updated, err := reenricher.ReenrichSessionRecords(ctx, []*SessionRecord{
		{
			Ts:             ts,
			SessionID:      "reenrich-cctrace-s1",
			RecordType:     "assistant",
			ProfileEmail:   "user@example.com",
			UUID:           "turn-1",
			SourceFile:     "session.jsonl",
			CctraceVersion: "v0.9.0",
		},
		{
			Ts:             ts,
			SessionID:      "reenrich-cctrace-s1",
			RecordType:     "assistant",
			ProfileEmail:   "user@example.com",
			UUID:           "turn-2",
			SourceFile:     "session.jsonl",
			CctraceVersion: "v0.9.0",
		},
	})
	if err != nil {
		t.Fatalf("ReenrichSessionRecords: %v", err)
	}
	if updated != 2 {
		t.Fatalf("updated rows = %d, want 2", updated)
	}

	rows, err := s.pool.Query(ctx, `SELECT uuid, cctrace_version FROM session_records WHERE session_id = $1 ORDER BY uuid`, "reenrich-cctrace-s1")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	got := map[string]string{}
	for rows.Next() {
		var uuid, version string
		if err := rows.Scan(&uuid, &version); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[uuid] = version
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if got["turn-1"] != "" {
		t.Fatalf("turn-1 cctrace_version = %q, want empty (reenrich must not overwrite)", got["turn-1"])
	}
	if got["turn-2"] != "v0.7.8" {
		t.Fatalf("turn-2 cctrace_version = %q, want v0.7.8 (reenrich must not overwrite)", got["turn-2"])
	}
}

func TestPgStore_ListSessionRecords_returnsCctraceVersion(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 7, 6, 14, 0, 0, 0, time.UTC)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
		Ts:             ts,
		SessionID:      "list-cctrace-s1",
		RecordType:     "assistant",
		ProfileEmail:   "user@example.com",
		UUID:           "turn-1",
		CctraceVersion: "v0.7.8",
		Raw:            json.RawMessage(`{}`),
	}}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	recs, err := s.ListSessionRecords(ctx, SessionRecordFilter{SessionID: "list-cctrace-s1"})
	if err != nil {
		t.Fatalf("ListSessionRecords: %v", err)
	}
	if len(recs) != 1 || recs[0].CctraceVersion != "v0.7.8" {
		t.Fatalf("unexpected records: %+v", recs)
	}
}

func TestPgStore_SessionRecords_roundTripToolIdentity(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 9, 17, 9, 0, 0, 0, time.UTC)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "tool-identity", RecordType: "tool_call", ProfileEmail: "user@example.com", UUID: "call", Agent: "codex", ToolName: "shell", ToolCallID: "call_1", Raw: json.RawMessage(`{}`)},
		{Ts: ts.Add(time.Second), SessionID: "tool-identity", RecordType: "assistant", ProfileEmail: "user@example.com", UUID: "plain", Raw: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	check := func(name string, recs []*SessionRecord) {
		t.Helper()
		got := map[string]*SessionRecord{}
		for _, r := range recs {
			got[r.UUID] = r
		}
		call, plain := got["call"], got["plain"]
		if call == nil || plain == nil {
			t.Fatalf("%s: missing records: %+v", name, recs)
		}
		if call.Agent != "codex" || call.ToolName != "shell" || call.ToolCallID != "call_1" {
			t.Fatalf("%s: tool record = agent %q tool_name %q tool_call_id %q", name, call.Agent, call.ToolName, call.ToolCallID)
		}
		if plain.Agent != "claude" || plain.ToolName != "" || plain.ToolCallID != "" {
			t.Fatalf("%s: plain record = agent %q tool_name %q tool_call_id %q", name, plain.Agent, plain.ToolName, plain.ToolCallID)
		}
	}

	recs, err := s.ListSessionRecords(ctx, SessionRecordFilter{SessionID: "tool-identity"})
	if err != nil {
		t.Fatalf("ListSessionRecords: %v", err)
	}
	check("ListSessionRecords", recs)
	recs, err = s.ListSessionLineage(ctx, SessionRecordFilter{SessionID: "tool-identity", ProfileEmail: "user@example.com"})
	if err != nil {
		t.Fatalf("ListSessionLineage: %v", err)
	}
	check("ListSessionLineage", recs)
}

// Postgres TEXT rejects NUL, and one rejected row fails the whole batch the client
// keeps resending. Raw is already scrubbed; the tool identity columns come from the
// same log lines and must be too.
func TestPgStore_SessionRecords_toolIdentityWithNULDoesNotFailBatch(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 9, 17, 9, 0, 0, 0, time.UTC)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "tool-nul", RecordType: "tool_call", ProfileEmail: "user@example.com", UUID: "call", Agent: "codex", ToolName: "sh\x00ell", ToolCallID: "call\x001", Raw: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	recs, err := s.ListSessionRecords(ctx, SessionRecordFilter{SessionID: "tool-nul"})
	if err != nil {
		t.Fatalf("ListSessionRecords: %v", err)
	}
	if len(recs) != 1 || recs[0].ToolName != "sh�ell" || recs[0].ToolCallID != "call�1" {
		t.Fatalf("records = %+v", recs)
	}
}

// Reenrich must never write tool identity. Codex rows carry uuid=” and are
// matched on (session_id, ts, record_type, profile_email) alone, so parallel
// calls that share a millisecond can match each other's row when the re-read's
// conflict-nudged ts differs from the original insert. Fill-only would then make
// a swapped call_id permanent; identity comes from the original insert only.
func TestPgStore_ReenrichSessionRecords_leavesToolIdentityUntouched(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	base := SessionRecord{Ts: ts, SessionID: "tool-reenrich", RecordType: "tool_call", ProfileEmail: "user@example.com", Agent: "codex", Raw: json.RawMessage(`{}`)}
	first := base
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{&first}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	enriched := base
	enriched.Entrypoint = "cli"
	enriched.ToolName, enriched.ToolCallID = "shell", "call_other"
	updated, err := s.ReenrichSessionRecords(ctx, []*SessionRecord{&enriched})
	if err != nil {
		t.Fatalf("ReenrichSessionRecords: %v", err)
	}
	if updated != 1 {
		t.Fatalf("reenrich updated %d rows, want 1 (the row must match for this test to mean anything)", updated)
	}
	var name, id, entrypoint string
	if err := s.pool.QueryRow(ctx, `SELECT tool_name, tool_call_id, entrypoint FROM session_records WHERE session_id = 'tool-reenrich'`).Scan(&name, &id, &entrypoint); err != nil {
		t.Fatalf("query: %v", err)
	}
	if entrypoint != "cli" {
		t.Fatalf("entrypoint = %q, want cli (reenrich did not apply)", entrypoint)
	}
	if name != "" || id != "" {
		t.Fatalf("tool identity = %q, %q; want both empty", name, id)
	}
}

func TestPgStore_ListSessionLineage_returnsDifferentCctraceVersionsAcrossForkChain(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	records := []*SessionRecord{
		{Ts: now, SessionID: "lineage-root", ProfileEmail: "user@example.com", UUID: "root-1", CctraceVersion: "v0.7.4", Raw: []byte(`{}`)},
		{Ts: now.Add(time.Minute), SessionID: "lineage-branch", ProfileEmail: "user@example.com", UUID: "branch-1", ForkedFromSession: "lineage-root", CctraceVersion: "v0.7.8", Raw: []byte(`{}`)},
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	recs, err := s.ListSessionLineage(ctx, SessionRecordFilter{SessionID: "lineage-branch", ProfileEmail: "user@example.com"})
	if err != nil {
		t.Fatalf("ListSessionLineage: %v", err)
	}
	got := map[string]string{}
	for _, r := range recs {
		got[r.SessionID] = r.CctraceVersion
	}
	if got["lineage-root"] != "v0.7.4" || got["lineage-branch"] != "v0.7.8" {
		t.Fatalf("unexpected lineage versions: %+v", got)
	}
}

func TestPgStore_ListSessionLineage_appliesOffsetAcrossPages(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

	records := []*SessionRecord{
		{Ts: now, SessionID: "paged-lineage-root", ProfileEmail: "user@example.com", UUID: "turn-1", Raw: []byte(`{}`)},
		{Ts: now.Add(time.Minute), SessionID: "paged-lineage-root", ProfileEmail: "user@example.com", UUID: "turn-2", Raw: []byte(`{}`)},
		{Ts: now.Add(2 * time.Minute), SessionID: "paged-lineage-root", ProfileEmail: "user@example.com", UUID: "turn-3", Raw: []byte(`{}`)},
		{Ts: now.Add(3 * time.Minute), SessionID: "paged-lineage-root", ProfileEmail: "user@example.com", UUID: "turn-4", Raw: []byte(`{}`)},
		{Ts: now.Add(4 * time.Minute), SessionID: "paged-lineage-branch", ProfileEmail: "user@example.com", UUID: "turn-5", ForkedFromSession: "paged-lineage-root", Raw: []byte(`{}`)},
		{Ts: now.Add(5 * time.Minute), SessionID: "paged-lineage-branch", ProfileEmail: "user@example.com", UUID: "turn-6", Raw: []byte(`{}`)},
		{Ts: now.Add(6 * time.Minute), SessionID: "paged-lineage-branch", ProfileEmail: "user@example.com", UUID: "turn-7", Raw: []byte(`{}`)},
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	wantPages := [][]string{
		{"turn-1", "turn-2", "turn-3"},
		{"turn-4", "turn-5", "turn-6"},
		{"turn-7"},
	}
	for page, want := range wantPages {
		recs, err := s.ListSessionLineage(ctx, SessionRecordFilter{
			SessionID: "paged-lineage-branch", ProfileEmail: "user@example.com", Limit: 3, Offset: page * 3,
		})
		if err != nil {
			t.Fatalf("ListSessionLineage page %d: %v", page, err)
		}
		got := make([]string, len(recs))
		for i, rec := range recs {
			got[i] = rec.UUID
		}
		if len(got) != len(want) {
			t.Fatalf("page %d UUIDs = %v, want %v", page, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("page %d UUIDs = %v, want %v", page, got, want)
			}
		}
	}
}

func TestPgStore_ListSessionOverviews_aggregatesCctraceAndClaudeVersion(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
		Ts:             now,
		SessionID:      "overview-version-s1",
		ProfileEmail:   "user@example.com",
		UUID:           "turn-1",
		CctraceVersion: "v0.7.8",
		Raw:            []byte(`{}`),
	}}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.InsertEvents(ctx, []*OtelEvent{{
		Ts:              now.Add(time.Minute),
		EventName:       "api_request",
		SessionID:       "overview-version-s1",
		ProfileEmail:    "user@example.com",
		Agent:           "claude",
		BillingProvider: "anthropic",
		ServiceVersion:  "1.2.3",
	}}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	overviews, err := s.ListSessionOverviews(ctx, SessionOverviewFilter{ProfileEmail: "user@example.com"})
	if err != nil {
		t.Fatalf("ListSessionOverviews: %v", err)
	}
	if len(overviews) != 1 {
		t.Fatalf("expected 1 overview, got %d: %+v", len(overviews), overviews)
	}
	if overviews[0].CctraceVersion != "v0.7.8" {
		t.Fatalf("CctraceVersion = %q, want v0.7.8", overviews[0].CctraceVersion)
	}
	if overviews[0].ClaudeVersion != "1.2.3" {
		t.Fatalf("ClaudeVersion = %q, want 1.2.3", overviews[0].ClaudeVersion)
	}
}
