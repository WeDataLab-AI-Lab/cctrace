package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func clearLoginEmailHistoryBackfillMarker(t *testing.T, s *PgStore) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, loginEmailHistoryBackfill); err != nil {
		t.Fatalf("clear marker: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM login_email_history_repair_state WHERE name = $1`, loginEmailHistoryBackfill); err != nil {
		t.Fatalf("clear repair state: %v", err)
	}
	t.Cleanup(func() {
		if _, err := s.pool.Exec(ctx, `DELETE FROM login_email_history_repair_state WHERE name = $1`, loginEmailHistoryBackfill); err != nil {
			t.Fatalf("clear repair state: %v", err)
		}
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO schema_backfills (name) VALUES ($1) ON CONFLICT DO NOTHING`, loginEmailHistoryBackfill); err != nil {
			t.Fatalf("restore marker: %v", err)
		}
	})
}

func seedLoginEmailHistoryBackfill(t *testing.T, s *PgStore, suffix string, observed int) {
	t.Helper()
	ctx := context.Background()
	base := recentDay(38).Add(10 * time.Hour)
	events := []*OtelEvent{
		{Ts: base, EventName: "api_request", SessionID: "timeline-" + suffix, UserID: "quiet-user-" + suffix, LoginEmail: "inferred@example.com"},
	}
	records := []*SessionRecord{
		{Ts: base.Add(time.Minute), SessionID: "quiet-" + suffix, RecordType: "user", UUID: "inferred-" + suffix, UserID: "quiet-user-" + suffix, Raw: json.RawMessage(`{}`)},
	}
	for i := 0; i < observed; i++ {
		session := fmt.Sprintf("observed-%s-%02d", suffix, i)
		events = append(events, &OtelEvent{Ts: base, EventName: "api_request", SessionID: session, UserID: "observed-user-" + suffix, LoginEmail: "observed@example.com"})
		records = append(records, &SessionRecord{Ts: base.Add(time.Minute), SessionID: session, RecordType: "user", UUID: session, UserID: "observed-user-" + suffix, Raw: json.RawMessage(`{}`)})
	}
	if err := s.InsertEvents(ctx, events); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
}

func historyRepairMarked(t *testing.T, s *PgStore) bool {
	t.Helper()
	var marked bool
	if err := s.pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM schema_backfills WHERE name = $1)`, loginEmailHistoryBackfill).Scan(&marked); err != nil {
		t.Fatalf("read marker: %v", err)
	}
	return marked
}

// Snapshot limits must override larger session defaults only inside the setup
// transaction. Checking the same acquired connection after commit proves the
// helper uses transaction-local settings and cannot contaminate the pool.
func TestLoginEmailHistorySnapshotResourceLimitsAreTransactionLocal(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer conn.Release()
	defer func() {
		_, _ = conn.Exec(context.Background(), `RESET work_mem`)
		_, _ = conn.Exec(context.Background(), `RESET max_parallel_workers_per_gather`)
	}()
	if _, err := conn.Exec(ctx, `SET work_mem = '64MB'`); err != nil {
		t.Fatalf("set session work_mem: %v", err)
	}
	if _, err := conn.Exec(ctx, `SET max_parallel_workers_per_gather = 2`); err != nil {
		t.Fatalf("set session parallel workers: %v", err)
	}

	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatalf("begin snapshot transaction: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := setLoginEmailHistorySnapshotResourceLimits(ctx, tx); err != nil {
		t.Fatalf("apply snapshot limits: %v", err)
	}
	var workMem, workers string
	if err := tx.QueryRow(ctx, `SELECT current_setting('work_mem'),
		current_setting('max_parallel_workers_per_gather')`).Scan(&workMem, &workers); err != nil {
		t.Fatalf("read local limits: %v", err)
	}
	if workMem != loginEmailHistorySnapshotWorkMem || workers != loginEmailHistorySnapshotParallelWorkers {
		t.Fatalf("local limits = (%q, %q), want (%q, %q)", workMem, workers,
			loginEmailHistorySnapshotWorkMem, loginEmailHistorySnapshotParallelWorkers)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit snapshot transaction: %v", err)
	}
	if err := conn.QueryRow(ctx, `SELECT current_setting('work_mem'),
		current_setting('max_parallel_workers_per_gather')`).Scan(&workMem, &workers); err != nil {
		t.Fatalf("read restored session settings: %v", err)
	}
	if workMem != "64MB" || workers != "2" {
		t.Fatalf("settings leaked after commit: got (%q, %q), want (64MB, 2)", workMem, workers)
	}
}

// The staged implementation must produce exactly the same source/inference
// attribution as the established unbounded SQL, even when every commit contains
// only one source row.
func TestLoginEmailHistoryRepairBatchesEquivalentToFullPass(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	clearLoginEmailHistoryBackfillMarker(t, s)
	ctx := context.Background()
	seedLoginEmailHistoryBackfill(t, s, "equivalence", 3)

	if _, err := s.BackfillSessionRecordLoginEmail(ctx, time.Time{}); err != nil {
		t.Fatalf("reference source pass: %v", err)
	}
	if _, err := s.InferSessionRecordLoginEmail(ctx, time.Time{}); err != nil {
		t.Fatalf("reference inference pass: %v", err)
	}
	expected := map[string][2]string{}
	rows, err := s.pool.Query(ctx, `SELECT uuid, login_email, login_email_source FROM session_records ORDER BY id`)
	if err != nil {
		t.Fatalf("read reference: %v", err)
	}
	for rows.Next() {
		var uuid, email, source string
		if err := rows.Scan(&uuid, &email, &source); err != nil {
			t.Fatalf("scan reference: %v", err)
		}
		expected[uuid] = [2]string{email, source}
	}
	rows.Close()
	if _, err := s.pool.Exec(ctx, `UPDATE session_records SET login_email = '', login_email_source = ''`); err != nil {
		t.Fatalf("reset source rows: %v", err)
	}

	for {
		result, err := s.backfillSessionRecordLoginEmailHistoryBatch(ctx, 1)
		if err != nil {
			t.Fatalf("batch: %v", err)
		}
		if result.Complete {
			if result.SourceUpdated != 3 || result.InferenceUpdated != 1 {
				t.Fatalf("completed totals = (%d, %d), want (3, 1)", result.SourceUpdated, result.InferenceUpdated)
			}
			break
		}
	}
	for uuid, want := range expected {
		if email, source := loginEmailSourceOf(t, s, uuid); email != want[0] || source != want[1] {
			t.Errorf("%s = (%q, %q), want (%q, %q)", uuid, email, source, want[0], want[1])
		}
	}
}

// One committed batch can be followed by a process restart and even loss of the
// original OTEL rows. Resume must use the durable interval snapshot, not derive a
// shorter or different timeline from what happens to remain.
func TestLoginEmailHistoryRepairResumesFromDurableSnapshot(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	clearLoginEmailHistoryBackfillMarker(t, s)
	ctx := context.Background()
	seedLoginEmailHistoryBackfill(t, s, "resume", 3)

	result, err := s.backfillSessionRecordLoginEmailHistoryBatch(ctx, 1)
	if err != nil {
		t.Fatalf("first batch: %v", err)
	}
	if result.SourceUpdated != 1 || result.Complete {
		t.Fatalf("first batch = %+v, want one committed source row", result)
	}
	if historyRepairMarked(t, s) {
		t.Fatal("completion marker exists after first batch")
	}
	var stagedSessions, stagedUsers int
	if err := s.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM login_email_history_session_intervals WHERE repair_name=$1),
		(SELECT count(*) FROM login_email_history_user_intervals WHERE repair_name=$1)`, loginEmailHistoryBackfill).
		Scan(&stagedSessions, &stagedUsers); err != nil {
		t.Fatalf("read staging: %v", err)
	}
	if stagedSessions == 0 || stagedUsers == 0 {
		t.Fatalf("durable staging is incomplete: sessions=%d users=%d", stagedSessions, stagedUsers)
	}
	if _, err := s.pool.Exec(ctx, `TRUNCATE otel_events`); err != nil {
		t.Fatalf("simulate retained source loss: %v", err)
	}

	source, inferred, err := s.BackfillSessionRecordLoginEmailHistoryOnce(ctx)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if source != 3 || inferred != 1 {
		t.Fatalf("resumed totals = (%d, %d), want (3, 1)", source, inferred)
	}
	for i := 0; i < 3; i++ {
		uuid := fmt.Sprintf("observed-resume-%02d", i)
		if email, source := loginEmailSourceOf(t, s, uuid); email != "observed@example.com" || source != "otel" {
			t.Errorf("%s = (%q, %q), want staged observed attribution", uuid, email, source)
		}
	}
	if email, source := loginEmailSourceOf(t, s, "inferred-resume"); email != "inferred@example.com" || source != "inferred" {
		t.Errorf("inferred row = (%q, %q), want staged inference", email, source)
	}
}

// A derived refresh failure must roll back the source write and progress together.
// Otherwise resume skips a source row whose rollup/facts still carry the old value.
func TestLoginEmailHistoryRepairProgressIsAtomicWithDerivedRefresh(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	clearLoginEmailHistoryBackfillMarker(t, s)
	ctx := context.Background()
	seedLoginEmailHistoryBackfill(t, s, "atomic", 1)

	if _, err := s.pool.Exec(ctx, `CREATE OR REPLACE FUNCTION fail_login_email_history_derived()
		RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forced derived failure'; END $$`); err != nil {
		t.Fatalf("create failure function: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `CREATE TRIGGER fail_login_email_history_derived
		BEFORE INSERT ON session_overview_rollups FOR EACH ROW
		EXECUTE FUNCTION fail_login_email_history_derived()`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(ctx, `DROP TRIGGER IF EXISTS fail_login_email_history_derived ON session_overview_rollups`)
		_, _ = s.pool.Exec(ctx, `DROP FUNCTION IF EXISTS fail_login_email_history_derived()`)
	})
	if _, err := s.backfillSessionRecordLoginEmailHistoryBatch(ctx, 1); err == nil {
		t.Fatal("forced derived failure returned nil")
	}
	if email, source := loginEmailSourceOf(t, s, "observed-atomic-00"); email != "" || source != "" {
		t.Errorf("source write survived failed batch as (%q, %q)", email, source)
	}
	var cursor, updated int64
	if err := s.pool.QueryRow(ctx, `SELECT source_cursor, source_updated
		FROM login_email_history_repair_state WHERE name=$1`, loginEmailHistoryBackfill).Scan(&cursor, &updated); err != nil {
		t.Fatalf("read progress: %v", err)
	}
	if cursor != 0 || updated != 0 {
		t.Fatalf("progress advanced on failed batch to cursor=%d updated=%d", cursor, updated)
	}
	if _, err := s.pool.Exec(ctx, `DROP TRIGGER fail_login_email_history_derived ON session_overview_rollups`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `DROP FUNCTION fail_login_email_history_derived()`); err != nil {
		t.Fatalf("drop function: %v", err)
	}
	if _, _, err := s.BackfillSessionRecordLoginEmailHistoryOnce(ctx); err != nil {
		t.Fatalf("resume after atomic failure: %v", err)
	}
}

// The configured bound applies to committed source mutations, and no completion
// marker may appear merely because one bounded transaction succeeded.
func TestLoginEmailHistoryRepairHonorsBatchBound(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	clearLoginEmailHistoryBackfillMarker(t, s)
	ctx := context.Background()
	seedLoginEmailHistoryBackfill(t, s, "bound", 5)

	result, err := s.backfillSessionRecordLoginEmailHistoryBatch(ctx, 2)
	if err != nil {
		t.Fatalf("bounded batch: %v", err)
	}
	if result.SourceUpdated != 2 || result.Complete {
		t.Fatalf("bounded batch = %+v, want exactly two source updates", result)
	}
	var attributed int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM session_records WHERE login_email_source='otel'`).Scan(&attributed); err != nil {
		t.Fatalf("count attributed rows: %v", err)
	}
	if attributed != 2 {
		t.Fatalf("committed source rows = %d, want batch bound 2", attributed)
	}
	if historyRepairMarked(t, s) {
		t.Fatal("completion marker exists before remaining batches")
	}
}

