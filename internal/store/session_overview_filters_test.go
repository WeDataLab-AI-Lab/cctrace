package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestPgStore_CountSessionOverviews_foldsForkedSessions_whenAssembled(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	records := []*SessionRecord{
		{Ts: now, SessionID: "root-session", ProfileEmail: "profile@example.com", LoginEmail: "account@example.com", UUID: "root-1", Raw: []byte(`{}`)},
		{Ts: now.Add(time.Minute), SessionID: "branch-session", ProfileEmail: "profile@example.com", LoginEmail: "account@example.com", UUID: "branch-1", ForkedFromSession: "root-session", Raw: []byte(`{}`)},
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	rawCount, err := s.CountSessionOverviews(ctx, SessionOverviewFilter{LoginEmail: "account@example.com"})
	if err != nil {
		t.Fatalf("CountSessionOverviews raw: %v", err)
	}
	assembledCount, err := s.CountSessionOverviews(ctx, SessionOverviewFilter{LoginEmail: "account@example.com", FoldLineage: true})
	if err != nil {
		t.Fatalf("CountSessionOverviews assembled: %v", err)
	}

	if rawCount != 2 {
		t.Fatalf("expected raw count 2, got %d", rawCount)
	}
	if assembledCount != 1 {
		t.Fatalf("expected assembled count 1, got %d", assembledCount)
	}
}

func TestPgStore_ListSessionOverviews_excludesShellSessions_lackingApiRequestAndSync(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	// Case A: only OTEL start telemetry (no api_request), no session_records -> excluded.
	// Case B: has api_request -> included.
	events := []*OtelEvent{
		{Ts: now, EventName: "hook_registered", SessionID: "shell-only-session", ProfileEmail: "profile@example.com", LoginEmail: "account@example.com", Agent: "claude", BillingProvider: "anthropic"},
		{Ts: now.Add(time.Minute), EventName: "api_request", SessionID: "api-request-session", ProfileEmail: "profile@example.com", LoginEmail: "account@example.com", Agent: "claude", BillingProvider: "anthropic"},
	}
	if err := s.InsertEvents(ctx, events); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	// Case C: no api_request, but has session_records -> included (regression guard).
	records := []*SessionRecord{
		{Ts: now.Add(2 * time.Minute), SessionID: "jsonl-only-session", ProfileEmail: "profile@example.com", LoginEmail: "account@example.com", UUID: "jsonl-1", Raw: []byte(`{}`)},
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	overviews, err := s.ListSessionOverviews(ctx, SessionOverviewFilter{LoginEmail: "account@example.com", Limit: 100})
	if err != nil {
		t.Fatalf("ListSessionOverviews: %v", err)
	}
	got := make(map[string]bool)
	for _, o := range overviews {
		got[o.SessionID] = true
	}
	if got["shell-only-session"] {
		t.Errorf("expected shell-only-session (no api_request, no sync) to be excluded, but it was present")
	}
	if !got["api-request-session"] {
		t.Errorf("expected api-request-session to be included, but it was missing")
	}
	if !got["jsonl-only-session"] {
		t.Errorf("expected jsonl-only-session (no api_request but has sync) to be included, but it was missing")
	}

	// Case D: count must match the list (excluding the shell-only session).
	cnt, err := s.CountSessionOverviews(ctx, SessionOverviewFilter{LoginEmail: "account@example.com"})
	if err != nil {
		t.Fatalf("CountSessionOverviews: %v", err)
	}
	if cnt != 2 {
		t.Fatalf("expected count 2 (api-request-session + jsonl-only-session), got %d", cnt)
	}
}

func TestPgStore_LatestActivityTs_filtersModel_whenModelSpecified(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	events := []*OtelEvent{
		{Ts: now.Add(-2 * time.Hour), EventName: "api_request", SessionID: "sonnet-session", ProfileEmail: "profile@example.com", LoginEmail: "account@example.com", Model: "claude-sonnet-4-6", Agent: "claude", BillingProvider: "anthropic"},
		{Ts: now.Add(-1 * time.Hour), EventName: "api_request", SessionID: "haiku-session", ProfileEmail: "profile@example.com", LoginEmail: "account@example.com", Model: "claude-haiku-4-5", Agent: "claude", BillingProvider: "anthropic"},
	}
	if err := s.InsertEvents(ctx, events); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	got, ok, err := s.LatestActivityTs(ctx, EventFilter{LoginEmail: "account@example.com", Model: "sonnet-4-6"})
	if err != nil {
		t.Fatalf("LatestActivityTs: %v", err)
	}
	if !ok {
		t.Fatal("expected latest activity for selected model")
	}
	if !got.Equal(events[0].Ts) {
		t.Fatalf("expected sonnet timestamp %s, got %s", events[0].Ts, got)
	}
}

// Sessions sharing the exact same end_time must order deterministically: without a
// tiebreaker the planner may order equal sort keys differently per query.
func TestPgStore_ListSessionOverviews_tiedEndTime_isDeterministic(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	if err := s.InsertSessionRecords(ctx, tiedEndTimeRecords(now, "tied", 6)); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	filter := SessionOverviewFilter{LoginEmail: "account@example.com", Limit: 6}
	first, err := s.ListSessionOverviews(ctx, filter)
	if err != nil {
		t.Fatalf("ListSessionOverviews: %v", err)
	}
	if len(first) != 6 {
		t.Fatalf("expected 6 overviews, got %d", len(first))
	}
	// Repeated identical queries happening to agree is not evidence of determinism —
	// the planner is free to reorder equal keys. Assert the tiebreaker contract itself:
	// among equal end_time, session_id must descend.
	for i := 1; i < len(first); i++ {
		if first[i-1].SessionID <= first[i].SessionID {
			t.Fatalf("position %d: session_id %q not descending after %q (missing tiebreaker)",
				i, first[i].SessionID, first[i-1].SessionID)
		}
	}
	for attempt := 0; attempt < 5; attempt++ {
		again, err := s.ListSessionOverviews(ctx, filter)
		if err != nil {
			t.Fatalf("ListSessionOverviews attempt %d: %v", attempt, err)
		}
		for i := range first {
			if again[i].SessionID != first[i].SessionID {
				t.Fatalf("attempt %d position %d: got %s, want %s (order not deterministic)",
					attempt, i, again[i].SessionID, first[i].SessionID)
			}
		}
	}
}

// Paging over tied end_time must neither skip nor repeat a session.
func TestPgStore_ListSessionOverviews_tiedEndTime_pagesWithoutGapOrDuplicate(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	if err := s.InsertSessionRecords(ctx, tiedEndTimeRecords(now, "paged", 6)); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	seen := make(map[string]int)
	for _, offset := range []int{0, 2, 4} {
		page, err := s.ListSessionOverviews(ctx, SessionOverviewFilter{
			LoginEmail: "account@example.com", Limit: 2, Offset: offset,
		})
		if err != nil {
			t.Fatalf("ListSessionOverviews offset=%d: %v", offset, err)
		}
		if len(page) != 2 {
			t.Fatalf("offset=%d: expected 2 rows, got %d", offset, len(page))
		}
		for _, o := range page {
			seen[o.SessionID]++
		}
	}
	if len(seen) != 6 {
		t.Fatalf("union of pages has %d distinct sessions, want 6: %v", len(seen), seen)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("session %s appeared %d times across pages, want 1", id, n)
		}
	}
}

// has_enriched is already computed in the grouped CTE; the overview must expose it
// so the UI judges "legacy" per session instead of per loaded page.
func TestPgStore_ListSessionOverviews_exposesHasEnriched(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	records := []*SessionRecord{
		{Ts: now, SessionID: "enriched-session", ProfileEmail: "profile@example.com", LoginEmail: "account@example.com", UUID: "enriched-1", SourceFile: "rollout-abc.jsonl", Raw: []byte(`{}`)},
		{Ts: now.Add(time.Minute), SessionID: "bare-session", ProfileEmail: "profile@example.com", LoginEmail: "account@example.com", UUID: "bare-1", Raw: []byte(`{}`)},
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	overviews, err := s.ListSessionOverviews(ctx, SessionOverviewFilter{LoginEmail: "account@example.com", Limit: 100})
	if err != nil {
		t.Fatalf("ListSessionOverviews: %v", err)
	}
	got := make(map[string]bool, len(overviews))
	for _, o := range overviews {
		got[o.SessionID] = o.HasEnriched
	}
	if len(overviews) != 2 {
		t.Fatalf("expected 2 overviews, got %d", len(overviews))
	}
	if !got["enriched-session"] {
		t.Error("enriched-session: HasEnriched = false, want true")
	}
	if got["bare-session"] {
		t.Error("bare-session: HasEnriched = true, want false")
	}
}

func tiedEndTimeRecords(ts time.Time, prefix string, n int) []*SessionRecord {
	records := make([]*SessionRecord, 0, n)
	for i := 0; i < n; i++ {
		records = append(records, &SessionRecord{
			Ts:           ts,
			SessionID:    fmt.Sprintf("%s-session-%d", prefix, i),
			ProfileEmail: "profile@example.com",
			LoginEmail:   "account@example.com",
			UUID:         fmt.Sprintf("%s-%d", prefix, i),
			Raw:          []byte(`{}`),
		})
	}
	return records
}

// The harness scope answers "what program ran this", and 'other' means every harness that
// is not one of the two named ones. It is expressed as NOT IN rather than a list of the
// rest so a harness added later is reachable the day it starts syncing -- an enumeration
// would have to be edited for each one, and this file's neighbours are where enumerations
// have been missed before (config keys, status state files).
//
// Billing is a separate axis: Claude Code and Codex are harnesses another model can be
// plugged into, and one gjc session can mix providers inside itself. So this filter must
// not consult billing_provider at all, which the cases below pin by giving gjc the same
// provider as codex, and omo the same as claude.
func TestPgStore_SessionOverviews_harnessScopeOther(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	const profile = "profile@example.invalid"
	const account = "account@example.invalid"

	records := []*SessionRecord{
		{Ts: now, SessionID: "claude-session", Agent: "claude", BillingProvider: "anthropic",
			ProfileEmail: profile, LoginEmail: account, UUID: "c-1", Raw: []byte(`{}`)},
		{Ts: now, SessionID: "codex-session", Agent: "codex", BillingProvider: "openai",
			ProfileEmail: profile, LoginEmail: account, UUID: "x-1", Raw: []byte(`{}`)},
		{Ts: now, SessionID: "gjc-session", Agent: "gjc", BillingProvider: "openai",
			ProfileEmail: profile, LoginEmail: account, UUID: "g-1", Raw: []byte(`{}`)},
		{Ts: now, SessionID: "omo-session", Agent: "omo", BillingProvider: "anthropic",
			ProfileEmail: profile, LoginEmail: account, UUID: "o-1", Raw: []byte(`{}`)},
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	sessionsFor := func(agent string) map[string]bool {
		t.Helper()
		rows, err := s.ListSessionOverviews(ctx, SessionOverviewFilter{LoginEmail: account, Agent: agent})
		if err != nil {
			t.Fatalf("ListSessionOverviews(%q): %v", agent, err)
		}
		got := map[string]bool{}
		for _, r := range rows {
			got[r.SessionID] = true
		}
		// The count is shown next to this list; a filter the two disagree about reads as
		// missing rows rather than as a bug.
		count, err := s.CountSessionOverviews(ctx, SessionOverviewFilter{LoginEmail: account, Agent: agent})
		if err != nil {
			t.Fatalf("CountSessionOverviews(%q): %v", agent, err)
		}
		if count != len(rows) {
			t.Errorf("agent %q: count %d but list returned %d", agent, count, len(rows))
		}
		return got
	}

	for _, tc := range []struct {
		agent string
		want  []string
	}{
		{"", []string{"claude-session", "codex-session", "gjc-session", "omo-session"}},
		{"claude", []string{"claude-session"}},
		{"codex", []string{"codex-session"}},
		// gjc and omo both land here, and neither of the two named harnesses does -- even
		// though gjc-session is billed to openai like the codex one, and omo-session to
		// anthropic like the claude one. That is the point: this axis is not billing.
		{"other", []string{"gjc-session", "omo-session"}},
	} {
		got := sessionsFor(tc.agent)
		if len(got) != len(tc.want) {
			t.Errorf("agent %q: got %d sessions %v, want %v", tc.agent, len(got), got, tc.want)
			continue
		}
		for _, id := range tc.want {
			if !got[id] {
				t.Errorf("agent %q: missing %s (got %v)", tc.agent, id, got)
			}
		}
	}
}
