package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func overviewBySession(t *testing.T, got []*SessionOverview) map[string]*SessionOverview {
	t.Helper()
	m := map[string]*SessionOverview{}
	for _, o := range got {
		m[o.SessionID] = o
	}
	return m
}

// A session that spans an account switch must say so. The list shows one row per
// session because a session is one conversation, but collapsing the account to a
// single value with no other signal presents split usage as if it belonged to
// one account.
func TestListSessionOverviews_reportsAccountCount(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)

	in, out := 100, 10
	ev := func(sid, email string, at time.Time) *OtelEvent {
		return &OtelEvent{Ts: at, EventName: "api_request", SessionID: sid,
			LoginEmail: email, InputTokens: &in, OutputTokens: &out}
	}
	if err := s.InsertEvents(ctx, []*OtelEvent{
		// switched: one session, two accounts.
		ev("switched", "one@example.com", ts),
		ev("switched", "two@example.com", ts.Add(time.Hour)),
		// steady: one session, one account.
		ev("steady", "one@example.com", ts),
		ev("steady", "one@example.com", ts.Add(time.Hour)),
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	got, err := s.ListSessionOverviews(ctx, SessionOverviewFilter{Limit: 50})
	if err != nil {
		t.Fatalf("ListSessionOverviews: %v", err)
	}
	by := overviewBySession(t, got)

	if by["switched"] == nil || by["steady"] == nil {
		t.Fatalf("missing sessions: %+v", by)
	}
	if by["switched"].AccountCount != 2 {
		t.Errorf("switched account_count = %d, want 2", by["switched"].AccountCount)
	}
	if by["steady"].AccountCount != 1 {
		t.Errorf("steady account_count = %d, want 1", by["steady"].AccountCount)
	}
}

// A Claude account is one account even though OTEL rows identify it by email
// and synced rows now also carry its uuid. Counting distinct values across both
// columns would report every ordinary session as multi-account.
func TestListSessionOverviews_accountCountDoesNotDoubleCountClaude(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)

	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: ts, EventName: "api_request", SessionID: "s1", LoginEmail: "one@example.com", Agent: "claude"},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "s1", RecordType: "user", UUID: "u1", Agent: "claude",
			LoginEmail: "one@example.com", AccountID: "uuid-of-one", Raw: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	got, err := s.ListSessionOverviews(ctx, SessionOverviewFilter{Limit: 50})
	if err != nil {
		t.Fatalf("ListSessionOverviews: %v", err)
	}
	by := overviewBySession(t, got)
	if by["s1"] == nil {
		t.Fatal("session missing")
	}
	if by["s1"].AccountCount != 1 {
		t.Errorf("account_count = %d, want 1 (email and uuid name the same account)", by["s1"].AccountCount)
	}
}

// Codex has no Anthropic login at all, so its identity is the account uuid.
// Keying on login_email alone would report every Codex session as accountless.
func TestListSessionOverviews_codexCountsByAccountID(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "cx", RecordType: "usage", UUID: "c1", Agent: "codex",
			AccountID: "acct-one", Raw: json.RawMessage(`{}`)},
		{Ts: ts.Add(time.Hour), SessionID: "cx", RecordType: "usage", UUID: "c2", Agent: "codex",
			AccountID: "acct-two", Raw: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	got, err := s.ListSessionOverviews(ctx, SessionOverviewFilter{Limit: 50})
	if err != nil {
		t.Fatalf("ListSessionOverviews: %v", err)
	}
	by := overviewBySession(t, got)
	if by["cx"] == nil {
		t.Fatal("codex session missing")
	}
	if by["cx"].AccountCount != 2 {
		t.Errorf("codex account_count = %d, want 2", by["cx"].AccountCount)
	}
}

// The representative account is the one that used the most tokens, not the one
// that sorts last alphabetically. MAX() picked a winner by string order, which
// is arbitrary and changes with an unrelated rename.
func TestListSessionOverviews_representativeAccountIsTheDominantOne(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)

	big, small, out := 10_000, 10, 1
	if err := s.InsertEvents(ctx, []*OtelEvent{
		// "aaa" dominates the session but loses an alphabetical MAX() to "zzz".
		{Ts: ts, EventName: "api_request", SessionID: "s1", LoginEmail: "aaa@example.com",
			InputTokens: &big, OutputTokens: &out},
		{Ts: ts.Add(time.Hour), EventName: "api_request", SessionID: "s1", LoginEmail: "zzz@example.com",
			InputTokens: &small, OutputTokens: &out},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	got, err := s.ListSessionOverviews(ctx, SessionOverviewFilter{Limit: 50})
	if err != nil {
		t.Fatalf("ListSessionOverviews: %v", err)
	}
	by := overviewBySession(t, got)
	if by["s1"] == nil {
		t.Fatal("session missing")
	}
	if by["s1"].LoginEmail != "aaa@example.com" {
		t.Errorf("representative login_email = %q, want aaa@example.com (the dominant account)", by["s1"].LoginEmail)
	}
	if by["s1"].AccountCount != 2 {
		t.Errorf("account_count = %d, want 2", by["s1"].AccountCount)
	}
}
