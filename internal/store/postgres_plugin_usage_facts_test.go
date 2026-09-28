package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type pluginFactTotals struct {
	invocations int64
	input       int64
	output      int64
}

// legacyPluginFactTotals is the response-window attribution query that PluginUsage
// used before plugin_invocation_facts. Keeping the reference in this differential
// test lets the maintained facts change implementation without changing semantics.
func legacyPluginFactTotals(t *testing.T, s *PgStore, since, until time.Time) map[string]pluginFactTotals {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), `
WITH commands AS (
	SELECT sr.session_id, sr.ts, sr.record_type, sr.command_name,
		COALESCE(NULLIF(sr.agent, ''), 'claude') AS agent,
		(SELECT MIN(u.ts) FROM session_records u
		 WHERE u.session_id = sr.session_id AND u.record_type = 'user'
		   AND (COALESCE(NULLIF(sr.agent, ''), 'claude') = 'codex'
		     OR jsonb_typeof(u.raw->'message'->'content') = 'string')
		   AND u.ts > sr.ts) AS next_user_ts
	FROM visible_session_records sr
	WHERE sr.command_name <> ''
	  AND (sr.command_source = '' OR sr.command_source = 'plugin')
	  AND sr.ts >= $1 AND sr.ts < $2
)
SELECT c.command_name, c.agent, COUNT(DISTINCT c.ts),
	COALESCE(SUM(CASE WHEN c.agent = 'codex'
		THEN GREATEST(COALESCE(r.input_tokens, 0) - COALESCE(r.cache_read_tokens, 0), 0)
		ELSE COALESCE(r.input_tokens, 0) END), 0),
	COALESCE(SUM(COALESCE(r.output_tokens, 0)), 0)
FROM commands c
LEFT JOIN session_records r ON r.session_id = c.session_id
	AND COALESCE(NULLIF(r.agent, ''), 'claude') = c.agent
	AND ((c.agent = 'codex' AND r.record_type = 'usage')
	  OR (c.agent <> 'codex' AND r.record_type = 'assistant'))
	AND ((c.record_type = 'assistant' AND r.ts >= c.ts)
	  OR (c.record_type <> 'assistant' AND r.ts > c.ts))
	AND (c.next_user_ts IS NULL OR r.ts < c.next_user_ts)
GROUP BY c.command_name, c.agent`, since, until)
	if err != nil {
		t.Fatalf("legacy attribution query: %v", err)
	}
	defer rows.Close()
	got := map[string]pluginFactTotals{}
	for rows.Next() {
		var name, agent string
		var totals pluginFactTotals
		if err := rows.Scan(&name, &agent, &totals.invocations, &totals.input, &totals.output); err != nil {
			t.Fatalf("scan legacy attribution: %v", err)
		}
		got[name+"/"+agent] = totals
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("legacy attribution rows: %v", err)
	}
	return got
}

func pluginUsageTotals(t *testing.T, s *PgStore, since, until time.Time) map[string]pluginFactTotals {
	t.Helper()
	rows, err := s.PluginUsage(context.Background(), since, until, "", "", "", "")
	if err != nil {
		t.Fatalf("PluginUsage: %v", err)
	}
	got := map[string]pluginFactTotals{}
	for _, row := range rows {
		got[row.CommandName+"/"+row.Agent] = pluginFactTotals{
			invocations: row.InvocationCount,
			input:       row.InputTokens,
			output:      row.OutputTokens,
		}
	}
	return got
}

