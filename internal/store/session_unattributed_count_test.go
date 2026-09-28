package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestCountUnattributedSessions covers the degradation a login_email filter
// causes: sessions that never emitted OTEL carry no account identity, so any
// account filter drops them. They are real usage, and omitting them silently
// reads as "there was nothing here" -- so the count has to be reportable.
func TestCountUnattributedSessions(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)

	// attributed: OTEL present, account known.
	// unattributed-1/2: JSONL only, no account anywhere.
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: ts, EventName: "api_request", SessionID: "attributed", LoginEmail: "one@example.com"},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "attributed", RecordType: "user", UUID: "a1", LoginEmail: "one@example.com", Raw: json.RawMessage(`{}`)},
		{Ts: ts, SessionID: "unattributed-1", RecordType: "user", UUID: "u1", Raw: json.RawMessage(`{}`)},
		{Ts: ts, SessionID: "unattributed-2", RecordType: "user", UUID: "u2", Raw: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	base := SessionOverviewFilter{}

	all, err := s.CountSessionOverviews(ctx, base)
	if err != nil {
		t.Fatalf("count all: %v", err)
	}
	if all != 3 {
		t.Fatalf("unfiltered count = %d, want 3", all)
	}

	// With an account filter, only the attributed session survives...
	filtered := base
	filtered.LoginEmail = "one@example.com"
	got, err := s.CountSessionOverviews(ctx, filtered)
	if err != nil {
		t.Fatalf("count filtered: %v", err)
	}
	if got != 1 {
		t.Fatalf("filtered count = %d, want 1", got)
	}

	// ...and the two dropped ones are reportable rather than invisible.
	unattr := filtered
	unattr.OnlyUnattributed = true
	got, err = s.CountSessionOverviews(ctx, unattr)
	if err != nil {
		t.Fatalf("count unattributed: %v", err)
	}
	if got != 2 {
		t.Fatalf("unattributed count = %d, want 2", got)
	}
}

// A session that has OTEL is attributed even while its synced rows are still
// blank (the backfill runs on a timer). Counting blank session_records rows
// directly would report it as unknown, overstating the gap.
func TestUnattributedIgnoresPendingBackfill(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)

	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: ts, EventName: "api_request", SessionID: "pending", LoginEmail: "one@example.com"},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	// Synced rows not yet backfilled: login_email still blank.
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "pending", RecordType: "user", UUID: "p1", Raw: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	got, err := s.CountSessionOverviews(ctx, SessionOverviewFilter{
		LoginEmail:       "one@example.com",
		OnlyUnattributed: true,
	})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != 0 {
		t.Fatalf("unattributed count = %d, want 0 (OTEL knows this session's account)", got)
	}
}

// Other filters still apply: the report is "how many did THIS view drop",
// not "how many exist".
func TestUnattributedRespectsOtherFilters(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	inRange := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	outOfRange := inRange.AddDate(0, -1, 0)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: inRange, SessionID: "in", RecordType: "user", UUID: "i1", Raw: json.RawMessage(`{}`)},
		{Ts: outOfRange, SessionID: "out", RecordType: "user", UUID: "o1", Raw: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	since := inRange.Add(-time.Hour)
	until := inRange.Add(time.Hour)
	got, err := s.CountSessionOverviews(ctx, SessionOverviewFilter{
		LoginEmail:       "one@example.com",
		OnlyUnattributed: true,
		Since:            &since,
		Until:            &until,
	})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != 1 {
		t.Fatalf("unattributed count = %d, want 1 (the out-of-range session must not be counted)", got)
	}
}
