package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"
	"time"
)

// queued is what a test expects of a session's queue row. scanFrom nil is NULL.
type queued struct {
	reasons  int32
	scanFrom *int64
	removal  bool
	priority int16
}

// expectQueued compares the whole queue with want: every session named there,
// queued as said, and no other.
func expectQueued(t *testing.T, s *PgStore, want map[string]queued) {
	t.Helper()
	got := queueRows(t, s)
	var names []string
	for id := range want {
		names = append(names, id)
	}
	sort.Strings(names)
	for id, w := range want {
		g, ok := got[id]
		if !ok {
			t.Errorf("%s is not queued", id)
			continue
		}
		removal := g.removalGeneration != 0 && g.removalGeneration == g.generation
		if g.reasons != w.reasons || g.priority != w.priority || removal != w.removal ||
			scanFromString(g.scanFrom) != scanFromString(w.scanFrom) {
			t.Errorf("%s queued with reasons=%d priority=%d removal=%v scan_from_id=%s, want reasons=%d priority=%d removal=%v scan_from_id=%s",
				id, g.reasons, g.priority, removal, scanFromString(g.scanFrom),
				w.reasons, w.priority, w.removal, scanFromString(w.scanFrom))
		}
	}
	for id := range got {
		if _, ok := want[id]; !ok {
			t.Errorf("%s is queued too; want only %v", id, names)
		}
	}
}

func clearQueue(t *testing.T, s *PgStore) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(), `DELETE FROM session_fact_dirty`); err != nil {
		t.Fatalf("clear session_fact_dirty: %v", err)
	}
}

// seedQueued queues a session directly, as a writer would have before its facts
// were first computed, and returns the row's generation.
func seedQueued(t *testing.T, s *PgStore, sessionID string) int64 {
	t.Helper()
	var generation int64
	if err := s.pool.QueryRow(context.Background(), `INSERT INTO session_fact_dirty (session_id) VALUES ($1)
		RETURNING generation`, sessionID).Scan(&generation); err != nil {
		t.Fatalf("queue %s: %v", sessionID, err)
	}
	return generation
}

// guardQueueWriteLast refuses, for the rest of the test, a write to a derived
// table or a table of session analysis after the queue write of the same
// transaction: the queue rows are the last rows a transaction locks (p1b-design
// §5.4), after the rollups, plugin facts, task segments, copy markers and facts.
func guardQueueWriteLast(t *testing.T, s *PgStore) {
	t.Helper()
	ctx := context.Background()
	guarded := []string{"session_overview_rollups", "plugin_invocation_facts", "task_segment_facts",
		"session_record_copies", "session_timeline_revisions", "session_timeline_policy_epoch",
		"session_commit_facts", "session_task_facts", "session_analysis_facts", "session_turn_facts", "session_activity_segments"}
	stmts := []string{
		`CREATE OR REPLACE FUNCTION test_mark_queued() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM set_config('cctrace_test.queued', 'yes', true); RETURN NULL; END $$`,
		`CREATE TRIGGER test_mark_queued AFTER INSERT OR UPDATE ON session_fact_dirty
			FOR EACH STATEMENT EXECUTE FUNCTION test_mark_queued()`,
		`CREATE OR REPLACE FUNCTION test_refuse_after_queue() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF current_setting('cctrace_test.queued', true) = 'yes' THEN
				RAISE EXCEPTION '% on % after the queue write', TG_OP, TG_TABLE_NAME;
			END IF;
			RETURN NULL;
		END $$`,
	}
	for _, table := range guarded {
		stmts = append(stmts, `CREATE TRIGGER test_refuse_after_queue BEFORE INSERT OR UPDATE OR DELETE ON `+table+
			` FOR EACH STATEMENT EXECUTE FUNCTION test_refuse_after_queue()`)
	}
	for _, stmt := range stmts {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("install the queue-last guard: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(ctx, `DROP TRIGGER IF EXISTS test_mark_queued ON session_fact_dirty`)
		for _, table := range guarded {
			_, _ = s.pool.Exec(ctx, `DROP TRIGGER IF EXISTS test_refuse_after_queue ON `+table)
		}
		_, _ = s.pool.Exec(ctx, `DROP FUNCTION IF EXISTS test_mark_queued()`)
		_, _ = s.pool.Exec(ctx, `DROP FUNCTION IF EXISTS test_refuse_after_queue()`)
	})
}

// factsRow is what a test sets of a session's facts; the rest is what the worker
// writes for a session with nothing in it.
type factsRow struct {
	userIDs, profileEmails         []string
	firstRecordAt, firstActivityAt *time.Time
	costSource                     string
}