func TestPluginInvocationFacts_DifferentialBoundariesTokensAndLateRecords(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, pluginInvocationFactsBackfill); err != nil {
		t.Fatalf("clear fact marker: %v", err)
	}
	if err := s.BackfillPluginInvocationFacts(ctx); err != nil {
		t.Fatalf("complete empty fact backfill: %v", err)
	}
	base := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	str := func(v string) json.RawMessage { return json.RawMessage(`{"message":{"content":"` + v + `"}}`) }
	toolResult := json.RawMessage(`{"message":{"content":[{"type":"tool_result","content":"ok"}]}}`)

	initial := []*SessionRecord{
		// A user command excludes an assistant at the exact command timestamp, crosses
		// tool-result user rows, and stops at Claude's next human-string user row.
		{Ts: base, SessionID: "claude-user", RecordType: "user", CommandName: "review", CommandSource: "plugin", Agent: "claude", UUID: "c-command", Raw: str("/review")},
		{Ts: base, SessionID: "claude-user", RecordType: "assistant", Agent: "claude", UUID: "c-tied", InputTokens: ptrInt(100), OutputTokens: ptrInt(100)},
		{Ts: base.Add(time.Second), SessionID: "claude-user", RecordType: "assistant", Agent: "claude", UUID: "c-a1", InputTokens: ptrInt(10), OutputTokens: ptrInt(2)},
		{Ts: base.Add(2 * time.Second), SessionID: "claude-user", RecordType: "user", Agent: "claude", UUID: "c-tool", Raw: toolResult},
		{Ts: base.Add(3 * time.Second), SessionID: "claude-user", RecordType: "assistant", Agent: "claude", UUID: "c-a2", InputTokens: ptrInt(20), OutputTokens: ptrInt(3)},
		{Ts: base.Add(5 * time.Second), SessionID: "claude-user", RecordType: "assistant", Agent: "claude", UUID: "c-after", InputTokens: ptrInt(200), OutputTokens: ptrInt(200)},

		// An assistant attributionSkill row includes its own token-bearing record.
		{Ts: base.Add(10 * time.Second), SessionID: "claude-assistant", RecordType: "assistant", CommandName: "implicit", CommandSource: "plugin", Agent: "claude", UUID: "ca-command", InputTokens: ptrInt(7), OutputTokens: ptrInt(4)},
		{Ts: base.Add(11 * time.Second), SessionID: "claude-assistant", RecordType: "user", Agent: "claude", UUID: "ca-human", Raw: str("next")},

		// Codex stops at every user row, only consumes usage rows, excludes a tied
		// usage row, and subtracts cache-read tokens without allowing negatives.
		{Ts: base.Add(20 * time.Second), SessionID: "codex", RecordType: "user", CommandName: "commit", Agent: "codex", UUID: "x-command"},
		{Ts: base.Add(20 * time.Second), SessionID: "codex", RecordType: "usage", Agent: "codex", UUID: "x-tied", InputTokens: ptrInt(50), OutputTokens: ptrInt(50)},
		{Ts: base.Add(21 * time.Second), SessionID: "codex", RecordType: "usage", Agent: "codex", UUID: "x-u1", InputTokens: ptrInt(10), CacheReadTokens: ptrInt(4), OutputTokens: ptrInt(2)},
		{Ts: base.Add(21500 * time.Millisecond), SessionID: "codex", RecordType: "usage", Agent: "codex", UUID: "x-u2", InputTokens: ptrInt(2), CacheReadTokens: ptrInt(9), OutputTokens: ptrInt(1)},
		{Ts: base.Add(22 * time.Second), SessionID: "codex", RecordType: "assistant", Agent: "codex", UUID: "x-assistant", InputTokens: ptrInt(100), OutputTokens: ptrInt(100)},
		{Ts: base.Add(24 * time.Second), SessionID: "codex", RecordType: "usage", Agent: "codex", UUID: "x-after", InputTokens: ptrInt(100), OutputTokens: ptrInt(100)},

		{Ts: base.Add(30 * time.Second), SessionID: "excluded-kind", RecordType: "user", CommandName: "clear", CommandSource: "builtin", Agent: "claude", UUID: "builtin"},
	}
	if err := s.InsertSessionRecords(ctx, initial); err != nil {
		t.Fatalf("initial insert: %v", err)
	}

	// These boundaries arrive after the response rows. Whole-session recomputation
	// must revise, rather than append to, the existing facts.
	late := []*SessionRecord{
		{Ts: base.Add(4 * time.Second), SessionID: "claude-user", RecordType: "user", Agent: "claude", UUID: "c-human", Raw: str("late human boundary")},
		{Ts: base.Add(23 * time.Second), SessionID: "codex", RecordType: "user", Agent: "codex", UUID: "x-boundary", Raw: toolResult},
	}
	if err := s.InsertSessionRecords(ctx, late); err != nil {
		t.Fatalf("late insert: %v", err)
	}
	// Idempotent sync must not duplicate either a source row or its fact.
	if err := s.InsertSessionRecords(ctx, late); err != nil {
		t.Fatalf("idempotent insert: %v", err)
	}

	since, until := base.Add(-time.Minute), base.Add(time.Minute)
	want := legacyPluginFactTotals(t, s, since, until)
	got := pluginUsageTotals(t, s, since, until)
	if len(got) != len(want) {
		t.Fatalf("fact groups = %#v, legacy groups = %#v", got, want)
	}
	for key, expected := range want {
		if got[key] != expected {
			t.Errorf("%s facts = %+v, legacy = %+v", key, got[key], expected)
		}
	}
	if got["review/claude"] != (pluginFactTotals{invocations: 1, input: 30, output: 5}) {
		t.Fatalf("Claude boundary/cache semantics = %+v", got["review/claude"])
	}
	if got["commit/codex"] != (pluginFactTotals{invocations: 1, input: 6, output: 3}) {
		t.Fatalf("Codex boundary/cache semantics = %+v", got["commit/codex"])
	}
}

