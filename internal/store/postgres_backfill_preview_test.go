package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// The backfill is a bulk UPDATE on a 3.3M-row hypertable in production. Its
// blast radius has to be measurable before it runs, and measurable by the same
// code that will run it -- a hand-written SQL estimate can drift from the real
// statement, which is exactly how a "small" migration surprises someone.
func TestPreviewBackfillReportsBlastRadius(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)

	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: ts, EventName: "api_request", SessionID: "fillable", LoginEmail: "one@example.com"},
		// A mid-session switch: both sides are fillable now that matching is by
		// interval, which the old uniqueness guard would have skipped entirely.
		{Ts: ts, EventName: "api_request", SessionID: "switched", LoginEmail: "one@example.com"},
		{Ts: ts.Add(2 * time.Hour), EventName: "api_request", SessionID: "switched", LoginEmail: "two@example.com"},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "fillable", RecordType: "user", UUID: "f1", Raw: json.RawMessage(`{}`)},
		{Ts: ts.Add(time.Hour), SessionID: "switched", RecordType: "user", UUID: "s1", Raw: json.RawMessage(`{}`)},
		{Ts: ts.Add(3 * time.Hour), SessionID: "switched", RecordType: "user", UUID: "s2", Raw: json.RawMessage(`{}`)},
		// No OTEL for this session: permanently unfillable, and it must be counted
		// separately so a shortfall is not read as a bug in the backfill.
		{Ts: ts, SessionID: "orphan", RecordType: "user", UUID: "o1", Raw: json.RawMessage(`{}`)},
		// Already attributed: outside the blast radius entirely.
		{Ts: ts, SessionID: "done", RecordType: "user", UUID: "d1", LoginEmail: "one@example.com", Raw: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	p, err := s.PreviewBackfillSessionRecordLoginEmail(ctx, time.Time{})
	if err != nil {
		t.Fatalf("PreviewBackfillSessionRecordLoginEmail: %v", err)
	}

	if p.EmptyRows != 4 {
		t.Errorf("empty rows = %d, want 4", p.EmptyRows)
	}
	if p.FillableRows != 3 {
		t.Errorf("fillable rows = %d, want 3 (fillable + both sides of the switch)", p.FillableRows)
	}
	if p.UnfillableRows != 1 {
		t.Errorf("unfillable rows = %d, want 1 (the session with no OTEL)", p.UnfillableRows)
	}
	if p.FillableSessions != 2 {
		t.Errorf("fillable sessions = %d, want 2", p.FillableSessions)
	}

	// The preview must predict the real statement exactly. If these drift, the
	// number an operator approved is not the number that ran.
	applied, err := s.BackfillSessionRecordLoginEmail(ctx, time.Time{})
	if err != nil {
		t.Fatalf("BackfillSessionRecordLoginEmail: %v", err)
	}
	if applied != p.FillableRows {
		t.Fatalf("applied %d rows, preview said %d", applied, p.FillableRows)
	}
}

// The bounded periodic pass and its preview must agree on scope too, or the
// preview would describe a bigger change than the pass makes.
func TestPreviewBackfillHonoursSince(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	old := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)

	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: old, EventName: "api_request", SessionID: "old-sess", LoginEmail: "one@example.com"},
		{Ts: recent, EventName: "api_request", SessionID: "new-sess", LoginEmail: "two@example.com"},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: old, SessionID: "old-sess", RecordType: "user", UUID: "o1", Raw: json.RawMessage(`{}`)},
		{Ts: recent, SessionID: "new-sess", RecordType: "user", UUID: "r1", Raw: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	p, err := s.PreviewBackfillSessionRecordLoginEmail(ctx, recent.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if p.FillableRows != 1 {
		t.Fatalf("bounded fillable rows = %d, want 1", p.FillableRows)
	}

	applied, err := s.BackfillSessionRecordLoginEmail(ctx, recent.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if applied != p.FillableRows {
		t.Fatalf("applied %d rows, preview said %d", applied, p.FillableRows)
	}
}
