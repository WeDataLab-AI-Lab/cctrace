package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func codexMessageRaw(role, text string) json.RawMessage {
	return json.RawMessage(`{"type":"response_item","payload":{"type":"message","role":"` + role +
		`","content":[{"type":"input_text","text":"` + text + `"}]}}`)
}

// A client released before the codexlog fix still sends developer messages as
// record_type='user'. Stored as-is they would come back as typed turns after the
// migration below repaired history, and sit beside the repaired row as a duplicate.
func TestInsertSessionRecords_CodexDeveloperFromOldClientStoredAsDeveloper(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	base := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: base, SessionID: "cx-old", RecordType: "user", Agent: "codex", ProfileEmail: "u@example.com", Raw: codexMessageRaw("developer", "<skills_instructions>x</skills_instructions>")},
		{Ts: base.Add(time.Minute), SessionID: "cx-old", RecordType: "user", Agent: "codex", ProfileEmail: "u@example.com", Raw: codexMessageRaw("user", "fix the failing test")},
		// Old rollout format: role sits at the top level, not under payload.
		{Ts: base.Add(2 * time.Minute), SessionID: "cx-old", RecordType: "user", Agent: "codex", ProfileEmail: "u@example.com", Raw: json.RawMessage(`{"type":"message","role":"developer","content":[{"type":"input_text","text":"instructions"}]}`)},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	for _, ts := range []time.Time{base, base.Add(2 * time.Minute)} {
		var recordType, taskType string
		if err := s.pool.QueryRow(ctx, `SELECT record_type, task_type FROM session_records
			WHERE session_id = 'cx-old' AND ts = $1`, ts).Scan(&recordType, &taskType); err != nil {
			t.Fatalf("query %s: %v", ts, err)
		}
		if recordType != "developer" || taskType != "" {
			t.Errorf("developer row at %s = (%q, %q), want (developer, '')", ts, recordType, taskType)
		}
	}
	var typedTurns int
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(SUM(typed_turn_count), 0) FROM task_segment_facts
		WHERE session_id = 'cx-old'`).Scan(&typedTurns); err != nil {
		t.Fatalf("query facts: %v", err)
	}
	if typedTurns != 1 {
		t.Errorf("typed turns = %d, want 1", typedTurns)
	}
}

// Re-enrichment matches rows on record_type, so an old client re-enriching a
// developer line it still calls 'user' would silently miss the relabeled row.
func TestReenrichSessionRecords_CodexDeveloperFromOldClientReachesDeveloperRow(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	base := time.Date(2026, 9, 1, 11, 0, 0, 0, time.UTC)
	raw := codexMessageRaw("developer", "<skills_instructions>x</skills_instructions>")
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: base, SessionID: "cx-reenrich", RecordType: "developer", Agent: "codex", ProfileEmail: "u@example.com", Raw: raw},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	updated, err := s.ReenrichSessionRecords(ctx, []*SessionRecord{
		{Ts: base, SessionID: "cx-reenrich", RecordType: "user", Agent: "codex", ProfileEmail: "u@example.com", Raw: raw, SourceFile: "rollout.jsonl"},
	})
	if err != nil {
		t.Fatalf("reenrich: %v", err)
	}
	if updated != 1 {
		t.Errorf("updated = %d, want 1", updated)
	}
	var sourceFile string
	if err := s.pool.QueryRow(ctx, `SELECT source_file FROM session_records
		WHERE session_id = 'cx-reenrich' AND record_type = 'developer'`).Scan(&sourceFile); err != nil {
		t.Fatalf("query: %v", err)
	}
	if sourceFile != "rollout.jsonl" {
		t.Errorf("source_file = %q, want rollout.jsonl", sourceFile)
	}
}

func TestMigrate_RepairsCodexDeveloperRowsStoredAsUser(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, codexDeveloperRecordTypeRepair); err != nil {
		t.Fatalf("clear repair marker: %v", err)
	}
	for _, name := range []string{taskSegmentFactsBackfill, pluginInvocationFactsBackfill} {
		if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills (name) VALUES ($1) ON CONFLICT DO NOTHING`, name); err != nil {
			t.Fatalf("seed marker %s: %v", name, err)
		}
	}

	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	seed := func(session string, ts time.Time, recordType, role, taskType, classifierVersion string) {
		t.Helper()
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_records
			(ts, session_id, record_type, profile_email, agent, raw, task_type, classifier_version)
			VALUES ($1, $2, $3, 'u@example.com', 'codex', $4, $5, $6)`,
			ts, session, recordType, codexMessageRaw(role, "text"), taskType, classifierVersion); err != nil {
			t.Fatalf("seed %s %s: %v", session, recordType, err)
		}
	}
	seed("cx-dev", base, "user", "developer", "unknown", "keyword-v1")
	seed("cx-dev", base.Add(time.Minute), "user", "user", "testing", "keyword-v1")
	// A new client already stored this developer message; an old client re-synced
	// the same line as user. Relabeling the user copy would violate the unique key.
	seed("cx-twin", base, "developer", "developer", "", "")
	seed("cx-twin", base, "user", "developer", "", "")

	for i := 0; i < 2; i++ {
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("migrate pass %d: %v", i+1, err)
		}
	}

	rows, err := s.pool.Query(ctx, `SELECT session_id, record_type, task_type, classifier_version
		FROM session_records ORDER BY session_id, ts, record_type`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var sid, rt, tt, cv string
		if err := rows.Scan(&sid, &rt, &tt, &cv); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, sid+"/"+rt+"/"+tt+"/"+cv)
	}
	want := []string{"cx-dev/developer//", "cx-dev/user/testing/keyword-v1", "cx-twin/developer//"}
	if len(got) != len(want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %q, want %q", i, got[i], want[i])
		}
	}

	// A full rebuild holds the exclusive fact locks every session record write waits
	// on (4m41s on a 6.0M-row local copy), so the relabel must not trigger one.
	var kept int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM schema_backfills WHERE name = ANY($1)`,
		[]string{taskSegmentFactsBackfill, pluginInvocationFactsBackfill}).Scan(&kept); err != nil {
		t.Fatalf("query markers: %v", err)
	}
	if kept != 2 {
		t.Errorf("fact markers kept = %d, want 2", kept)
	}
	resetCodexDeveloperFactsRepair(t, s)
	if err := s.BackfillCodexDeveloperFacts(ctx); err != nil {
		t.Fatalf("recompute affected sessions: %v", err)
	}
	var typedTurns int
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(SUM(typed_turn_count), 0) FROM task_segment_facts
		WHERE session_id = 'cx-dev'`).Scan(&typedTurns); err != nil {
		t.Fatalf("query facts: %v", err)
	}
	if typedTurns != 1 {
		t.Errorf("typed turns = %d, want 1", typedTurns)
	}
}

func resetCodexDeveloperFactsRepair(t *testing.T, s *PgStore) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, codexDeveloperFactsRepair); err != nil {
		t.Fatalf("clear facts repair marker: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE codex_developer_facts_repair_cursor SET last_session_id = ''`); err != nil {
		t.Fatalf("reset facts repair cursor: %v", err)
	}
}

