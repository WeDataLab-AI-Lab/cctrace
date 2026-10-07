package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// The session list shows one row per session because a session is one
// conversation. The drill-down is where the split becomes readable: which
// account held which stretch of it, and how much each spent.
func TestSessionAccountSegments_splitsAtTheSwitch(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := recentDay(51).Add(10 * time.Hour)

	in, out := 100, 10
	ev := func(email string, at time.Time) *OtelEvent {
		return &OtelEvent{Ts: at, EventName: "api_request", SessionID: "sw", Agent: "claude",
			LoginEmail: email, InputTokens: &in, OutputTokens: &out}
	}
	if err := s.InsertEvents(ctx, []*OtelEvent{
		ev("one@example.com", ts),
		ev("one@example.com", ts.Add(time.Hour)),
		ev("two@example.com", ts.Add(2*time.Hour)),
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	got, err := s.SessionAccountSegments(ctx, "sw")
	if err != nil {
		t.Fatalf("SessionAccountSegments: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("segments = %d, want 2", len(got))
	}

	// Ordered by when each stretch began, so the drill-down reads as a timeline.
	if got[0].Account != "one@example.com" || got[1].Account != "two@example.com" {
		t.Fatalf("accounts = %q, %q; want one then two", got[0].Account, got[1].Account)
	}
	if !got[0].StartTime.Equal(ts) || !got[0].EndTime.Equal(ts.Add(time.Hour)) {
		t.Errorf("first segment spans %v..%v, want %v..%v",
			got[0].StartTime, got[0].EndTime, ts, ts.Add(time.Hour))
	}
	if got[0].InputTokens != 200 || got[0].OutputTokens != 20 {
		t.Errorf("first segment tokens = %d/%d, want 200/20", got[0].InputTokens, got[0].OutputTokens)
	}
	if got[1].InputTokens != 100 {
		t.Errorf("second segment input = %d, want 100", got[1].InputTokens)
	}
}

// An ordinary session yields one segment. The caller can then skip the timeline
// entirely rather than rendering a one-row "split".
func TestSessionAccountSegments_singleAccountYieldsOne(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := recentDay(50).Add(10 * time.Hour)

	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: ts, EventName: "api_request", SessionID: "s1", Agent: "claude", LoginEmail: "one@example.com"},
		{Ts: ts.Add(time.Hour), EventName: "api_request", SessionID: "s1", Agent: "claude", LoginEmail: "one@example.com"},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	got, err := s.SessionAccountSegments(ctx, "s1")
	if err != nil {
		t.Fatalf("SessionAccountSegments: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("segments = %d, want 1", len(got))
	}
}

// Codex identifies its account by uuid and has no Anthropic login at all, so
// keying on login_email would report every Codex session as accountless.
func TestSessionAccountSegments_codexUsesAccountID(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "cx", RecordType: "usage", UUID: "c1", Agent: "codex",
			AccountID: "acct-one", Raw: json.RawMessage(`{}`)},
		{Ts: ts.Add(time.Hour), SessionID: "cx", RecordType: "usage", UUID: "c2", Agent: "codex",
			AccountID: "acct-two", Raw: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	got, err := s.SessionAccountSegments(ctx, "cx")
	if err != nil {
		t.Fatalf("SessionAccountSegments: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("segments = %d, want 2", len(got))
	}
	if got[0].Account != "acct-one" || got[1].Account != "acct-two" {
		t.Fatalf("accounts = %q, %q", got[0].Account, got[1].Account)
	}
}

// Rows with no account identity are their own segment rather than being dropped
// or folded into a neighbour: the gap is a fact about the session, and hiding it
// would make the segment tokens silently fail to add up to the session total.
func TestSessionAccountSegments_keepsUnattributedStretch(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)

	in, out := 50, 5
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "mix", RecordType: "assistant", UUID: "m1", Agent: "claude",
			InputTokens: &in, OutputTokens: &out, Raw: json.RawMessage(`{}`)},
		{Ts: ts.Add(time.Hour), SessionID: "mix", RecordType: "assistant", UUID: "m2", Agent: "claude",
			LoginEmail: "one@example.com", InputTokens: &in, OutputTokens: &out, Raw: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	got, err := s.SessionAccountSegments(ctx, "mix")
	if err != nil {
		t.Fatalf("SessionAccountSegments: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("segments = %d, want 2 (unattributed stretch kept)", len(got))
	}
	if got[0].Account != "" {
		t.Errorf("first segment account = %q, want empty", got[0].Account)
	}
	var total int64
	for _, seg := range got {
		total += seg.InputTokens
	}
	if total != 100 {
		t.Errorf("segment input tokens sum = %d, want 100 (must equal the session total)", total)
	}
}

// Returning to an account after leaving it is two stretches, not one. Grouping
// by account alone would merge them and lose the ordering the drill-down exists
// to show.
func TestSessionAccountSegments_returnToAccountIsANewSegment(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := recentDay(47).Add(10 * time.Hour)

	ev := func(email string, at time.Time) *OtelEvent {
		return &OtelEvent{Ts: at, EventName: "api_request", SessionID: "rt", Agent: "claude", LoginEmail: email}
	}
	if err := s.InsertEvents(ctx, []*OtelEvent{
		ev("one@example.com", ts),
		ev("two@example.com", ts.Add(time.Hour)),
		ev("one@example.com", ts.Add(2*time.Hour)),
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	got, err := s.SessionAccountSegments(ctx, "rt")
	if err != nil {
		t.Fatalf("SessionAccountSegments: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("segments = %d, want 3 (one, two, one again)", len(got))
	}
	if got[2].Account != "one@example.com" {
		t.Errorf("third segment account = %q, want one@example.com", got[2].Account)
	}
}
