package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// ListSessionSummaries paired MAX(profile_email) with SUM(tokens): the totals
// merged every account in the session while the label named whichever profile
// sorted last. #220 names this line specifically.
func TestListSessionSummaries_reportsAccountCountAndDominantProfile(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)

	rec := func(uuid, profile, login string, in, out int, at time.Time) *SessionRecord {
		return &SessionRecord{
			Ts: at, SessionID: "s1", RecordType: "assistant", UUID: uuid, Agent: "claude",
			ProfileEmail: profile, LoginEmail: login,
			InputTokens: &in, OutputTokens: &out, Raw: json.RawMessage(`{}`),
		}
	}
	// "aaa" dominates the session; "zzz" wins an alphabetical MAX().
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		rec("r1", "aaa@example.com", "aaa@example.com", 10_000, 100, ts),
		rec("r2", "zzz@example.com", "zzz@example.com", 10, 1, ts.Add(time.Hour)),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	got, err := s.ListSessionSummaries(ctx, "", "", 50)
	if err != nil {
		t.Fatalf("ListSessionSummaries: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("summaries = %d, want 1 (one row per session)", len(got))
	}
	if got[0].ProfileEmail != "aaa@example.com" {
		t.Errorf("profile_email = %q, want aaa@example.com (the dominant profile)", got[0].ProfileEmail)
	}
	if got[0].AccountCount != 2 {
		t.Errorf("account_count = %d, want 2 (the totals span two accounts)", got[0].AccountCount)
	}
	// The totals still cover the whole session; the count is what tells the reader
	// they are a sum across accounts.
	if got[0].InputTokens != 10_010 {
		t.Errorf("input_tokens = %d, want 10010", got[0].InputTokens)
	}
}

// An ordinary single-account session reports 1, so the badge only appears where
// something is actually split.
func TestListSessionSummaries_singleAccountReportsOne(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "s1", RecordType: "assistant", UUID: "r1", Agent: "claude",
			ProfileEmail: "one@example.com", LoginEmail: "one@example.com", Raw: json.RawMessage(`{}`)},
		{Ts: ts.Add(time.Hour), SessionID: "s1", RecordType: "assistant", UUID: "r2", Agent: "claude",
			ProfileEmail: "one@example.com", LoginEmail: "one@example.com", Raw: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	got, err := s.ListSessionSummaries(ctx, "", "", 50)
	if err != nil {
		t.Fatalf("ListSessionSummaries: %v", err)
	}
	if len(got) != 1 || got[0].AccountCount != 1 {
		t.Fatalf("got %+v, want one summary with account_count 1", got)
	}
}