// seedFacts writes a session's facts directly -- the worker that computes them
// comes in a later pass -- one row in each of the three tables it fills.
func seedFacts(t *testing.T, s *PgStore, sessionID string, f factsRow) {
	t.Helper()
	ctx := context.Background()
	if f.userIDs == nil {
		f.userIDs = []string{}
	}
	if f.profileEmails == nil {
		f.profileEmails = []string{}
	}
	if f.costSource == "" {
		f.costSource = "none"
	}
	at := time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
	for _, stmt := range []struct {
		q    string
		args []any
	}{
		{`INSERT INTO session_analysis_facts (session_id, agent, coverage, user_ids, profile_emails,
			first_record_at, first_activity_at, transcript, title_source, title_state, turn_boundaries,
			cost_source, output_hash, source_generation, rules_version)
			VALUES ($1, 'claude', 'full', $2, $3, $4, $5, true, 'none', 'none', 'observed', $6, '', 1, 1)`,
			[]any{sessionID, f.userIDs, f.profileEmails, f.firstRecordAt, f.firstActivityAt, f.costSource}},
		{`INSERT INTO session_turn_facts (session_id, turn_key, agent, ordinal, branch, started_at, ended_at,
			input_kind, is_request, status, preview_state, cost_source, context_source, rules_version)
			VALUES ($1, 'turn-1', 'claude', 1, 'active', $2, $2, 'typed', true, 'ended', 'none', 'none', 'unknown', 1)`,
			[]any{sessionID, at}},
		{`INSERT INTO session_activity_segments (session_id, seg_start, seg_end, agent, records, human_inputs, rules_version)
			VALUES ($1, $2, $2, 'claude', 1, 1, 1)`, []any{sessionID, at}},
	} {
		if _, err := s.pool.Exec(ctx, stmt.q, stmt.args...); err != nil {
			t.Fatalf("seed facts of %s: %v", sessionID, err)
		}
	}
}

// minRecordID is the least session_records.id of a session.
func minRecordID(t *testing.T, s *PgStore, sessionID string) int64 {
	t.Helper()
	var id int64
	if err := s.pool.QueryRow(context.Background(),
		`SELECT min(id) FROM session_records WHERE session_id = $1`, sessionID).Scan(&id); err != nil {
		t.Fatalf("min id of %s: %v", sessionID, err)
	}
	return id
}

