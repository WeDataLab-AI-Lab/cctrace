package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestPgStore_ListLoginAccounts_matchesUnifiedEventArms(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Now().UTC().Truncate(time.Millisecond)

	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: ts, EventName: "api_request", SessionID: "otel-1", UserID: "wanted-user", LoginEmail: "otel@example.com"},
		{Ts: ts.Add(time.Millisecond), EventName: "api_request", SessionID: "otel-2", UserID: "wanted-user", LoginEmail: "otel@example.com"},
		{Ts: ts.Add(2 * time.Millisecond), EventName: "api_request", SessionID: "otel-blank", UserID: "wanted-user"},
		{Ts: ts.Add(3 * time.Millisecond), EventName: "api_request", SessionID: "excluded", UserID: "wanted-user", LoginEmail: "hidden@example.com"},
		{Ts: ts.Add(4 * time.Millisecond), EventName: "api_request", SessionID: "other", UserID: "other-user", LoginEmail: "other@example.com"},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	for _, table := range []string{"codex_imputed_cost", "claude_imputed_cost"} {
		if _, err := s.pool.Exec(ctx, `INSERT INTO `+table+`
			(srec_id, ts, user_id, login_email) VALUES
			($1, $2, 'wanted-user', $3), ($4, $5, 'wanted-user', $3), ($6, $7, 'wanted-user', '')`,
			100, ts, strings.TrimSuffix(table, "_imputed_cost")+"@example.com",
			101, ts.Add(time.Millisecond), 102, ts.Add(2*time.Millisecond)); err != nil {
			t.Fatalf("seed %s: %v", table, err)
		}
	}

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "gjc-1", RecordType: "assistant", UserID: "wanted-user", LoginEmail: "gjc@example.com", Agent: "gjc", UUID: "gjc-1", Raw: []byte(`{}`)},
		{Ts: ts.Add(time.Millisecond), SessionID: "gjc-2", RecordType: "assistant", UserID: "wanted-user", LoginEmail: "gjc@example.com", Agent: "gjc", UUID: "gjc-2", Raw: []byte(`{}`)},
		{Ts: ts.Add(2 * time.Millisecond), SessionID: "gjc-blank", RecordType: "assistant", UserID: "wanted-user", Agent: "gjc", UUID: "gjc-blank", Raw: []byte(`{}`)},
		{Ts: ts, SessionID: "omo-1", RecordType: "assistant", UserID: "wanted-user", LoginEmail: "omo@example.com", Agent: "omo", UUID: "omo-1", Raw: []byte(`{}`)},
		{Ts: ts.Add(time.Millisecond), SessionID: "omo-2", RecordType: "assistant", UserID: "wanted-user", LoginEmail: "omo@example.com", Agent: "omo", UUID: "omo-2", Raw: []byte(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO excluded_accounts (login_email) VALUES ('Hidden@Example.COM')`); err != nil {
		t.Fatalf("insert excluded account: %v", err)
	}

	all, err := s.ListLoginAccounts(ctx, "")
	if err != nil {
		t.Fatalf("ListLoginAccounts(all): %v", err)
	}
	wantAll := []string{"claude@example.com", "codex@example.com", "gjc@example.com", "omo@example.com", "otel@example.com", "other@example.com"}
	assertStringsEqual(t, all, wantAll)

	restricted, err := s.ListLoginAccounts(ctx, "wanted-user")
	if err != nil {
		t.Fatalf("ListLoginAccounts(restricted): %v", err)
	}
	wantRestricted := []string{"claude@example.com", "codex@example.com", "gjc@example.com", "omo@example.com", "otel@example.com"}
	assertStringsEqual(t, restricted, wantRestricted)
}

func TestPgStore_LatestActivityTs_readsEveryUnifiedEventArm(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	ts := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)

	arms := []struct {
		name      string
		sessionID string
		agent     string
		seed      func(*testing.T, *PgStore, time.Time)
	}{
		{name: "otel", sessionID: "otel", agent: "claude", seed: func(t *testing.T, s *PgStore, ts time.Time) {
			if err := s.InsertEvent(ctx, &OtelEvent{Ts: ts, EventName: "api_request", SessionID: "otel", Agent: "claude", LoginEmail: "arm@example.com"}); err != nil {
				t.Fatalf("InsertEvent: %v", err)
			}
		}},
		{name: "codex imputed", sessionID: "codex", agent: "codex", seed: func(t *testing.T, s *PgStore, ts time.Time) {
			if _, err := s.pool.Exec(ctx, `INSERT INTO codex_imputed_cost (srec_id, session_id, ts, agent, login_email) VALUES (201, 'codex', $1, 'codex', 'arm@example.com')`, ts); err != nil {
				t.Fatalf("seed codex: %v", err)
			}
		}},
		{name: "claude imputed", sessionID: "claude", agent: "claude", seed: func(t *testing.T, s *PgStore, ts time.Time) {
			if _, err := s.pool.Exec(ctx, `INSERT INTO claude_imputed_cost (srec_id, session_id, ts, agent, login_email) VALUES (202, 'claude', $1, 'claude', 'arm@example.com')`, ts); err != nil {
				t.Fatalf("seed claude: %v", err)
			}
		}},
		{name: "gjc assistant", sessionID: "gjc", agent: "gjc", seed: func(t *testing.T, s *PgStore, ts time.Time) {
			if err := s.InsertSessionRecords(ctx, []*SessionRecord{{Ts: ts, SessionID: "gjc", RecordType: "assistant", LoginEmail: "arm@example.com", Agent: "gjc", UUID: "gjc", Raw: []byte(`{}`)}}); err != nil {
				t.Fatalf("seed gjc: %v", err)
			}
		}},
		{name: "omo assistant", sessionID: "omo", agent: "omo", seed: func(t *testing.T, s *PgStore, ts time.Time) {
			if err := s.InsertSessionRecords(ctx, []*SessionRecord{{Ts: ts, SessionID: "omo", RecordType: "assistant", LoginEmail: "arm@example.com", Agent: "omo", UUID: "omo", Raw: []byte(`{}`)}}); err != nil {
				t.Fatalf("seed omo: %v", err)
			}
		}},
	}

	for _, arm := range arms {
		t.Run(arm.name, func(t *testing.T) {
			truncateTables(t, s)
			arm.seed(t, s, ts)
			got, ok, err := s.LatestActivityTs(ctx, EventFilter{LoginEmail: "arm@example.com"})
			if err != nil {
				t.Fatalf("LatestActivityTs: %v", err)
			}
			if !ok || !got.Equal(ts) {
				t.Fatalf("LatestActivityTs = (%s, %v), want (%s, true)", got, ok, ts)
			}

			seedScope := func(agent string) {
				if err := s.InsertSessionRecords(ctx, []*SessionRecord{{Ts: ts.Add(-time.Minute), SessionID: arm.sessionID,
					Agent: agent, ProjectHash: "project-selected", RecordType: "user", UUID: arm.sessionID + "-scope", Raw: []byte(`{}`)}}); err != nil {
					t.Fatalf("seed project scope: %v", err)
				}
			}
			filter := EventFilter{LoginEmail: "arm@example.com", ProjectHashes: []string{"project-selected"}, ProjectHashesPresent: true}
			seedScope(arm.agent)
			if got, ok, err = s.LatestActivityTs(ctx, filter); err != nil || !ok || !got.Equal(ts) {
				t.Fatalf("LatestActivityTs with project scope = (%s, %v, %v), want (%s, true, nil)", got, ok, err, ts)
			}

			truncateTables(t, s)
			seedScope("different-agent")
			arm.seed(t, s, ts)
			if got, ok, err = s.LatestActivityTs(ctx, filter); err != nil {
				t.Fatalf("LatestActivityTs with different-agent scope: %v", err)
			} else if ok {
				t.Fatalf("LatestActivityTs with different-agent scope = (%s, true), want no cross-agent row", got)
			}
		})
	}

	t.Run("all arms empty", func(t *testing.T) {
		truncateTables(t, s)
		_, ok, err := s.LatestActivityTs(ctx, EventFilter{})
		if err != nil {
			t.Fatalf("LatestActivityTs: %v", err)
		}
		if ok {
			t.Fatal("LatestActivityTs reported data for empty sources")
		}
	})
}

func TestPgStore_LatestActivityTs_excludesNewestAccountCaseInsensitively(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	kept := now.Add(-2 * time.Hour)

	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: kept, EventName: "api_request", SessionID: "kept", LoginEmail: "kept@example.com"},
		{Ts: now.Add(-time.Hour), EventName: "api_request", SessionID: "hidden", LoginEmail: "HIDDEN@example.com"},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO excluded_accounts (login_email) VALUES ('hidden@EXAMPLE.COM')`); err != nil {
		t.Fatalf("insert excluded account: %v", err)
	}

	got, ok, err := s.LatestActivityTs(ctx, EventFilter{})
	if err != nil {
		t.Fatalf("LatestActivityTs: %v", err)
	}
	if !ok || !got.Equal(kept) {
		t.Fatalf("LatestActivityTs = (%s, %v), want (%s, true)", got, ok, kept)
	}
}