// A real human reply that arrives as an array of text blocks (not a tool_result)
// must close a command's attribution window exactly like a plain-string reply --
// otherwise a later assistant row gets absorbed into the wrong command. Both
// next_user_ts queries (the raw fallback in postgres_plugin_usage.go and the
// maintained fact in pluginInvocationFactsInsertSQL) shared the same gap: only
// jsonb_typeof='string' closed the boundary, so an array-shaped human reply left
// it open through the next assistant row. Measured on dev: 18.1% of
// plugin_invocation_facts.output_tokens was misattributed this way.
func TestPluginInvocationFacts_ArrayShapedTextBlockClosesCommandBoundary(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC)
	arrayText := json.RawMessage(`{"message":{"content":[{"type":"text","text":"thanks, let's move on"}]}}`)

	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, pluginInvocationFactsBackfill); err != nil {
		t.Fatalf("clear fact marker: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: base, SessionID: "arr-cmd", RecordType: "user", CommandName: "review", CommandSource: "plugin", Agent: "claude", UUID: "arr-command"},
		{Ts: base.Add(time.Second), SessionID: "arr-cmd", RecordType: "assistant", Agent: "claude", UUID: "arr-a1", InputTokens: ptrInt(10), OutputTokens: ptrInt(5)},
		{Ts: base.Add(2 * time.Second), SessionID: "arr-cmd", RecordType: "user", Agent: "claude", UUID: "arr-human", Raw: arrayText},
		{Ts: base.Add(3 * time.Second), SessionID: "arr-cmd", RecordType: "assistant", Agent: "claude", UUID: "arr-after", InputTokens: ptrInt(1000), OutputTokens: ptrInt(1000)},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	assertClosed := func(t *testing.T, label string) {
		t.Helper()
		rows, err := s.PluginUsage(ctx, base.Add(-time.Minute), base.Add(time.Minute), "", "", "", "")
		if err != nil {
			t.Fatalf("%s: PluginUsage: %v", label, err)
		}
		if len(rows) != 1 {
			t.Fatalf("%s: rows = %+v, want 1", label, rows)
		}
		if rows[0].OutputTokens != 5 {
			t.Fatalf("%s: output_tokens = %d, want 5 (array-shaped human reply must close the command boundary)", label, rows[0].OutputTokens)
		}
	}
	assertClosed(t, "raw path")

	if err := s.BackfillPluginInvocationFacts(ctx); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	assertClosed(t, "fact path")
}

func TestPgStore_MigrateDoesNotWaitForPluginFactBackfill(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, pluginInvocationFactsBackfill); err != nil {
		t.Fatalf("clear fact marker: %v", err)
	}

	blocker, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin blocker: %v", err)
	}
	defer blocker.Rollback(ctx) //nolint:errcheck
	if err := lockPluginInvocationFacts(ctx, blocker, false); err != nil {
		t.Fatalf("hold fact backfill lock: %v", err)
	}
	migrateCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := s.Migrate(migrateCtx); err != nil {
		t.Fatalf("Migrate waited for plugin fact data repair: %v", err)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatalf("release fact backfill lock: %v", err)
	}
	if err := s.BackfillPluginInvocationFacts(ctx); err != nil {
		t.Fatalf("background fact backfill: %v", err)
	}
}