// Every entry point of p1b-design §5.2 -- a shared refresh through each of its
// callers -- queues the sessions it already refreshes the rollups of, or, where
// it refreshes none, the sessions whose rows it wrote, under the reason of the
// table: bit, priority, scan_from_id, removal. None compares values, so a call
// that changes nothing still queues what it reaches. And none writes a derived
// row after its queue write (guardQueueWriteLast).
//
// Mutation: drop any one hook and its subtest finds the session unqueued; queue
// before a derived refresh and the guard refuses the refresh.
func TestFactWritersMarkDirty(t *testing.T) {
	s := acquireTestStore(t)
	guardQueueWriteLast(t, s)
	ctx := context.Background()
	base := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	raw := json.RawMessage(`{}`)
	prompt := json.RawMessage(`{"message":{"role":"user","content":"구현 계획을 세워줘"}}`)
	sync := func(from *int64) queued { return queued{reasons: 1, scanFrom: from} }
	otel := queued{reasons: 4}
	snapshot := queued{reasons: 8}
	exclusion := queued{reasons: 16}
	identity := queued{reasons: 32, priority: 1}
	imputed := queued{reasons: 64, priority: 1}
	taskType := queued{reasons: 2048, priority: 1}

	t.Run("InsertSessionRecords", func(t *testing.T) {
		truncateTables(t, s)
		records := []*SessionRecord{
			{Ts: base, SessionID: "rec-a", RecordType: "user", UUID: "ra1", Raw: raw},
			{Ts: base.Add(time.Second), SessionID: "rec-a", RecordType: "assistant", UUID: "ra2", Raw: raw},
			{Ts: base, SessionID: "rec-b", RecordType: "user", UUID: "rb1", Raw: raw},
		}
		if err := s.InsertSessionRecords(ctx, records); err != nil {
			t.Fatal(err)
		}
		a, b := minRecordID(t, s, "rec-a"), minRecordID(t, s, "rec-b")
		expectQueued(t, s, map[string]queued{"rec-a": sync(&a), "rec-b": sync(&b)})

		// Sent again: nothing goes in, the sessions are queued all the same, with no
		// new row to scan for copies.
		clearQueue(t, s)
		if err := s.InsertSessionRecords(ctx, records); err != nil {
			t.Fatal(err)
		}
		expectQueued(t, s, map[string]queued{"rec-a": sync(nil), "rec-b": sync(nil)})

		// A new row of a queued session sets where the scan starts.
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{
			{Ts: base.Add(time.Minute), SessionID: "rec-a", RecordType: "user", UUID: "ra3", Raw: raw},
		}); err != nil {
			t.Fatal(err)
		}
		var newest int64
		if err := s.pool.QueryRow(ctx, `SELECT id FROM session_records WHERE uuid = 'ra3'`).Scan(&newest); err != nil {
			t.Fatal(err)
		}
		expectQueued(t, s, map[string]queued{"rec-a": sync(&newest), "rec-b": sync(nil)})
	})

	t.Run("ReenrichSessionRecords", func(t *testing.T) {
		truncateTables(t, s)
		records := []*SessionRecord{{Ts: base, SessionID: "re-a", RecordType: "user", UUID: "re1", Raw: raw}}
		if err := s.InsertSessionRecords(ctx, records); err != nil {
			t.Fatal(err)
		}
		clearQueue(t, s)
		// The same values again: rows matched, nothing changed.
		if _, err := s.ReenrichSessionRecords(ctx, records); err != nil {
			t.Fatal(err)
		}
		expectQueued(t, s, map[string]queued{"re-a": {reasons: 2, scanFrom: int64Ptr(0), priority: 1}})
	})

	t.Run("InsertEventsTx", func(t *testing.T) {
		truncateTables(t, s)
		if err := s.InsertEvents(ctx, []*OtelEvent{
			{Ts: base, EventName: "api_request", SessionID: "otel-a"},
			{Ts: base, EventName: "tool_result", SessionID: "otel-b"},
		}); err != nil {
			t.Fatal(err)
		}
		expectQueued(t, s, map[string]queued{"otel-a": otel, "otel-b": otel})
	})

	t.Run("ingestSessionObservations", func(t *testing.T) {
		truncateTables(t, s)
		ingestObservations(t, s, SessionObservationBatch{
			Snapshots:  []*SessionSnapshot{obsSnapshot("snap-a", "p@example.com", base, "x")},
			LiveStates: []*SessionLiveState{obsLiveState("snap-b", "p@example.com", base, "busy")},
			Events:     []*SessionObservationEvent{obsEvent("snap-b", "p@example.com", "end", base, `{}`)},
		})
		expectQueued(t, s, map[string]queued{"snap-a": snapshot})
		// The same snapshot again takes the row again: queued.
		clearQueue(t, s)
		ingestObservations(t, s, SessionObservationBatch{
			Snapshots: []*SessionSnapshot{obsSnapshot("snap-a", "p@example.com", base, "x")}})
		expectQueued(t, s, map[string]queued{"snap-a": snapshot})
		// An older one is refused and writes nothing: not queued.
		clearQueue(t, s)
		ingestObservations(t, s, SessionObservationBatch{
			Snapshots: []*SessionSnapshot{obsSnapshot("snap-a", "p@example.com", base.Add(-time.Hour), "y")}})
		expectQueued(t, s, map[string]queued{})
	})

	t.Run("BackfillSessionRecordLoginEmail", func(t *testing.T) {
		truncateTables(t, s)
		if err := s.InsertEvents(ctx, []*OtelEvent{
			{Ts: base, EventName: "api_request", SessionID: "bf-a", LoginEmail: "a@example.com"},
			{Ts: base, EventName: "api_request", SessionID: "bf-same", LoginEmail: "same@example.com"},
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{
			{Ts: base, SessionID: "bf-a", RecordType: "user", UUID: "bf1", Raw: raw},
			{Ts: base, SessionID: "bf-same", RecordType: "user", UUID: "bf2", LoginEmail: "same@example.com", Raw: raw},
		}); err != nil {
			t.Fatal(err)
		}
		// Already filled with the right address, by a guess: the backfill takes the
		// row back to an observation without changing the address.
		if _, err := s.pool.Exec(ctx, `UPDATE session_records SET login_email_source = 'inferred' WHERE uuid = 'bf2'`); err != nil {
			t.Fatal(err)
		}
		clearQueue(t, s)
		if _, err := s.BackfillSessionRecordLoginEmail(ctx, time.Time{}); err != nil {
			t.Fatal(err)
		}
		expectQueued(t, s, map[string]queued{"bf-a": identity, "bf-same": identity})
	})

	t.Run("InferSessionRecordLoginEmail", func(t *testing.T) {
		truncateTables(t, s)
		seedInference(t, s, inferSeed{
			user:   "u-infer",
			events: []inferEvent{{base, "bot@example.com"}, {base.Add(15 * time.Minute), "bot@example.com"}},
			recs:   []inferRec{{uuid: "inf1", at: base.Add(5 * time.Minute)}},
		})
		clearQueue(t, s)
		if n, err := s.InferSessionRecordLoginEmail(ctx, time.Time{}); err != nil || n != 1 {
			t.Fatalf("InferSessionRecordLoginEmail = (%d, %v), want one row", n, err)
		}
		expectQueued(t, s, map[string]queued{"u-infer-quiet": identity})
	})

	t.Run("runLoginEmailHistoryBatch", func(t *testing.T) {
		truncateTables(t, s)
		clearLoginEmailHistoryBackfillMarker(t, s)
		seedLoginEmailHistoryBackfill(t, s, "w", 1)
		clearQueue(t, s)
		if _, _, err := s.BackfillSessionRecordLoginEmailHistoryOnce(ctx); err != nil {
			t.Fatal(err)
		}
		expectQueued(t, s, map[string]queued{"observed-w-00": identity, "quiet-w": identity})
	})

	t.Run("FillCodexAccountFromQuota", func(t *testing.T) {
		truncateTables(t, s)
		seedCodexRecords(t, s, codexRec{uuid: "fc1", session: "fill-a"})
		seedCodexSamples(t, s, codexSample(0, "acct-a", "wedataopenai@example.com", "fill-a", AttributionObserved))
		clearQueue(t, s)
		if n, err := s.FillCodexAccountFromQuota(ctx, time.Time{}); err != nil || n != 1 {
			t.Fatalf("FillCodexAccountFromQuota = (%d, %v), want one row", n, err)
		}
		expectQueued(t, s, map[string]queued{"fill-a": identity})
	})

	t.Run("AttributeCodexLoginEmailFromQuota", func(t *testing.T) {
		truncateTables(t, s)
		seedCodexRecords(t, s, codexRec{uuid: "at1", session: "attr-a", account: "acct-a"})
		seedCodexSamples(t, s, codexSample(0, "acct-a", "wedataopenai@example.com", "", AttributionObserved))
		clearQueue(t, s)
		if n, err := s.AttributeCodexLoginEmailFromQuota(ctx, time.Time{}); err != nil || n != 1 {
			t.Fatalf("AttributeCodexLoginEmailFromQuota = (%d, %v), want one row", n, err)
		}
		expectQueued(t, s, map[string]queued{"attr-a": identity})
	})

	// The repair names no sessions; its completion queues every one with facts,
	// and every one already queued, whose first computation may be under way: the
	// new generation is what makes that computation's publish start over.
	t.Run("runCodexLoginEmailRepairBatch", func(t *testing.T) {
		truncateTables(t, s)
		clearCodexLoginEmailRepairMarker(t, s)
		seedCodexRecords(t, s, codexRec{uuid: "cr1", session: "repair-records-only"})
		seedFacts(t, s, "repair-facts", factsRow{})
		clearQueue(t, s)
		before := seedQueued(t, s, "repair-queued")
		if _, _, _, err := s.RepairCodexLoginEmailFromQuotaOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if !codexRepairMarked(t, s) {
			t.Fatal("the repair did not complete")
		}
		// The queued row keeps its live priority.
		expectQueued(t, s, map[string]queued{"repair-facts": identity, "repair-queued": {reasons: 32}})
		if g := queueRows(t, s)["repair-queued"].generation; g <= before {
			t.Errorf("repair-queued generation %d, was %d: want a new one", g, before)
		}
	})

	t.Run("applyExclusionChange", func(t *testing.T) {
		truncateTables(t, s)
		if err := s.InsertEvents(ctx, []*OtelEvent{
			{Ts: base, EventName: "api_request", SessionID: "ex-otel", LoginEmail: "ex@example.com"},
			{Ts: base, EventName: "api_request", SessionID: "ex-other", LoginEmail: "other@example.com"},
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{
			{Ts: base, SessionID: "ex-records", RecordType: "user", UUID: "ex1", LoginEmail: "ex@example.com", Raw: raw},
		}); err != nil {
			t.Fatal(err)
		}
		seedCodexRecords(t, s,
			codexRec{uuid: "bill1", session: "bill-admin", account: "acct-admin"},
			codexRec{uuid: "bill2", session: "bill-self", account: "acct-self"})

		for _, step := range []struct {
			name string
			run  func() error
			want []string
		}{
			{"ExcludeAccount", func() error { _, err := s.ExcludeAccount(ctx, "ex@example.com", "r", "admin"); return err },
				[]string{"ex-otel", "ex-records"}},
			// Excluding it again changes nothing, and still queues what it reaches.
			{"ExcludeAccount again", func() error { _, err := s.ExcludeAccount(ctx, "ex@example.com", "r", "admin"); return err },
				[]string{"ex-otel", "ex-records"}},
			{"RemoveExcludedAccount", func() error { return s.RemoveExcludedAccount(ctx, "ex@example.com") },
				[]string{"ex-otel", "ex-records"}},
			{"ExcludeBillingAccount", func() error {
				_, _, err := s.ExcludeBillingAccount(ctx, "openai", "acct-admin", "r", "admin")
				return err
			}, []string{"bill-admin"}},
			{"RemoveExcludedBillingAccount", func() error { return s.RemoveExcludedBillingAccount(ctx, "openai", "acct-admin") },
				[]string{"bill-admin"}},
			{"SelfExcludeBillingAccount", func() error {
				return s.SelfExcludeBillingAccount(ctx, "openai", "acct-self", "r", "owner@example.com")
			}, []string{"bill-self"}},
			{"RemoveSelfExcludedBillingAccount", func() error {
				removed, err := s.RemoveSelfExcludedBillingAccount(ctx, "openai", "acct-self", "owner@example.com")
				if err == nil && !removed {
					err = errors.New("not removed")
				}
				return err
			}, []string{"bill-self"}},
		} {
			clearQueue(t, s)
			if err := step.run(); err != nil {
				t.Fatalf("%s: %v", step.name, err)
			}
			want := map[string]queued{}
			for _, id := range step.want {
				want[id] = exclusion
			}
			t.Logf("after %s", step.name)
			expectQueued(t, s, want)
		}
	})

	// A quota reading links an excluded address to a billing account after the
	// address was excluded: the account's sessions are hidden now, and queued.
	t.Run("RefreshExcludedBillingLinks", func(t *testing.T) {
		truncateTables(t, s)
		seedCodexRecords(t, s, codexRec{uuid: "lk1", session: "link-a", account: "acct-link"})
		if _, err := s.ExcludeAccount(ctx, "link@example.com", "r", "admin"); err != nil {
			t.Fatal(err)
		}
		seedCodexSamples(t, s, codexSample(0, "acct-link", "link@example.com", "", AttributionObserved))
		clearQueue(t, s)
		if changed, err := s.RefreshExcludedBillingLinks(ctx); err != nil || !changed {
			t.Fatalf("RefreshExcludedBillingLinks = (%v, %v), want a change", changed, err)
		}
		expectQueued(t, s, map[string]queued{"link-a": exclusion})
	})

	// A full recompute cannot say what changed: every session with facts, and
	// every session already queued.
	t.Run("RecomputeExcludedSessions", func(t *testing.T) {
		truncateTables(t, s)
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{
			{Ts: base, SessionID: "full-records-only", RecordType: "user", UUID: "fu1", Raw: raw},
		}); err != nil {
			t.Fatal(err)
		}
		seedFacts(t, s, "full-facts", factsRow{})
		clearQueue(t, s)
		before := seedQueued(t, s, "full-queued")
		if err := s.RecomputeExcludedSessions(ctx); err != nil {
			t.Fatal(err)
		}
		expectQueued(t, s, map[string]queued{"full-facts": exclusion, "full-queued": exclusion})
		if g := queueRows(t, s)["full-queued"].generation; g <= before {
			t.Errorf("full-queued generation %d, was %d: want a new one", g, before)
		}
	})

	t.Run("RefreshExcludedSessionsIncremental", func(t *testing.T) {
		truncateTables(t, s)
		if _, err := s.ExcludeAccount(ctx, "late@example.com", "r", "admin"); err != nil {
			t.Fatal(err)
		}
		if err := s.InsertEvents(ctx, []*OtelEvent{
			{Ts: base, EventName: "api_request", SessionID: "late-a", LoginEmail: "late@example.com"},
		}); err != nil {
			t.Fatal(err)
		}
		clearQueue(t, s)
		if err := s.RefreshExcludedSessionsIncremental(ctx); err != nil {
			t.Fatal(err)
		}
		expectQueued(t, s, map[string]queued{"late-a": exclusion})
	})

	t.Run("RefreshClaudeImputedCost", func(t *testing.T) {
		truncateTables(t, s)
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{
			{Ts: base, SessionID: "imp-claude", RecordType: "assistant", UUID: "ic1", Model: "claude-sonnet-5",
				InputTokens: ptrInt(10), OutputTokens: ptrInt(5), Raw: json.RawMessage(`{"message":{"id":"msg-ic1"}}`)},
		}); err != nil {
			t.Fatal(err)
		}
		for i, pass := range []string{"first", "second, nothing to change"} {
			clearQueue(t, s)
			if err := s.RefreshClaudeImputedCost(ctx); err != nil {
				t.Fatalf("%s: %v", pass, err)
			}
			t.Logf("after the %s pass", pass)
			if i == 0 {
				expectQueued(t, s, map[string]queued{"imp-claude": imputed})
			} else {
				expectQueued(t, s, map[string]queued{})
			}
		}
	})

	t.Run("RefreshCodexImputedCost", func(t *testing.T) {
		truncateTables(t, s)
		seedCodexRecords(t, s, codexRec{uuid: "cx1", session: "imp-codex", recordType: "usage"})
		clearQueue(t, s)
		if err := s.RefreshCodexImputedCost(ctx); err != nil {
			t.Fatal(err)
		}
		expectQueued(t, s, map[string]queued{"imp-codex": imputed})

		seedCodexRecords(t, s, codexRec{uuid: "cx2", session: "imp-codex-late", recordType: "usage",
			at: codexQuotaBase.Add(time.Hour)})
		clearQueue(t, s)
		if _, err := s.RefreshCodexImputedCostIncremental(ctx); err != nil {
			t.Fatal(err)
		}
		expectQueued(t, s, map[string]queued{"imp-codex-late": imputed})
	})

	t.Run("BackfillLoginEmailSource", func(t *testing.T) {
		truncateTables(t, s)
		clearLoginEmailSourceMarker(t, s)
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{
			{Ts: base, SessionID: "src-a", RecordType: "user", UUID: "src1", LoginEmail: "one@example.com", Raw: raw},
			{Ts: base, SessionID: "src-blank", RecordType: "user", UUID: "src2", Raw: raw},
		}); err != nil {
			t.Fatal(err)
		}
		clearQueue(t, s)
		if err := s.BackfillLoginEmailSource(ctx); err != nil {
			t.Fatal(err)
		}
		expectQueued(t, s, map[string]queued{"src-a": identity})
	})

	t.Run("BackfillTaskTypes", func(t *testing.T) {
		truncateTables(t, s)
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{
			{Ts: base, SessionID: "tt-a", RecordType: "user", UUID: "tt1", Raw: prompt},
		}); err != nil {
			t.Fatal(err)
		}
		// A prompt stored before the classifier ran on ingest.
		if _, err := s.pool.Exec(ctx, `UPDATE session_records SET task_type = '', classifier_version = '' WHERE uuid = 'tt1'`); err != nil {
			t.Fatal(err)
		}
		clearQueue(t, s)
		if n, err := s.BackfillTaskTypes(ctx, 10); err != nil || n != 1 {
			t.Fatalf("BackfillTaskTypes = (%d, %v), want one row", n, err)
		}
		expectQueued(t, s, map[string]queued{"tt-a": taskType})
	})

	t.Run("ReclassifyStaleTaskTypes", func(t *testing.T) {
		truncateTables(t, s)
		if _, err := s.pool.Exec(ctx, `DELETE FROM task_type_reclassify_cursor`); err != nil {
			t.Fatal(err)
		}
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{
			{Ts: base, SessionID: "tt-b", RecordType: "user", UUID: "tt2", Raw: prompt},
		}); err != nil {
			t.Fatal(err)
		}
		// A verdict of an older rule set.
		if _, err := s.pool.Exec(ctx, `UPDATE session_records SET task_type = 'unknown', classifier_version = 'keyword-v0' WHERE uuid = 'tt2'`); err != nil {
			t.Fatal(err)
		}
		clearQueue(t, s)
		if n, err := s.ReclassifyStaleTaskTypes(ctx); err != nil || n != 1 {
			t.Fatalf("ReclassifyStaleTaskTypes = (%d, %v), want one row", n, err)
		}
		expectQueued(t, s, map[string]queued{"tt-b": taskType})
	})

	t.Run("moveHash", func(t *testing.T) {
		truncateTables(t, s)
		var marked bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_backfills WHERE name = $1)`, backfillName).Scan(&marked); err != nil {
			t.Fatal(err)
		}
		if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, backfillName); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			q := `DELETE FROM schema_backfills WHERE name = $1`
			if marked {
				q = `INSERT INTO schema_backfills (name) VALUES ($1) ON CONFLICT DO NOTHING`
			}
			if _, err := s.pool.Exec(ctx, q, backfillName); err != nil {
				t.Errorf("restore %s: %v", backfillName, err)
			}
		})
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{
			{Ts: base, SessionID: "ph-a", RecordType: "user", UUID: "ph1", ProjectHash: "users-p1b2", Raw: raw},
			{Ts: base, SessionID: "ph-kept", RecordType: "user", UUID: "ph2", ProjectHash: "-users-kept", Raw: raw},
		}); err != nil {
			t.Fatal(err)
		}
		clearQueue(t, s)
		if err := s.BackfillCanonicalProjectHash(ctx); err != nil {
			t.Fatal(err)
		}
		if n := countWhere(t, s, "session_records", "project_hash = '-users-p1b2'"); n != 1 {
			t.Fatalf("%d row(s) moved to the canonical hash, want 1", n)
		}
		expectQueued(t, s, map[string]queued{"ph-a": {reasons: 16384, priority: 1}})
	})
}

// Every writer that took the source lock in this pass holds it at its first row
// write: a trigger on the table it writes first refuses the write when the
// transaction does not hold it. The snapshot prune and the orphan cleanup also
// hold the retention lock exclusively by then (p1b-design §5.4).
//
// Mutation: drop any one of the new locks and its case fails at the trigger.
func TestFactWritersTakeTheSourceLockBeforeWriting(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	raw := json.RawMessage(`{}`)
	prompt := json.RawMessage(`{"message":{"role":"user","content":"구현 계획을 세워줘"}}`)
	const sourceHeld = `EXISTS (SELECT 1 FROM pg_locks WHERE pid = pg_backend_pid() AND locktype = 'advisory'
		AND classid = hashtext('session_overview_source')::oid AND objid = 0 AND objsubid = 2
		AND mode IN ('ShareLock', 'ExclusiveLock') AND granted)`
	const retentionHeld = `EXISTS (SELECT 1 FROM pg_locks WHERE pid = pg_backend_pid() AND locktype = 'advisory'
		AND objsubid = 1 AND mode = 'ExclusiveLock' AND granted
		AND ((classid::bigint << 32) | objid::bigint) = hashtext('session_overview_retention')::bigint)`
	guard := func(t *testing.T, table, operation, required string) {
		t.Helper()
		for _, stmt := range []string{
			`CREATE OR REPLACE FUNCTION test_require_locks() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
				IF NOT (` + required + `) THEN
					RAISE EXCEPTION 'locks missing before % on %', TG_OP, TG_TABLE_NAME;
				END IF;
				RETURN NULL;
			END $$`,
			`CREATE TRIGGER test_require_locks BEFORE ` + operation + ` ON ` + table +
				` FOR EACH STATEMENT EXECUTE FUNCTION test_require_locks()`,
		} {
			if _, err := s.pool.Exec(ctx, stmt); err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() {
			_, _ = s.pool.Exec(ctx, `DROP TRIGGER IF EXISTS test_require_locks ON `+table)
			_, _ = s.pool.Exec(ctx, `DROP FUNCTION IF EXISTS test_require_locks()`)
		})
	}
	seedRecords := func(t *testing.T, records ...*SessionRecord) {
		t.Helper()
		if err := s.InsertSessionRecords(ctx, records); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("applyExclusionChange", func(t *testing.T) {
		truncateTables(t, s)
		guard(t, "excluded_accounts", "INSERT OR DELETE", sourceHeld)
		if _, err := s.ExcludeAccount(ctx, "lock@example.com", "r", "admin"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("RefreshExcludedSessionsIncremental", func(t *testing.T) {
		truncateTables(t, s)
		if _, err := s.ExcludeAccount(ctx, "lock@example.com", "r", "admin"); err != nil {
			t.Fatal(err)
		}
		if err := s.InsertEvents(ctx, []*OtelEvent{{Ts: base, EventName: "api_request", SessionID: "lock-a", LoginEmail: "lock@example.com"}}); err != nil {
			t.Fatal(err)
		}
		guard(t, "excluded_sessions", "INSERT", sourceHeld)
		if err := s.RefreshExcludedSessionsIncremental(ctx); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("BackfillLoginEmailSource", func(t *testing.T) {
		truncateTables(t, s)
		clearLoginEmailSourceMarker(t, s)
		seedRecords(t, &SessionRecord{Ts: base, SessionID: "lock-b", RecordType: "user", UUID: "lk1", LoginEmail: "one@example.com", Raw: raw})
		guard(t, "session_records", "UPDATE", sourceHeld)
		if err := s.BackfillLoginEmailSource(ctx); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("BackfillTaskTypes", func(t *testing.T) {
		truncateTables(t, s)
		seedRecords(t, &SessionRecord{Ts: base, SessionID: "lock-c", RecordType: "user", UUID: "lk2", Raw: prompt})
		if _, err := s.pool.Exec(ctx, `UPDATE session_records SET task_type = '' WHERE uuid = 'lk2'`); err != nil {
			t.Fatal(err)
		}
		guard(t, "session_records", "UPDATE", sourceHeld)
		if n, err := s.BackfillTaskTypes(ctx, 10); err != nil || n != 1 {
			t.Fatalf("BackfillTaskTypes = (%d, %v)", n, err)
		}
	})
	t.Run("ReclassifyStaleTaskTypes", func(t *testing.T) {
		truncateTables(t, s)
		if _, err := s.pool.Exec(ctx, `DELETE FROM task_type_reclassify_cursor`); err != nil {
			t.Fatal(err)
		}
		seedRecords(t, &SessionRecord{Ts: base, SessionID: "lock-d", RecordType: "user", UUID: "lk3", Raw: prompt})
		if _, err := s.pool.Exec(ctx, `UPDATE session_records SET task_type = 'unknown', classifier_version = 'keyword-v0' WHERE uuid = 'lk3'`); err != nil {
			t.Fatal(err)
		}
		guard(t, "session_records", "UPDATE", sourceHeld)
		if n, err := s.ReclassifyStaleTaskTypes(ctx); err != nil || n != 1 {
			t.Fatalf("ReclassifyStaleTaskTypes = (%d, %v)", n, err)
		}
	})
	t.Run("moveHash", func(t *testing.T) {
		truncateTables(t, s)
		seedRecords(t, &SessionRecord{Ts: base, SessionID: "lock-e", RecordType: "user", UUID: "lk4", ProjectHash: "users-lock", Raw: raw})
		guard(t, "session_records", "UPDATE", sourceHeld)
		if _, err := s.moveHash(ctx, "session_records", "users-lock", "-users-lock"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("PruneSessionObservations", func(t *testing.T) {
		truncateTables(t, s)
		t.Cleanup(func() { forceRetention(t, s, "session_records", nil) })
		forceRetention(t, s, "session_records", ptrInt(7))
		ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{
			obsSnapshot("lock-f", "p@example.com", time.Now().Add(-10*24*time.Hour), "x")}})
		guard(t, "session_snapshots", "DELETE", sourceHeld+" AND "+retentionHeld)
		if _, err := s.PruneSessionObservations(ctx, time.Now().Add(-SessionObservationEventRetention)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("CleanupOrphanSessionRecords", func(t *testing.T) {
		truncateTables(t, s)
		seedRecords(t, &SessionRecord{Ts: base, SessionID: "lock-g", RecordType: "user", UUID: "lk5", Raw: raw})
		guard(t, "session_records", "DELETE", sourceHeld+" AND "+retentionHeld)
		if n, err := s.CleanupOrphanSessionRecords(ctx); err != nil || n != 1 {
			t.Fatalf("CleanupOrphanSessionRecords = (%d, %v)", n, err)
		}
	})
}

// A writer that now takes the source lock shared takes it before any row lock,
// so it cannot hold a row DeleteUserData is about to delete while it waits for the
// lock DeleteUserData holds exclusively. DeleteUserData is stopped inside its
// delete of session_records, holding the lock, while BackfillTaskTypes starts on
// the same user's unlabelled prompts; released, both finish.
//
// Mutation: take BackfillTaskTypes' source lock after its candidates are locked
// FOR UPDATE and it holds the prompts DeleteUserData has yet to delete while it
// waits for the lock: deadlock_detected.
func TestFactWriterSourceLockDoesNotDeadlockDeleteUserData(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	prompt := json.RawMessage(`{"message":{"role":"user","content":"구현 계획을 세워줘"}}`)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: base, SessionID: "victim-a", RecordType: "user", UUID: "v1", ProfileEmail: "victim@example.com", Raw: prompt},
		{Ts: base.Add(time.Second), SessionID: "victim-a", RecordType: "user", UUID: "v2", ProfileEmail: "victim@example.com", Raw: prompt},
		{Ts: base.Add(2 * time.Second), SessionID: "victim-a", RecordType: "user", UUID: "v3", ProfileEmail: "victim@example.com", Raw: prompt},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_records SET task_type = '', classifier_version = ''`); err != nil {
		t.Fatal(err)
	}
	const key = 7319002
	for _, stmt := range []string{
		`CREATE OR REPLACE FUNCTION test_delete_barrier() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF OLD.profile_email = 'victim@example.com' THEN
				PERFORM pg_advisory_xact_lock_shared(7319002);
			END IF;
			RETURN OLD;
		END $$`,
		`CREATE TRIGGER test_delete_barrier BEFORE DELETE ON session_records
			FOR EACH ROW EXECUTE FUNCTION test_delete_barrier()`,
	} {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(ctx, `DROP TRIGGER IF EXISTS test_delete_barrier ON session_records`)
		_, _ = s.pool.Exec(ctx, `DROP FUNCTION IF EXISTS test_delete_barrier()`)
	})
	holder, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_lock($1)`, key); err != nil {
		t.Fatal(err)
	}
	released := false
	release := func() {
		if !released {
			released = true
			_, _ = holder.Exec(ctx, `SELECT pg_advisory_unlock($1)`, key)
		}
	}
	defer release()

	waiting := func(what, lock string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			var n int
			if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted AND `+lock).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n > 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s never blocked", what)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	deleted := make(chan error, 1)
	go func() { deleted <- s.DeleteUserData(ctx, "victim@example.com", "") }()
	waiting("DeleteUserData at the barrier", `objid = 7319002`)
	labelled := make(chan error, 1)
	go func() { _, err := s.BackfillTaskTypes(ctx, 10); labelled <- err }()
	waiting("BackfillTaskTypes on the source lock",
		`classid = hashtext('session_overview_source')::oid AND objid = 0 AND objsubid = 2`)

	release()
	for _, w := range []struct {
		name string
		done chan error
	}{{"DeleteUserData", deleted}, {"BackfillTaskTypes", labelled}} {
		select {
		case err := <-w.done:
			var pgErr interface{ SQLState() string }
			if errors.As(err, &pgErr) && pgErr.SQLState() == "40P01" {
				t.Errorf("%s: deadlock: %v", w.name, err)
			} else if err != nil {
				t.Errorf("%s: %v", w.name, err)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("%s did not finish", w.name)
		}
	}
	if n := countWhere(t, s, "session_records", "profile_email = 'victim@example.com'"); n != 0 {
		t.Errorf("%d of the user's rows survived DeleteUserData", n)
	}
}

