package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestImputedRefreshQueuesOnlyChangedRows(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	t.Run("claude", func(t *testing.T) {
		truncateTables(t, s)
		ts := recentDay(10).Add(10 * time.Hour)
		in1, out1, in2, out2 := 1000, 100, 2000, 200
		recs := []*SessionRecord{
			{Ts: ts, SessionID: "dirty-claude", RecordType: "assistant", UUID: "dc1", Model: "claude-sonnet-5", InputTokens: &in1, OutputTokens: &out1, ProfileEmail: "p@example.com", Agent: "claude", Raw: json.RawMessage(`{"message":{"id":"m1"}}`)},
			{Ts: ts.Add(time.Minute), SessionID: "dirty-claude", RecordType: "assistant", UUID: "dc2", Model: "claude-sonnet-5", InputTokens: &in2, OutputTokens: &out2, ProfileEmail: "p@example.com", Agent: "claude", Raw: json.RawMessage(`{"message":{"id":"m2"}}`)},
		}
		if err := s.InsertSessionRecords(ctx, recs); err != nil {
			t.Fatal(err)
		}
		refresh := func() {
			t.Helper()
			if err := s.RefreshClaudeImputedCost(ctx); err != nil {
				t.Fatal(err)
			}
		}
		refresh()
		first := queueRows(t, s)["dirty-claude"]
		if first.generation == 0 {
			t.Fatalf("first refresh did not queue session: %+v", queueRows(t, s))
		}
		refresh()
		if got := queueRows(t, s)["dirty-claude"]; got.generation != first.generation {
			t.Fatalf("identical refresh generation = %d, want stable %d", got.generation, first.generation)
		}
		clearQueue(t, s)
		refresh()
		if got := queueRows(t, s); len(got) != 0 {
			t.Fatalf("identical refresh queued: %+v", got)
		}
		// Per-request costs move in opposite directions while the session total stays fixed.
		if _, err := s.pool.Exec(ctx, `UPDATE session_records SET input_tokens = CASE uuid WHEN 'dc1' THEN 1100 WHEN 'dc2' THEN 1900 END WHERE uuid IN ('dc1','dc2')`); err != nil {
			t.Fatal(err)
		}
		refresh()
		if len(queueRows(t, s)) != 1 {
			t.Fatalf("same-total request repricing did not queue: %+v", queueRows(t, s))
		}
		clearQueue(t, s)
		if _, err := s.pool.Exec(ctx, `UPDATE session_records SET profile_email='changed@example.com' WHERE uuid='dc1'`); err != nil {
			t.Fatal(err)
		}
		refresh()
		if len(queueRows(t, s)) != 1 {
			t.Fatalf("metadata change did not queue: %+v", queueRows(t, s))
		}
	})
	t.Run("codex", func(t *testing.T) {
		truncateTables(t, s)
		ts := recentDay(10).Add(10 * time.Hour)
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{codexUsageRecord("dirty-codex", "gpt-5.1-codex-max", ts, 1000, 100, 0)}); err != nil {
			t.Fatal(err)
		}
		refresh := func() {
			t.Helper()
			if err := s.RefreshCodexImputedCost(ctx); err != nil {
				t.Fatal(err)
			}
		}
		refresh()
		rows := queueRows(t, s)
		first, ok := rows["dirty-codex"]
		if !ok {
			t.Fatalf("first refresh did not queue: %+v", rows)
		}
		refresh()
		if got := queueRows(t, s)["dirty-codex"]; got.generation != first.generation {
			t.Fatalf("identical refresh generation = %d, want stable %d", got.generation, first.generation)
		}
		clearQueue(t, s)
		refresh()
		if got := queueRows(t, s); len(got) != 0 {
			t.Fatalf("identical refresh queued: %+v", got)
		}
		if _, err := s.pool.Exec(ctx, `UPDATE session_records SET account_id='account-changed' WHERE session_id='dirty-codex'`); err != nil {
			t.Fatal(err)
		}
		refresh()
		rows = queueRows(t, s)
		if _, ok := rows["dirty-codex"]; !ok {
			t.Fatalf("account metadata change did not queue: %+v (initial generation %d)", rows, first.generation)
		}
	})
}

func TestImputedRefreshQueuesBothSessionsWhenDerivedIdentityIsCorrected(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			truncateTables(t, s)
			id, stale := "current-"+agent, "stale-"+agent
			ts := recentDay(10).Add(10 * time.Hour)
			record := codexUsageRecord(id, "gpt-5.1-codex-max", ts, 1000, 100, 0)
			refresh := s.RefreshCodexImputedCost
			if agent == "claude" {
				in, out := 1000, 100
				record = &SessionRecord{Ts: ts, SessionID: id, RecordType: "assistant", UUID: "identity-correction", Model: "claude-sonnet-5", InputTokens: &in, OutputTokens: &out, Agent: "claude", Raw: json.RawMessage(`{"message":{"id":"identity-correction"}}`)}
				refresh = s.RefreshClaudeImputedCost
			}
			if err := s.InsertSessionRecords(ctx, []*SessionRecord{record}); err != nil {
				t.Fatal(err)
			}
			if err := refresh(ctx); err != nil {
				t.Fatal(err)
			}
			clearQueue(t, s)
			if _, err := s.pool.Exec(ctx, `UPDATE `+agent+`_imputed_cost SET session_id=$1 WHERE session_id=$2`, stale, id); err != nil {
				t.Fatal(err)
			}
			if err := refresh(ctx); err != nil {
				t.Fatal(err)
			}
			rows := queueRows(t, s)
			for _, want := range []string{stale, id} {
				if _, ok := rows[want]; !ok {
					t.Fatalf("corrected identity did not queue both old and new sessions: missing %q in %+v", want, rows)
				}
			}
		})
	}
}