func TestPluginUsage_PreBackfillReadsExactRawAttribution(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, pluginInvocationFactsBackfill); err != nil {
		t.Fatalf("clear fact marker: %v", err)
	}
	// Direct source rows model history that predates application fact maintenance.
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records
		(ts, session_id, record_type, profile_email, user_id, agent, command_name, uuid, input_tokens, output_tokens, raw)
		VALUES ($1, 'pre-fact', 'user', 'pre@example.com', 'pre-user', 'claude', 'historical', 'pre-command', NULL, NULL, '{"message":{"content":"/historical"}}'),
		       ($2, 'pre-fact', 'assistant', 'pre@example.com', 'pre-user', 'claude', '', 'pre-response', 12, 3, '{}'),
		       ($3, 'pre-fact', 'user', 'pre@example.com', 'pre-user', 'claude', '', 'pre-boundary', NULL, NULL, '{"message":{"content":"next"}}'),
		       ($4, 'pre-fact', 'assistant', 'pre@example.com', 'pre-user', 'claude', '', 'pre-after', 100, 100, '{}')`,
		base, base.Add(time.Second), base.Add(2*time.Second), base.Add(3*time.Second)); err != nil {
		t.Fatalf("seed pre-backfill history: %v", err)
	}
	// New sync traffic is already fact-maintained while old history is not. Until the
	// marker commits, the read must use raw history wholesale rather than expose a
	// partial union or facts-only result.
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: base.Add(10 * time.Second), SessionID: "during-backfill", RecordType: "user", ProfileEmail: "pre@example.com", UserID: "pre-user", Agent: "claude", CommandName: "new", UUID: "new-command"},
		{Ts: base.Add(11 * time.Second), SessionID: "during-backfill", RecordType: "assistant", ProfileEmail: "pre@example.com", UserID: "pre-user", Agent: "claude", UUID: "new-response", InputTokens: ptrInt(5), OutputTokens: ptrInt(1)},
	}); err != nil {
		t.Fatalf("insert fact-maintained traffic: %v", err)
	}

	since, until := base.Add(-time.Hour), base.Add(time.Hour)
	want := legacyPluginFactTotals(t, s, since, until)
	got := pluginUsageTotals(t, s, since, until)
	if len(got) != len(want) {
		t.Fatalf("pre-backfill read = %#v, raw attribution = %#v", got, want)
	}
	for key, expected := range want {
		if got[key] != expected {
			t.Fatalf("pre-backfill %s = %+v, raw attribution = %+v", key, got[key], expected)
		}
	}
}

func TestPluginInvocationFacts_BackfillVisibilityAndLifecycle(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	// Bypass the application writer to model history present before this migration.
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records
		(ts, session_id, record_type, profile_email, user_id, agent, command_name, uuid, raw)
		VALUES ($1, 'historical-plugin', 'user', 'old@example.com', 'old-user', 'claude', 'history', 'h-command', '{"message":{"content":"/history"}}'),
		       ($2, 'historical-plugin', 'assistant', 'old@example.com', 'old-user', 'claude', '', 'h-response', '{}')`,
		base, base.Add(time.Second)); err != nil {
		t.Fatalf("seed historical rows: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, pluginInvocationFactsBackfill); err != nil {
		t.Fatalf("reset backfill marker: %v", err)
	}
	if err := s.BackfillPluginInvocationFacts(ctx); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if got := pluginUsageTotals(t, s, base.Add(-time.Hour), base.Add(time.Hour))["history/claude"]; got != (pluginFactTotals{invocations: 1}) {
		t.Fatalf("backfilled history = %+v", got)
	}
	// Exactly-once marker: a second call does not scan/rewrite established facts.
	if _, err := s.pool.Exec(ctx, `UPDATE plugin_invocation_facts SET input_tokens = 77 WHERE session_id = 'historical-plugin'`); err != nil {
		t.Fatalf("mutate fact: %v", err)
	}
	if err := s.BackfillPluginInvocationFacts(ctx); err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if got := pluginUsageTotals(t, s, base.Add(-time.Hour), base.Add(time.Hour))["history/claude"].input; got != 77 {
		t.Fatalf("backfill reran after marker; input = %d", got)
	}

	if err := s.InsertEvents(ctx, []*OtelEvent{{Ts: base, EventName: "api_request", SessionID: "historical-plugin", ProfileEmail: "old@example.com", UserID: "old-user", LoginEmail: "login@example.com"}}); err != nil {
		t.Fatalf("insert identity event: %v", err)
	}
	rows, err := s.PluginUsage(ctx, base.Add(-time.Hour), base.Add(time.Hour), "", "login@example.com", "", "")
	if err != nil || len(rows) != 1 {
		t.Fatalf("login-scoped facts = %+v, err %v", rows, err)
	}
	if _, err := s.ExcludeAccount(ctx, "login@example.com", "test", "tester"); err != nil {
		t.Fatalf("exclude account: %v", err)
	}
	if rows, err = s.PluginUsage(ctx, base.Add(-time.Hour), base.Add(time.Hour), "", "", "", ""); err != nil || len(rows) != 0 {
		t.Fatalf("excluded facts = %+v, err %v", rows, err)
	}
	if err := s.RemoveExcludedAccount(ctx, "login@example.com"); err != nil {
		t.Fatalf("remove exclusion: %v", err)
	}
	if err := s.MergeUsers(ctx, "old@example.com", "", "new@example.com"); err != nil {
		t.Fatalf("merge user: %v", err)
	}
	if rows, err = s.PluginUsage(ctx, base.Add(-time.Hour), base.Add(time.Hour), "new@example.com", "", "", ""); err != nil || len(rows) != 1 {
		t.Fatalf("merged facts = %+v, err %v", rows, err)
	}
	if err := s.DeleteUserData(ctx, "new@example.com", ""); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	var facts int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM plugin_invocation_facts WHERE session_id = 'historical-plugin'`).Scan(&facts); err != nil {
		t.Fatalf("count lifecycle facts: %v", err)
	}
	if facts != 0 {
		t.Fatalf("facts after source deletion = %d, want 0", facts)
	}
}

func TestPluginInvocationFacts_RetentionCleanupRecomputesCrossCutoffSession(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: base, SessionID: "retained-plugin", RecordType: "user", CommandName: "old", Agent: "claude", UUID: "old-command"},
		{Ts: base.Add(40 * 24 * time.Hour), SessionID: "retained-plugin", RecordType: "assistant", Agent: "claude", UUID: "survivor", InputTokens: ptrInt(10)},
	}); err != nil {
		t.Fatalf("insert retained session: %v", err)
	}
	// Model a completed Timescale drop: the old command chunk is gone while a newer
	// response from the same long session survives.
	if _, err := s.pool.Exec(ctx, `DELETE FROM session_records WHERE uuid = 'old-command'`); err != nil {
		t.Fatalf("drop old source chunk row: %v", err)
	}
	n, err := s.ReconcilePluginInvocationFactsForRetention(ctx)
	if err != nil {
		t.Fatalf("retention reconcile: %v", err)
	}
	if n != 1 {
		t.Fatalf("reconciled sessions = %d, want 1", n)
	}
	var facts int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM plugin_invocation_facts WHERE session_id = 'retained-plugin'`).Scan(&facts); err != nil {
		t.Fatalf("count retained facts: %v", err)
	}
	if facts != 0 {
		t.Fatalf("stale facts after retention = %d", facts)
	}
}