// P1t step 3 rewrites claude_imputed_cost, indexes it and writes its marker in
// one transaction, and queues the sessions it reached last, after the index and
// the marker: queue rows held while the index waits for its SHARE lock could
// close a cycle with the retention reconciliation, which holds ROW EXCLUSIVE on
// the table while it queues sessions of its own. A trigger refuses a queue write
// the marker does not yet precede.
//
// Mutation: queue inside refreshClaudeImputedCost, before the index and the
// marker, and the step fails at the trigger.
func Test_P1tStep3_QueuesAfterItsIndexAndMarker(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetRequestSetMigration(t, s)
	ctx := context.Background()
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC), SessionID: "step3-claude", RecordType: "assistant",
			UUID: "s3", Model: "claude-sonnet-5", InputTokens: ptrInt(10), OutputTokens: ptrInt(5),
			Raw: json.RawMessage(`{"message":{"id":"msg-s3"}}`)},
	}); err != nil {
		t.Fatal(err)
	}
	clearQueue(t, s)
	for _, stmt := range []string{
		`CREATE OR REPLACE FUNCTION test_queue_after_marker() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NOT EXISTS (SELECT 1 FROM schema_backfills WHERE name = 'claude_imputed_requests_v1') THEN
				RAISE EXCEPTION 'queue write before the step 3 marker';
			END IF;
			RETURN NULL;
		END $$`,
		`CREATE TRIGGER test_queue_after_marker BEFORE INSERT ON session_fact_dirty
			FOR EACH STATEMENT EXECUTE FUNCTION test_queue_after_marker()`,
	} {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(ctx, `DROP TRIGGER IF EXISTS test_queue_after_marker ON session_fact_dirty`)
		_, _ = s.pool.Exec(ctx, `DROP FUNCTION IF EXISTS test_queue_after_marker()`)
	})
	if progressed, err := s.rewriteClaudeImputedCostStep(ctx); err != nil || !progressed {
		t.Fatalf("step 3 = (%v, %v), want progress", progressed, err)
	}
	expectQueued(t, s, map[string]queued{"step3-claude": {reasons: 64, priority: 1}})
}

