package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type sessionRecordReenricher interface {
	ReenrichSessionRecords(ctx context.Context, records []*SessionRecord) (int, error)
}

func requireSessionRecordReenricher(t *testing.T, s *PgStore) sessionRecordReenricher {
	t.Helper()
	reenricher, ok := any(s).(sessionRecordReenricher)
	if !ok {
		t.Fatal("PgStore must implement ReenrichSessionRecords for #104")
	}
	return reenricher
}

func TestPgStore_ReenrichSessionRecords_updatesLegacyRowWithoutDuplicate(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 7, 6, 10, 0, 0, 0, time.UTC)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
		Ts:              ts,
		SessionID:       "legacy-claude-s1",
		RecordType:      "assistant",
		ProfileEmail:    "user@example.com",
		UserID:          "u1",
		Model:           "claude-sonnet-4-6",
		InputTokens:     intPtr(10),
		OutputTokens:    intPtr(3),
		Agent:           "claude",
		BillingProvider: "anthropic",
		Raw:             json.RawMessage(`{"type":"assistant","uuid":"turn-1"}`),
	}}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	reenricher := requireSessionRecordReenricher(t, s)
	updated, err := reenricher.ReenrichSessionRecords(ctx, []*SessionRecord{{
		Ts:                ts,
		SessionID:         "legacy-claude-s1",
		RecordType:        "assistant",
		ProfileEmail:      "user@example.com",
		UserID:            "u1",
		Model:             "claude-sonnet-4-6",
		InputTokens:       intPtr(999),
		OutputTokens:      intPtr(999),
		UUID:              "turn-1",
		ParentUUID:        "root-1",
		IsSidechain:       true,
		AgentID:           "agent-1",
		ForkedFromSession: "origin-s1",
		ForkedFromUUID:    "origin-turn-1",
		SourceFile:        "session.jsonl",
		ToolUseID:         "toolu_1",
		IsCompactSummary:  true,
		IsMeta:            true,
		PromptSource:      "typed",
		Entrypoint:        "cli",
		Agent:             "claude",
		BillingProvider:   "anthropic",
	}})
	if err != nil {
		t.Fatalf("ReenrichSessionRecords: %v", err)
	}
	if updated != 1 {
		t.Fatalf("updated rows = %d, want 1", updated)
	}

	var count int
	var uuid, parentUUID, agentID, forkedFromSession, forkedFromUUID, sourceFile, toolUseID, promptSource, entrypoint string
	var isSidechain, isCompactSummary, isMeta bool
	var inputTokens, outputTokens int64
	err = s.pool.QueryRow(ctx, `
		SELECT COUNT(*), MAX(uuid), MAX(parent_uuid), BOOL_OR(is_sidechain), MAX(agent_id),
			MAX(forked_from_session), MAX(forked_from_uuid), MAX(source_file), MAX(tool_use_id),
			BOOL_OR(is_compact_summary), BOOL_OR(is_meta), MAX(prompt_source), MAX(entrypoint),
			COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0)
		FROM session_records
		WHERE session_id = $1`, "legacy-claude-s1").Scan(
		&count, &uuid, &parentUUID, &isSidechain, &agentID,
		&forkedFromSession, &forkedFromUUID, &sourceFile, &toolUseID,
		&isCompactSummary, &isMeta, &promptSource, &entrypoint,
		&inputTokens, &outputTokens,
	)
	if err != nil {
		t.Fatalf("query session_records: %v", err)
	}
	if count != 1 {
		t.Fatalf("row count = %d, want 1", count)
	}
	if uuid != "turn-1" || parentUUID != "root-1" || !isSidechain || agentID != "agent-1" ||
		forkedFromSession != "origin-s1" || forkedFromUUID != "origin-turn-1" ||
		sourceFile != "session.jsonl" || toolUseID != "toolu_1" ||
		!isCompactSummary || !isMeta || promptSource != "typed" || entrypoint != "cli" {
		t.Fatalf("enrichment fields not updated: uuid=%q parent=%q source=%q prompt=%q entry=%q", uuid, parentUUID, sourceFile, promptSource, entrypoint)
	}
	if inputTokens != 10 || outputTokens != 3 {
		t.Fatalf("usage tokens changed to %d/%d, want 10/3", inputTokens, outputTokens)
	}
}