func TestPluginUsageReadQueryDoesNotScanResponseWindowSessionRecords(t *testing.T) {
	var sqlLines []string
	for _, line := range strings.Split(pluginUsageQuery, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			sqlLines = append(sqlLines, line)
		}
	}
	q := strings.ToLower(strings.Join(sqlLines, "\n"))
	if strings.Contains(q, "session_records") || strings.Contains(q, "visible_session_records") {
		t.Fatalf("PluginUsage read query still scans raw session records:\n%s", pluginUsageQuery)
	}
	if !strings.Contains(q, "plugin_invocation_facts") {
		t.Fatalf("PluginUsage read query does not aggregate plugin facts:\n%s", pluginUsageQuery)
	}
}

// The two read paths must agree. A pre-backfill test already pins the raw path; this
// pins the other half, which is where the paths actually diverged: the fact INSERT
// carried the old two-value command_source test (empty or plugin), copied from
// postgres_plugin_usage.go as it stood at v0.7.22. #324 replaced that line the next
// day with a rule that also subtracts the commands the binary ships. The fact table
// had thrown command_source away, so nothing downstream could re-apply the newer
// rule -- /clear, /model and /rename counted as plugin usage from the moment the
// marker landed, which is the 5.5x over-count #324 removed.
//
// Asserting equality rather than a fixed number on purpose: the builtin list is
// regenerated from the vendors' docs and moves with releases, so a hard-coded
// expectation would have to be edited every time the list does, and an edit is where
// a wrong number gets normalised.
func TestPluginUsage_FactPathMatchesRawPathOnBuiltins(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	// clear/model are Claude builtins, rename is a Codex builtin, myskill is not one,
	// and deploy arrives already classified. Keyed by agent because Codex ships
	// /rename and Claude does not: a flat list would let one agent's builtin excuse
	// the other's plugin.
	records := []*SessionRecord{
		{Ts: base, SessionID: "b-1", RecordType: "user", ProfileEmail: "u@example.com", UserID: "u", Agent: "claude", CommandName: "clear", UUID: "b-clear"},
		{Ts: base.Add(time.Second), SessionID: "b-1", RecordType: "assistant", ProfileEmail: "u@example.com", UserID: "u", Agent: "claude", UUID: "b-clear-r", InputTokens: ptrInt(7), OutputTokens: ptrInt(2)},
		{Ts: base.Add(2 * time.Second), SessionID: "b-2", RecordType: "user", ProfileEmail: "u@example.com", UserID: "u", Agent: "claude", CommandName: "model", UUID: "b-model"},
		{Ts: base.Add(3 * time.Second), SessionID: "b-2", RecordType: "assistant", ProfileEmail: "u@example.com", UserID: "u", Agent: "claude", UUID: "b-model-r", InputTokens: ptrInt(9), OutputTokens: ptrInt(4)},
		{Ts: base.Add(4 * time.Second), SessionID: "b-3", RecordType: "user", ProfileEmail: "u@example.com", UserID: "u", Agent: "codex", CommandName: "rename", UUID: "b-rename"},
		{Ts: base.Add(5 * time.Second), SessionID: "b-3", RecordType: "usage", ProfileEmail: "u@example.com", UserID: "u", Agent: "codex", UUID: "b-rename-r", InputTokens: ptrInt(11), OutputTokens: ptrInt(5)},
		{Ts: base.Add(6 * time.Second), SessionID: "b-4", RecordType: "user", ProfileEmail: "u@example.com", UserID: "u", Agent: "claude", CommandName: "myskill", UUID: "b-skill"},
		{Ts: base.Add(7 * time.Second), SessionID: "b-4", RecordType: "assistant", ProfileEmail: "u@example.com", UserID: "u", Agent: "claude", UUID: "b-skill-r", InputTokens: ptrInt(13), OutputTokens: ptrInt(6)},
		{Ts: base.Add(8 * time.Second), SessionID: "b-5", RecordType: "user", ProfileEmail: "u@example.com", UserID: "u", Agent: "claude", CommandName: "deploy", CommandSource: "plugin", UUID: "b-deploy"},
		{Ts: base.Add(9 * time.Second), SessionID: "b-5", RecordType: "assistant", ProfileEmail: "u@example.com", UserID: "u", Agent: "claude", UUID: "b-deploy-r", InputTokens: ptrInt(15), OutputTokens: ptrInt(8)},
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.BackfillPluginInvocationFacts(ctx); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	since, until := base.Add(-time.Hour), base.Add(time.Hour)

	// Both readings come from PluginUsage itself, toggled by the marker. The other
	// tests compare against legacyPluginFactTotals, whose SQL carries the same
	// v0.7.22 line the fact INSERT copied -- comparing the two would have agreed
	// while both were wrong, which is how this shipped.
	got := pluginUsageTotals(t, s, since, until)
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, pluginInvocationFactsBackfill); err != nil {
		t.Fatalf("clear fact marker: %v", err)
	}
	want := pluginUsageTotals(t, s, since, until)
	if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills (name) VALUES ($1)`, pluginInvocationFactsBackfill); err != nil {
		t.Fatalf("restore fact marker: %v", err)
	}

	for key := range got {
		if _, ok := want[key]; !ok {
			t.Errorf("fact path counted %s, raw path does not", key)
		}
	}
	for key, expected := range want {
		actual, ok := got[key]
		if !ok {
			t.Errorf("raw path counted %s, fact path does not", key)
			continue
		}
		if actual != expected {
			t.Errorf("%s: fact path = %+v, raw path = %+v", key, actual, expected)
		}
	}

	// The seed is only meaningful if it produced something to count and something to
	// drop. Without this the test passes just as well against two empty results.
	if len(want) == 0 {
		t.Fatal("raw path counted nothing; the seed cannot distinguish the paths")
	}
	if len(want) >= len(records)/2 {
		t.Fatalf("raw path counted %d of %d commands; no builtin was dropped", len(want), len(records)/2)
	}
}

// Asserting the plan rather than the SQL text. A test that greps for the word
// MATERIALIZED passes whenever the word is present, which says nothing about whether
// it did anything -- and what actually breaks is a shape, not a spelling. Postgres
// inlines a single-reference CTE, and inlining moves next_user_ts into the LEFT JOIN
// as a SubPlan in the join filter, where it is re-evaluated per candidate row pair
// instead of once per command. The dev snapshot took 12+ minutes inlined and 2.9
// seconds materialized. The backfill holds the exclusive advisory lock that the
// ingest path waits on as a shared lock, so that difference is how long session
// record writes stall on a fresh deploy.
func TestPluginInvocationFactsBackfillMaterializesCommands(t *testing.T) {
	s := acquireTestStore(t)
	rows, err := s.pool.Query(context.Background(), "EXPLAIN "+pluginInvocationFactsInsertSQL, nil)
	if err != nil {
		t.Fatalf("explain backfill: %v", err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan = append(plan, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read plan: %v", err)
	}
	joined := strings.Join(plan, "\n")

	if !strings.Contains(joined, "CTE Scan on commands") {
		t.Fatalf("commands CTE was inlined -- next_user_ts will be re-evaluated per join row:\n%s", joined)
	}
	for _, line := range plan {
		if strings.Contains(line, "Join Filter:") && strings.Contains(line, "SubPlan") {
			t.Fatalf("join filter re-evaluates a subquery per row pair:\n%s", line)
		}
	}
}

// #717: the fact path matched excluded_accounts and excluded_sessions only. Codex
// rows carry a billing id and never an address, and excluded_sessions is built
// from otel_events, where Codex has no rows -- so an account excluded by billing
// id, or through a link from an excluded address, kept its plugin usage.
func TestPluginUsageFactsHideExcludedBillingAccounts(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)
	command := func(session, account string, at time.Time) *SessionRecord {
		return &SessionRecord{Ts: at, SessionID: session, UUID: session + "-cmd", RecordType: "user",
			CommandName: "commit", Agent: "codex", BillingProvider: "openai", AccountID: account}
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{command("personal", "acct-personal", base), command("team", "acct-team", base.Add(time.Second))}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.BackfillPluginInvocationFacts(ctx); err != nil {
		t.Fatalf("BackfillPluginInvocationFacts: %v", err)
	}
	invocations := func() int64 {
		t.Helper()
		return pluginUsageTotals(t, s, base.Add(-time.Minute), base.Add(time.Minute))["commit/codex"].invocations
	}
	if got := invocations(); got != 2 {
		t.Fatalf("invocations before exclusion = %d, want 2", got)
	}

	if _, _, err := s.ExcludeBillingAccount(ctx, "openai", "acct-personal", "personal", "admin"); err != nil {
		t.Fatalf("ExcludeBillingAccount: %v", err)
	}
	if got := invocations(); got != 1 {
		t.Errorf("invocations with the billing account excluded = %d, want 1", got)
	}
	if err := s.RemoveExcludedBillingAccount(ctx, "openai", "acct-personal"); err != nil {
		t.Fatalf("RemoveExcludedBillingAccount: %v", err)
	}
	if got := invocations(); got != 2 {
		t.Errorf("invocations after un-excluding the billing account = %d, want 2", got)
	}

	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{personalLinkSample("acct-personal", "personal@example.test")}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
	if _, err := s.ExcludeAccount(ctx, "personal@example.test", "personal", "admin"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}
	if got := invocations(); got != 1 {
		t.Errorf("invocations with the linked address excluded = %d, want 1", got)
	}
	if err := s.RemoveExcludedAccount(ctx, "personal@example.test"); err != nil {
		t.Fatalf("RemoveExcludedAccount: %v", err)
	}
	if got := invocations(); got != 2 {
		t.Errorf("invocations after un-excluding the address = %d, want 2", got)
	}
}

// Facts written before the billing columns existed carry an empty value there, so the
// exclusion above cannot reach them until the one-time v2 rebuild fills them in.
func TestPluginInvocationFactsBackfillFillsBillingAccountOfOldFacts(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{Ts: time.Now().UTC(), SessionID: "old", UUID: "old-cmd",
		RecordType: "user", CommandName: "commit", Agent: "codex", BillingProvider: "openai", AccountID: "acct-old"}}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	// The state an upgraded database starts in: facts without billing, v1 marker only.
	if _, err := s.pool.Exec(ctx, `UPDATE plugin_invocation_facts SET billing_provider = '', account_id = ''`); err != nil {
		t.Fatalf("blank billing: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, pluginInvocationFactsBackfill); err != nil {
		t.Fatalf("clear marker: %v", err)
	}
	if err := s.BackfillPluginInvocationFacts(ctx); err != nil {
		t.Fatalf("BackfillPluginInvocationFacts: %v", err)
	}
	var provider, account string
	if err := s.pool.QueryRow(ctx, `SELECT billing_provider, account_id FROM plugin_invocation_facts`).Scan(&provider, &account); err != nil {
		t.Fatalf("read fact: %v", err)
	}
	if provider != "openai" || account != "acct-old" {
		t.Fatalf("fact billing = %q/%q after backfill, want openai/acct-old", provider, account)
	}
}