// A writer's queue write is part of its transaction: when it fails, what the
// writer wrote is not committed either, so no write lands without its queue row.
//
// Mutation: commit InsertSessionRecords before its queue write and the records
// stay though the queue write failed.
func TestFactWritersQueueInTheirOwnTransaction(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	for _, w := range []struct {
		name string
		seed func(t *testing.T)
		run  func() error
	}{
		{"InsertSessionRecords", nil, func() error {
			return s.InsertSessionRecords(ctx, []*SessionRecord{
				{Ts: at, SessionID: "tx-records", RecordType: "user", UUID: "tx1", Raw: json.RawMessage(`{}`)}})
		}},
		{"InsertEventsTx", nil, func() error {
			return s.InsertEvents(ctx, []*OtelEvent{{Ts: at, EventName: "api_request", SessionID: "tx-otel"}})
		}},
		{"ingestSessionObservations", nil, func() error {
			_, err := s.IngestSessionObservations(ctx, SessionObservationBatch{
				Snapshots: []*SessionSnapshot{obsSnapshot("tx-snap", "p@example.com", at, "x")}})
			return err
		}},
		{"applyExclusionChange", func(t *testing.T) {
			if err := s.InsertEvents(ctx, []*OtelEvent{
				{Ts: at, EventName: "api_request", SessionID: "tx-excluded", LoginEmail: "tx@example.com"}}); err != nil {
				t.Fatal(err)
			}
		}, func() error { _, err := s.ExcludeAccount(ctx, "tx@example.com", "r", "admin"); return err }},
	} {
		t.Run(w.name, func(t *testing.T) {
			truncateTables(t, s)
			if w.seed != nil {
				w.seed(t)
			}
			rollsBackWhole(t, s, w.run)
		})
	}
}