func TestPgStore_LatestActivityTs_preservesDashboardFilters(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	want := now.Add(-2 * time.Hour)

	target := &OtelEvent{
		Ts: want, EventName: "api_request", SessionID: "target", ProfileEmail: "profile@example.com",
		LoginEmail: "login@example.com", UserID: "user-1", UserTeam: "team-1", Model: "claude-sonnet-4-6",
		Agent: "claude", BillingProvider: "anthropic",
	}
	if err := s.InsertEvents(ctx, []*OtelEvent{
		target,
		{Ts: now.Add(-time.Hour), EventName: "api_request", SessionID: "newer", ProfileEmail: "other@example.com",
			LoginEmail: "login@example.com", UserID: "user-1", UserTeam: "team-1", Model: "claude-sonnet-4-6",
			Agent: "claude", BillingProvider: "anthropic"},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: want.Add(-time.Hour), SessionID: "target", ProjectHash: "project-1", RecordType: "user", UUID: "project-target", Raw: []byte(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	filter := EventFilter{
		ProfileEmail: "profile@example.com", LoginEmail: "login@example.com", UserID: "user-1",
		ProjectHash: "project-1", UserTeam: "team-1", Agent: "claude", ModelCategory: "anthropic", Model: "sonnet-4-6",
	}
	got, ok, err := s.LatestActivityTs(ctx, filter)
	if err != nil {
		t.Fatalf("LatestActivityTs: %v", err)
	}
	if !ok || !got.Equal(want) {
		t.Fatalf("LatestActivityTs = (%s, %v), want (%s, true)", got, ok, want)
	}

	filtersThatMustReject := []EventFilter{
		{ProfileEmail: "missing@example.com"}, {LoginEmail: "missing@example.com"}, {UserID: "missing"},
		{ProjectHash: "missing"}, {UserTeam: "missing"}, {Agent: "codex"},
		{ModelCategory: "codex"}, {ModelCategory: "compatible"}, {Model: "opus"},
	}
	for _, f := range filtersThatMustReject {
		if _, ok, err := s.LatestActivityTs(ctx, f); err != nil {
			t.Fatalf("LatestActivityTs(%+v): %v", f, err)
		} else if ok {
			t.Errorf("LatestActivityTs(%+v) matched a row", f)
		}
	}
}

func TestBuildLatestActivityQuery_usesOrderedLimitPerSourceArm(t *testing.T) {
	query, _ := buildLatestActivityQuery(EventFilter{})
	if strings.Contains(strings.ToLower(query), "max(") {
		t.Fatalf("latest activity query still contains max aggregate:\n%s", query)
	}
	for _, table := range []string{"otel_events", "codex_imputed_cost", "claude_imputed_cost"} {
		start := strings.Index(query, "FROM "+table)
		if start < 0 {
			t.Fatalf("latest activity query missing %s arm", table)
		}
		arm := query[start:]
		if end := strings.Index(arm, "UNION ALL"); end >= 0 {
			arm = arm[:end]
		}
		if !strings.Contains(arm, "ORDER BY") || !strings.Contains(arm, "LIMIT 1") {
			t.Fatalf("%s arm is not an ordered-limit lookup:\n%s", table, arm)
		}
	}
	if got := strings.Count(query, "FROM session_records"); got < 2 {
		t.Fatalf("latest activity query has %d session_records references, want gjc and omo source arms", got)
	}
	if got := strings.Count(query, "ORDER BY"); got != 7 { // six source lookups plus the six-row merge
		t.Fatalf("latest activity query has %d ORDER BY clauses, want 7:\n%s", got, query)
	}
	if got := strings.Count(query, "LIMIT 1"); got != 7 {
		t.Fatalf("latest activity query has %d LIMIT 1 clauses, want 7:\n%s", got, query)
	}
}

func TestBuildLatestActivityQuery_precomputesProjectScopeOnce(t *testing.T) {
	query, args := buildLatestActivityQuery(EventFilter{
		ProjectHashes:        []string{"h-main", "h-worktree"},
		ProjectHashesPresent: true,
	})

	if got := strings.Count(query, "project_hash = ANY("); got != 1 {
		t.Fatalf("latest activity project scope is computed %d times, want once:\n%s", got, query)
	}
	if strings.Contains(query, "SELECT 1 FROM session_records project_scope") {
		t.Fatalf("latest activity still probes session_records once per event row:\n%s", query)
	}
	if !strings.Contains(query, "project_scope AS MATERIALIZED") || !strings.Contains(query, "FROM session_records") {
		t.Fatalf("latest activity query does not materialize session_records scope:\n%s", query)
	}
	// Each arm now drives from the scope rather than joining it onto a
	// newest-first walk, so the shape is one lateral per arm. The property the
	// original assertion protected is unchanged: every arm is agent-correlated
	// against the scope that was computed once above.
	if got := strings.Count(query, "FROM project_scope project_filter"); got != 6 {
		t.Fatalf("latest activity query drives %d arms from its project scope, want 6:\n%s", got, query)
	}
	if got := strings.Count(query, "= project_filter.agent"); got != 6 {
		t.Fatalf("latest activity query agent-correlates %d arms, want 6:\n%s", got, query)
	}
	if len(args) != 1 {
		t.Fatalf("latest activity query args = %d, want one member-hash array", len(args))
	}
}

func assertStringsEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}