// seedCodexDeveloperSession writes rows directly, as history does: no facts exist
// for the session until something recomputes them.
func seedCodexDeveloperSession(t *testing.T, s *PgStore, session string, base time.Time) {
	t.Helper()
	ctx := context.Background()
	for i, row := range []struct{ recordType, role string }{{"developer", "developer"}, {"user", "user"}} {
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_records
			(ts, session_id, record_type, profile_email, agent, raw)
			VALUES ($1, $2, $3, 'u@example.com', 'codex', $4)`,
			base.Add(time.Duration(i)*time.Second), session, row.recordType, codexMessageRaw(row.role, "text")); err != nil {
			t.Fatalf("seed %s %s: %v", session, row.recordType, err)
		}
	}
}

func typedTurnsFor(t *testing.T, s *PgStore, session string) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(), `SELECT COALESCE(SUM(typed_turn_count), 0)
		FROM task_segment_facts WHERE session_id = $1`, session).Scan(&n); err != nil {
		t.Fatalf("query facts for %s: %v", session, err)
	}
	return n
}

func TestBackfillCodexDeveloperFacts_ResumesAfterTheLastCommittedBatch(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	resetCodexDeveloperFactsRepair(t, s)

	base := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	for _, session := range []string{"cx-a", "cx-b", "cx-c"} {
		seedCodexDeveloperSession(t, s, session, base)
	}

	// One batch commits, then the process dies.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	n, err := refreshCodexDeveloperFactsBatch(ctx, tx, 1)
	if err != nil {
		t.Fatalf("first batch: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if n != 1 {
		t.Fatalf("first batch sessions = %d, want 1", n)
	}
	var cursor string
	if err := s.pool.QueryRow(ctx, `SELECT last_session_id FROM codex_developer_facts_repair_cursor`).Scan(&cursor); err != nil {
		t.Fatalf("query cursor: %v", err)
	}
	if cursor != "cx-a" {
		t.Errorf("cursor = %q, want cx-a", cursor)
	}
	if got := typedTurnsFor(t, s, "cx-b"); got != 0 {
		t.Errorf("cx-b typed turns before resume = %d, want 0", got)
	}

	// The next boot resumes after the cursor and finishes.
	if err := s.BackfillCodexDeveloperFacts(ctx); err != nil {
		t.Fatalf("resume: %v", err)
	}
	for _, session := range []string{"cx-a", "cx-b", "cx-c"} {
		if got := typedTurnsFor(t, s, session); got != 1 {
			t.Errorf("%s typed turns = %d, want 1", session, got)
		}
	}

	// Once marked done, a later boot touches nothing.
	if _, err := s.pool.Exec(ctx, `DELETE FROM task_segment_facts WHERE session_id = 'cx-c'`); err != nil {
		t.Fatalf("drop cx-c facts: %v", err)
	}
	if err := s.BackfillCodexDeveloperFacts(ctx); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if got := typedTurnsFor(t, s, "cx-c"); got != 0 {
		t.Errorf("cx-c typed turns after completed rerun = %d, want 0", got)
	}
}

// The full rebuild this replaces held the exclusive fact locks, and every session
// record write waited on them. A batch must hold only its own sessions' locks.
func TestCodexDeveloperFactsBatch_DoesNotBlockIngestForOtherSessions(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	resetCodexDeveloperFactsRepair(t, s)
	seedCodexDeveloperSession(t, s, "cx-held", time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC))

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // committed below on success
	if _, err := refreshCodexDeveloperFactsBatch(ctx, tx, 100); err != nil {
		t.Fatalf("batch: %v", err)
	}

	insertCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.InsertSessionRecords(insertCtx, []*SessionRecord{
		{Ts: time.Date(2026, 9, 2, 10, 1, 0, 0, time.UTC), SessionID: "other", RecordType: "user", Agent: "claude", ProfileEmail: "u@example.com", UUID: "o1", Raw: str("unrelated work")},
	}); err != nil {
		t.Fatalf("insert while a batch is open: %v", err)
	}
	if _, err := s.ReenrichSessionRecords(insertCtx, []*SessionRecord{
		{Ts: time.Date(2026, 9, 2, 10, 1, 0, 0, time.UTC), SessionID: "other", RecordType: "user", Agent: "claude", ProfileEmail: "u@example.com", UUID: "o1", SourceFile: "chat.jsonl"},
	}); err != nil {
		t.Fatalf("reenrich while a batch is open: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit batch: %v", err)
	}
}