func TestPgStore_ReenrichSessionRecords_preservesCodexUsageTotals(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 7, 6, 11, 0, 0, 0, time.UTC)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
		Ts:              ts,
		SessionID:       "legacy-codex-s1",
		RecordType:      "usage",
		ProfileEmail:    "user@example.com",
		UserID:          "u1",
		Model:           "gpt-5.5",
		InputTokens:     intPtr(100),
		OutputTokens:    intPtr(20),
		CacheReadTokens: intPtr(40),
		Agent:           "codex",
		BillingProvider: "openai",
		Raw:             json.RawMessage(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`),
	}}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	before := queryCodexUsageTotals(t, s, "legacy-codex-s1")

	reenricher := requireSessionRecordReenricher(t, s)
	updated, err := reenricher.ReenrichSessionRecords(ctx, []*SessionRecord{{
		Ts:              ts,
		SessionID:       "legacy-codex-s1",
		RecordType:      "usage",
		ProfileEmail:    "user@example.com",
		UserID:          "u1",
		Model:           "gpt-5.5",
		InputTokens:     intPtr(999),
		OutputTokens:    intPtr(999),
		CacheReadTokens: intPtr(999),
		UUID:            "usage-turn-1",
		SourceFile:      "rollout.jsonl",
		Agent:           "codex",
		BillingProvider: "openai",
	}})
	if err != nil {
		t.Fatalf("ReenrichSessionRecords: %v", err)
	}
	if updated != 1 {
		t.Fatalf("updated rows = %d, want 1", updated)
	}
	after := queryCodexUsageTotals(t, s, "legacy-codex-s1")

	if after != before {
		t.Fatalf("codex usage totals changed: before=%+v after=%+v", before, after)
	}
}

type codexUsageTotals struct {
	Count       int
	InputTokens int64
	CacheRead   int64
	Output      int64
	CostScaled  int64
}

func queryCodexUsageTotals(t *testing.T, s *PgStore, sessionID string) codexUsageTotals {
	t.Helper()
	var totals codexUsageTotals
	err := s.pool.QueryRow(context.Background(), `
		SELECT COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(cache_read_tokens),0),
			COALESCE(SUM(output_tokens),0), COALESCE(ROUND(SUM(cost_usd) * 1000000000),0)::bigint
		FROM unified_events
		WHERE session_id = $1`, sessionID).Scan(
		&totals.Count, &totals.InputTokens, &totals.CacheRead, &totals.Output, &totals.CostScaled,
	)
	if err != nil {
		t.Fatalf("query unified_events: %v", err)
	}
	return totals
}

// TestReenrich_carriesLaterAddedFieldsOntoOlderRows pins the only route history has to the
// facts a newer client can derive.
//
// The insert path is ON CONFLICT DO NOTHING, so re-syncing an old session leaves its
// rows byte-for-byte as they were. Re-enrichment is the update path, and until now its
// SET list stopped at entrypoint -- every field v0.7.22 added (command classification,
// project identity, account attribution, task type) had no way back onto older rows.
func TestReenrich_carriesLaterAddedFieldsOntoOlderRows(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

	// As an older client wrote it: name recorded, nothing derived.
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
		Ts: ts, SessionID: "s1", RecordType: "user", ProfileEmail: "p@example.com",
		CommandName: "deploy", UUID: "u1", Raw: json.RawMessage(`{"text":"x"}`),
	}}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	if _, err := s.ReenrichSessionRecords(ctx, []*SessionRecord{{
		Ts: ts, SessionID: "s1", RecordType: "user", ProfileEmail: "p@example.com", UUID: "u1",
		CommandSource: "project", CommandKind: "skill", CommandInvoke: "explicit",
		RepositoryID: "github.com/org/repo", RepositoryName: "repo", RepoSubpath: "web/",
		AccountID: "acct-1", TaskType: "refactor",
	}}); err != nil {
		t.Fatalf("ReenrichSessionRecords: %v", err)
	}

	var src, kind, invoke, repoID, repoName, subpath, acct, task string
	if err := s.pool.QueryRow(ctx, `
		SELECT command_source, command_kind, command_invoke,
			repository_id, repository_name, repo_subpath, account_id, task_type
		FROM session_records WHERE session_id = 's1' AND uuid = 'u1'`).
		Scan(&src, &kind, &invoke, &repoID, &repoName, &subpath, &acct, &task); err != nil {
		t.Fatalf("read back: %v", err)
	}
	for _, c := range []struct{ got, want, name string }{
		{src, "project", "command_source"}, {kind, "skill", "command_kind"},
		{invoke, "explicit", "command_invoke"}, {repoID, "github.com/org/repo", "repository_id"},
		{repoName, "repo", "repository_name"}, {subpath, "web/", "repo_subpath"},
		{acct, "acct-1", "account_id"}, {task, "refactor", "task_type"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

func TestReenrich_neverBlanksAnAlreadyDerivedField(t *testing.T) {
	// A client that could not classify something sends the empty string. That must not
	// erase what an earlier pass established -- and for command_source the empty string
	// is itself a value: it marks a client that never looked, distinct from one that
	// looked and could not tell.
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
		Ts: ts, SessionID: "s1", RecordType: "user", ProfileEmail: "p@example.com",
		CommandName: "deploy", CommandSource: "plugin", AccountID: "acct-1",
		UUID: "u1", Raw: json.RawMessage(`{"text":"x"}`),
	}}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	if _, err := s.ReenrichSessionRecords(ctx, []*SessionRecord{{
		Ts: ts, SessionID: "s1", RecordType: "user", ProfileEmail: "p@example.com", UUID: "u1",
		// Everything blank, as an older client would send.
	}}); err != nil {
		t.Fatalf("ReenrichSessionRecords: %v", err)
	}

	var src, acct string
	if err := s.pool.QueryRow(ctx,
		`SELECT command_source, account_id FROM session_records WHERE session_id='s1' AND uuid='u1'`).
		Scan(&src, &acct); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if src != "plugin" || acct != "acct-1" {
		t.Errorf("blanked by an empty re-enrich: command_source=%q account_id=%q", src, acct)
	}
}

// TestReenrich_resetsAccountIDSourceWhenClientOverwritesIt covers the other half of
// #524's fix: a client-observed account_id overwriting one the server filled from a
// quota join must take account_id_source back to observed (blank) too, or an
// attribution pass later reads the stale 'quota-inferred' left behind and never
// revisits a value that just changed under it.
//
// Mutation: drop the account_id_source CASE from the reenrich UPSERT and this row
// keeps claiming 'quota-inferred' for an account_id the client itself observed.
func TestReenrich_resetsAccountIDSourceWhenClientOverwritesIt(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
		Ts: ts, SessionID: "s1", RecordType: "user", ProfileEmail: "p@example.com",
		AccountID: "acct-old", UUID: "u1", Raw: json.RawMessage(`{"text":"x"}`),
	}}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE session_records SET account_id_source = 'quota-inferred' WHERE session_id='s1' AND uuid='u1'`); err != nil {
		t.Fatalf("stamp account_id_source: %v", err)
	}

	if _, err := s.ReenrichSessionRecords(ctx, []*SessionRecord{{
		Ts: ts, SessionID: "s1", RecordType: "user", ProfileEmail: "p@example.com", UUID: "u1",
		AccountID: "acct-new",
	}}); err != nil {
		t.Fatalf("ReenrichSessionRecords: %v", err)
	}

	var acct, src string
	if err := s.pool.QueryRow(ctx,
		`SELECT account_id, account_id_source FROM session_records WHERE session_id='s1' AND uuid='u1'`).
		Scan(&acct, &src); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if acct != "acct-new" || src != "" {
		t.Errorf("account_id=%q account_id_source=%q, want (acct-new, \"\")", acct, src)
	}
}

