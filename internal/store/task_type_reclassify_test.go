package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"cctrace/internal/insights"
)

func storedLabel(t *testing.T, s *PgStore, uuid string) (taskType, version string) {
	t.Helper()
	if err := s.pool.QueryRow(context.Background(),
		`SELECT task_type, classifier_version FROM session_records WHERE uuid = $1`, uuid,
	).Scan(&taskType, &version); err != nil {
		t.Fatalf("read label for %s: %v", uuid, err)
	}
	return taskType, version
}

// Bumping the classifier version is how a rule change says the stored verdicts are
// stale. The backfill cannot serve that -- it selects rows with no verdict at all --
// so without this sweep a bump changed nothing for rows already labelled, and the
// chart mixed one week under the new rules with every earlier week under the old
// (#429).
func TestReclassifyRelabelsRowsFromAnOlderRuleSet(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 4, 0, 0, 0, time.UTC)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: now, SessionID: "stale", UUID: "old-verdict", RecordType: "user", ProfileEmail: "u@example.com",
			Raw: json.RawMessage(`{"message":{"role":"user","content":"구현 계획을 세워줘"}}`)},
	}); err != nil {
		t.Fatal(err)
	}
	// What a row labelled by the previous rule set looks like.
	if _, err := s.pool.Exec(ctx,
		`UPDATE session_records SET task_type = 'unknown', classifier_version = 'keyword-v0' WHERE uuid = 'old-verdict'`); err != nil {
		t.Fatal(err)
	}

	n, err := s.ReclassifyStaleTaskTypes(ctx)
	if err != nil {
		t.Fatalf("ReclassifyStaleTaskTypes: %v", err)
	}
	if n != 1 {
		t.Errorf("relabelled %d rows, want 1", n)
	}
	label, version := storedLabel(t, s, "old-verdict")
	if label != "planning" {
		t.Errorf("task_type = %q, want planning -- the old verdict survived a rule change", label)
	}
	if version != insights.ClassifierVersion {
		t.Errorf("classifier_version = %q, want %q", version, insights.ClassifierVersion)
	}
}

// A row nobody has classified belongs to the backfill. Taking it here would have
// the two passes race for the same rows under different locks, and would spend the
// sweep's budget on work the backfill is already bounded to do.
func TestReclassifyLeavesNeverClassifiedRowsToTheBackfill(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 4, 0, 0, 0, time.UTC)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: now, SessionID: "fresh", UUID: "never-seen", RecordType: "user", ProfileEmail: "u@example.com",
			Raw: json.RawMessage(`{"message":{"role":"user","content":"구현 계획을 세워줘"}}`)},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE session_records SET task_type = '', classifier_version = '' WHERE uuid = 'never-seen'`); err != nil {
		t.Fatal(err)
	}

	if n, err := s.ReclassifyStaleTaskTypes(ctx); err != nil || n != 0 {
		t.Fatalf("relabelled %d rows (err=%v); an unclassified row is the backfill's", n, err)
	}
	if label, _ := storedLabel(t, s, "never-seen"); label != "" {
		t.Errorf("task_type = %q, want empty", label)
	}
}

// The sweep walks ids and remembers where it stopped, so a finished walk costs one
// index lookup rather than rescanning the table. A row relabelled once must not be
// picked up again.
func TestReclassifyDoesNotRepeatFinishedWork(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 4, 0, 0, 0, time.UTC)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: now, SessionID: "once", UUID: "u1", RecordType: "user", ProfileEmail: "u@example.com",
			Raw: json.RawMessage(`{"message":{"role":"user","content":"구현 계획을 세워줘"}}`)},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE session_records SET task_type = 'unknown', classifier_version = 'keyword-v0'`); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.ReclassifyStaleTaskTypes(ctx); n != 1 {
		t.Fatalf("first pass relabelled %d, want 1", n)
	}
	for i := 0; i < 2; i++ {
		if n, err := s.ReclassifyStaleTaskTypes(ctx); err != nil || n != 0 {
			t.Errorf("pass %d relabelled %d (err=%v), want 0", i+2, n, err)
		}
	}
}

// The cursor remembers which rule set it walked for, so a later bump restarts the
// walk on its own. Without that, the first sweep after a release would finish, park
// the cursor at the end, and every bump after it would silently do nothing.
func TestReclassifyRestartsWhenTheRuleSetChangesAgain(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 4, 0, 0, 0, time.UTC)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: now, SessionID: "again", UUID: "v1", RecordType: "user", ProfileEmail: "u@example.com",
			Raw: json.RawMessage(`{"message":{"role":"user","content":"구현 계획을 세워줘"}}`)},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE session_records SET task_type = 'unknown', classifier_version = 'keyword-v0'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReclassifyStaleTaskTypes(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.ReclassifyStaleTaskTypes(ctx); n != 0 {
		t.Fatalf("walk did not settle: %d", n)
	}

	// A later rule change: the cursor is parked at the end, and the stored verdict
	// is stale again.
	if _, err := s.pool.Exec(ctx,
		`UPDATE session_records SET classifier_version = 'keyword-v0'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE task_type_reclassify_cursor SET version = 'keyword-v0' WHERE only_row`); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ReclassifyStaleTaskTypes(ctx); err != nil || n != 1 {
		t.Fatalf("relabelled %d (err=%v) after a rule change; the walk did not restart", n, err)
	}
}
