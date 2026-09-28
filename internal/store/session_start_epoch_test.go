package store

import (
	"context"
	"testing"
	"time"
)

// Claude state lines (last-prompt, permission-mode, cost-state, ai-title, ...) carry no
// timestamp of their own, so the syncer stores them at the zero instant to keep their
// storage key stable across rescans (#57). Taking that row as MIN(ts) dated 979
// production sessions to 1970-01-01 in the session list.
func TestSessionStart_EpochStateRecordDoesNotStartSession(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	epoch := time.Unix(0, 0).UTC()
	far := ts.Add(24 * time.Hour)
	email := "start@example.test"

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: epoch, SessionID: "mixed", ProfileEmail: email, RecordType: "last-prompt", UUID: "content-mixed", Raw: []byte(`{}`)},
		{Ts: ts, SessionID: "mixed", ProfileEmail: email, RecordType: "user", UUID: "m1", Raw: []byte(`{}`)},
		{Ts: ts.Add(5 * time.Minute), SessionID: "mixed", ProfileEmail: email, RecordType: "assistant", UUID: "m2", Raw: []byte(`{}`)},
		{Ts: epoch, SessionID: "all-epoch", ProfileEmail: email, RecordType: "last-prompt", UUID: "content-a", Raw: []byte(`{}`)},
		{Ts: epoch, SessionID: "all-epoch", ProfileEmail: email, RecordType: "mode", UUID: "content-b", Raw: []byte(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	// The rollup path (no date filter) and the direct path (date filter set) are separate
	// statements over the same union, so both are checked. Since is pinned to the zero
	// instant on purpose: a later Since would hide the epoch row behind the date filter
	// and the direct query would look correct for the wrong reason.
	paths := []struct {
		name   string
		filter SessionOverviewFilter
	}{
		{"rollup", SessionOverviewFilter{ProfileEmail: email, Limit: 100}},
		{"direct", SessionOverviewFilter{ProfileEmail: email, Limit: 100, Since: &epoch, Until: &far}},
	}
	for _, path := range paths {
		got := overviewFor(t, s, path.filter, "mixed")
		if got == nil {
			t.Fatalf("%s: session mixed missing from list", path.name)
		}
		if !got.StartTime.Equal(ts) {
			t.Errorf("%s: mixed start_time = %s, want %s", path.name, got.StartTime, ts)
		}
		if !got.EndTime.Equal(ts.Add(5 * time.Minute)) {
			t.Errorf("%s: mixed end_time = %s, want %s", path.name, got.EndTime, ts.Add(5*time.Minute))
		}
		// Nothing else exists to date this session by, so it keeps the only timestamp it
		// has. A NULL here would break the list ordering and the API contract.
		allEpoch := overviewFor(t, s, path.filter, "all-epoch")
		if allEpoch == nil {
			t.Fatalf("%s: session all-epoch missing from list", path.name)
		}
		if !allEpoch.StartTime.Equal(epoch) || !allEpoch.EndTime.Equal(epoch) {
			t.Errorf("%s: all-epoch = [%s, %s], want both %s", path.name, allEpoch.StartTime, allEpoch.EndTime, epoch)
		}
	}

	summaries, err := s.ListSessionSummaries(ctx, email, "", 50)
	if err != nil {
		t.Fatalf("ListSessionSummaries: %v", err)
	}
	byID := map[string]*SessionSummary{}
	for _, row := range summaries {
		byID[row.SessionID] = row
	}
	if got := byID["mixed"]; got == nil || !got.StartTime.Equal(ts) || !got.EndTime.Equal(ts.Add(5*time.Minute)) {
		t.Errorf("summaries: mixed = %+v, want start %s", got, ts)
	}
	if got := byID["all-epoch"]; got == nil || !got.StartTime.Equal(epoch) {
		t.Errorf("summaries: all-epoch = %+v, want start %s", got, epoch)
	}
}

// The session detail reads session_records directly whenever a session has no OTEL
// rows -- the same JSONL-only session the list fix is about. Left in, the
// timestamp-less state records opened a 1970 segment with no account, so the list
// said the session started today while its drill-down said 1970.
func TestSessionAccountSegments_epochStateRecordIsNotASegment(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 9, 3, 14, 0, 0, 0, time.UTC)
	epoch := time.Unix(0, 0).UTC()
	account := "seg@example.test"
	in, out := 30, 3

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: epoch, SessionID: "seg-mixed", RecordType: "last-prompt", UUID: "content-seg", Agent: "claude", Raw: []byte(`{}`)},
		{Ts: ts, SessionID: "seg-mixed", RecordType: "assistant", UUID: "g1", Agent: "claude",
			LoginEmail: account, InputTokens: &in, OutputTokens: &out, Raw: []byte(`{}`)},
		{Ts: ts.Add(time.Hour), SessionID: "seg-mixed", RecordType: "assistant", UUID: "g2", Agent: "claude",
			LoginEmail: account, InputTokens: &in, OutputTokens: &out, Raw: []byte(`{}`)},
		{Ts: epoch, SessionID: "seg-all-epoch", RecordType: "mode", UUID: "content-seg-b", Agent: "claude", Raw: []byte(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	got, err := s.SessionAccountSegments(ctx, "seg-mixed")
	if err != nil {
		t.Fatalf("SessionAccountSegments: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("segments = %d, want 1; first = %+v", len(got), got[0])
	}
	if !got[0].StartTime.Equal(ts) {
		t.Errorf("segment starts at %s, want %s", got[0].StartTime, ts)
	}
	// The dropped rows carry no tokens and no cost, so the timeline still adds up to
	// the session's totals -- the one thing the segments must never stop doing.
	if got[0].InputTokens != 60 || got[0].OutputTokens != 6 {
		t.Errorf("segment tokens = %d/%d, want 60/6", got[0].InputTokens, got[0].OutputTokens)
	}

	// Same fallback as sessionStartExpr: a session that has nothing but state records
	// keeps them, because an empty drill-down would claim the session has no records.
	all, err := s.SessionAccountSegments(ctx, "seg-all-epoch")
	if err != nil {
		t.Fatalf("SessionAccountSegments all-epoch: %v", err)
	}
	if len(all) != 1 || !all[0].StartTime.Equal(epoch) {
		t.Fatalf("all-epoch segments = %+v, want one at %s", all, epoch)
	}
}