// TestReenrich_leavesAccountIDSourceAloneWhenAccountIDUnchanged is the fill-only
// half: a client that sends no account_id must not touch what an earlier pass wrote
// for it, exactly like every other field in this UPSERT.
//
// Mutation: reset account_id_source whenever $23 is empty instead of only when it is
// non-empty, and a blank re-sync erases provenance the row already had.
func TestReenrich_leavesAccountIDSourceAloneWhenAccountIDUnchanged(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
		Ts: ts, SessionID: "s1", RecordType: "user", ProfileEmail: "p@example.com",
		AccountID: "acct-old", UUID: "u1", Raw: json.RawMessage(`{"text":"x"}`),
	}}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE session_records SET account_id_source = 'quota-inferred' WHERE session_id='s1' AND uuid='u1'`); err != nil {
		t.Fatalf("stamp account_id_source: %v", err)
	}

	if _, err := s.ReenrichSessionRecords(ctx, []*SessionRecord{{
		Ts: ts, SessionID: "s1", RecordType: "user", ProfileEmail: "p@example.com", UUID: "u1",
		// AccountID left blank, as a client with nothing new to report would send.
	}}); err != nil {
		t.Fatalf("ReenrichSessionRecords: %v", err)
	}

	var acct, src string
	if err := s.pool.QueryRow(ctx,
		`SELECT account_id, account_id_source FROM session_records WHERE session_id='s1' AND uuid='u1'`).
		Scan(&acct, &src); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if acct != "acct-old" || src != "quota-inferred" {
		t.Errorf("account_id=%q account_id_source=%q, want (acct-old, quota-inferred)", acct, src)
	}
}