// Once both phases are exhausted, marker insertion is still a transaction boundary.
// A failed marker must retain progress/staging and a retry must complete without
// repeating or losing already committed batches.
func TestLoginEmailHistoryRepairCompletionIsResumable(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	clearLoginEmailHistoryBackfillMarker(t, s)
	ctx := context.Background()
	seedLoginEmailHistoryBackfill(t, s, "complete", 1)

	// Source row, source exhaustion/phase transition, inference row.
	for i := 0; i < 3; i++ {
		result, err := s.backfillSessionRecordLoginEmailHistoryBatch(ctx, 1)
		if err != nil {
			t.Fatalf("pre-completion batch %d: %v", i, err)
		}
		if result.Complete {
			t.Fatalf("repair completed before inference exhaustion on batch %d", i)
		}
	}
	if _, err := s.pool.Exec(ctx, `CREATE OR REPLACE FUNCTION fail_login_email_history_marker()
		RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
			IF NEW.name = 'session_records_login_email_history_v1' THEN RAISE EXCEPTION 'forced marker failure'; END IF;
			RETURN NEW;
		END $$`); err != nil {
		t.Fatalf("create marker failure function: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `CREATE TRIGGER fail_login_email_history_marker
		BEFORE INSERT ON schema_backfills FOR EACH ROW EXECUTE FUNCTION fail_login_email_history_marker()`); err != nil {
		t.Fatalf("create marker failure trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(ctx, `DROP TRIGGER IF EXISTS fail_login_email_history_marker ON schema_backfills`)
		_, _ = s.pool.Exec(ctx, `DROP FUNCTION IF EXISTS fail_login_email_history_marker()`)
	})
	if _, err := s.backfillSessionRecordLoginEmailHistoryBatch(ctx, 1); err == nil {
		t.Fatal("forced marker failure returned nil")
	}
	if historyRepairMarked(t, s) {
		t.Fatal("marker exists after failed completion transaction")
	}
	var states, intervals int
	if err := s.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM login_email_history_repair_state WHERE name=$1),
		(SELECT count(*) FROM login_email_history_user_intervals WHERE repair_name=$1)`, loginEmailHistoryBackfill).
		Scan(&states, &intervals); err != nil {
		t.Fatalf("read resumable staging: %v", err)
	}
	if states != 1 || intervals == 0 {
		t.Fatalf("failed completion discarded staging: state=%d intervals=%d", states, intervals)
	}
	if _, err := s.pool.Exec(ctx, `DROP TRIGGER fail_login_email_history_marker ON schema_backfills`); err != nil {
		t.Fatalf("drop marker trigger: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `DROP FUNCTION fail_login_email_history_marker()`); err != nil {
		t.Fatalf("drop marker function: %v", err)
	}
	result, err := s.backfillSessionRecordLoginEmailHistoryBatch(ctx, 1)
	if err != nil {
		t.Fatalf("resume completion: %v", err)
	}
	if !result.Complete || result.SourceUpdated != 1 || result.InferenceUpdated != 1 {
		t.Fatalf("completion result = %+v, want complete totals (1,1)", result)
	}
	if !historyRepairMarked(t, s) {
		t.Fatal("completion marker missing")
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM login_email_history_repair_state WHERE name=$1`, loginEmailHistoryBackfill).Scan(&states); err != nil {
		t.Fatalf("read cleaned state: %v", err)
	}
	if states != 0 {
		t.Fatalf("repair state retained after completion: %d rows", states)
	}
}

func TestLoginEmailHistoryRepairCompletedSecondRunIsNoOp(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	clearLoginEmailHistoryBackfillMarker(t, s)
	ctx := context.Background()
	if _, _, err := s.BackfillSessionRecordLoginEmailHistoryOnce(ctx); err != nil {
		t.Fatalf("first empty repair: %v", err)
	}
	seedLoginEmailHistoryBackfill(t, s, "late", 1)
	source, inferred, err := s.BackfillSessionRecordLoginEmailHistoryOnce(ctx)
	if err != nil {
		t.Fatalf("second repair: %v", err)
	}
	if source != 0 || inferred != 0 {
		t.Fatalf("second repair totals = (%d,%d), want no-op", source, inferred)
	}
	if email, source := loginEmailSourceOf(t, s, "observed-late-00"); email != "" || source != "" {
		t.Fatalf("post-completion row changed to (%q,%q)", email, source)
	}
}

// The one-time repair carries its own literal copy of the inference predicate, so
// the codex guard added to inferMatch does nothing for it. A fresh database that
// runs the history repair before the periodic pass would reproduce #524's wrong
// attribution in full, on exactly the rows the repair exists to reach.
//
// Mutation: add the guard to inferMatch only and the codex row here comes back
// stamped with the user's Claude account.
func TestLoginEmailHistoryInferencePhaseSkipsCodex(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	clearLoginEmailHistoryBackfillMarker(t, s)
	ctx := context.Background()
	base := recentDay(38).Add(10 * time.Hour)

	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: base, EventName: "api_request", SessionID: "timeline-codex", UserID: "mixed-user", LoginEmail: "user-a@example.com"},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: base.Add(time.Minute), SessionID: "quiet-claude", RecordType: "user",
			UUID: "hist-claude", UserID: "mixed-user", Raw: json.RawMessage(`{}`)},
		{Ts: base.Add(time.Minute), SessionID: "quiet-codex", RecordType: "user",
			UUID: "hist-codex", UserID: "mixed-user", Agent: "codex", Raw: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	if _, inferred, err := s.BackfillSessionRecordLoginEmailHistoryOnce(ctx); err != nil {
		t.Fatalf("BackfillSessionRecordLoginEmailHistoryOnce: %v", err)
	} else if inferred != 1 {
		t.Fatalf("inference phase updated %d rows, want 1 (the claude row only)", inferred)
	}
	if email, source := loginEmailSourceOf(t, s, "hist-claude"); email != "user-a@example.com" || source != "inferred" {
		t.Errorf("claude row = (%q, %q), want (user-a@example.com, inferred)", email, source)
	}
	if email, source := loginEmailSourceOf(t, s, "hist-codex"); email != "" || source != "" {
		t.Errorf("codex row = (%q, %q), want unattributed", email, source)
	}
}
