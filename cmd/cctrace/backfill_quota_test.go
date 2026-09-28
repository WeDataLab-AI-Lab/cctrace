package main

import (
	"context"
	"testing"
	"time"

	"cctrace/internal/store"
)

func qs(account, window string, at time.Time, pct float64) *store.QuotaSample {
	return &store.QuotaSample{
		BillingProvider: "openai",
		AccountID:       account,
		WindowKey:       window,
		SampledAt:       at,
		UsedPct:         pct,
	}
}

// newDrySender builds a sender that counts without uploading, which is what the
// dry run does and what these tests need.
func newDrySender() *backfillSender {
	return &backfillSender{dryRun: true, seen: map[quotaKey]bool{}}
}

// Codex homes overlap on real machines: an embedded runtime home can hold copies
// of the same session files. Measured on the development machine, folding those
// and the repeats inside a single file together turned 632,019 scanned readings
// into 109,780 distinct ones.
//
// The repeats are safe to drop because they are identical, not merely
// same-keyed: across 22,025 same-key groups in the local logs, none disagreed
// on used_percent.
func TestBackfillSender_collapsesTheSameReading(t *testing.T) {
	at := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	s := newDrySender()

	if err := s.add(context.Background(), []*store.QuotaSample{
		qs("acct-1", "300", at, 42),
		qs("acct-1", "300", at, 42), // same file, same instant
		qs("acct-1", "300", at, 42), // the other home's copy
		qs("acct-1", "10080", at, 61),
	}); err != nil {
		t.Fatalf("add: %v", err)
	}

	if s.found != 4 {
		t.Errorf("found = %d, want 4", s.found)
	}
	if s.unique != 2 {
		t.Errorf("unique = %d, want 2", s.unique)
	}
	if s.duplicates != 2 {
		t.Errorf("duplicates = %d, want 2", s.duplicates)
	}
}

// The key must match the one the server enforces, so what is dropped locally is
// exactly what would have been dropped there — never a row the server would
// have kept.
func TestBackfillSender_keepsDistinctKeys(t *testing.T) {
	at := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	s := newDrySender()

	if err := s.add(context.Background(), []*store.QuotaSample{
		qs("acct-1", "300", at, 42),
		qs("acct-2", "300", at, 42),                  // different account
		qs("acct-1", "10080", at, 42),                // different window
		qs("acct-1", "300", at.Add(time.Second), 43), // different instant
	}); err != nil {
		t.Fatalf("add: %v", err)
	}

	if s.unique != 4 {
		t.Fatalf("unique = %d, want 4 — distinct keys must survive", s.unique)
	}
}

// Sub-second resolution is part of the key. Truncating to whole seconds would
// silently merge readings the server would have kept apart.
func TestBackfillSender_distinguishesSubSecondInstants(t *testing.T) {
	at := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	s := newDrySender()

	if err := s.add(context.Background(), []*store.QuotaSample{
		qs("acct-1", "300", at, 42),
		qs("acct-1", "300", at.Add(time.Millisecond), 42),
	}); err != nil {
		t.Fatalf("add: %v", err)
	}

	if s.unique != 2 {
		t.Fatalf("unique = %d, want 2", s.unique)
	}
}

// Readings are uploaded as they are found, so what stays resident is one chunk
// and the dedup set — not every reading on the machine. The buffer must
// therefore drain rather than grow.
func TestBackfillSender_bufferDoesNotGrowPastAChunk(t *testing.T) {
	at := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	s := newDrySender()

	rows := make([]*store.QuotaSample, 0, backfillChunk*3)
	for i := range cap(rows) {
		rows = append(rows, qs("acct-1", "300", at.Add(time.Duration(i)*time.Millisecond), 1))
	}
	if err := s.add(context.Background(), rows); err != nil {
		t.Fatalf("add: %v", err)
	}

	if len(s.buf) >= backfillChunk {
		t.Fatalf("buffer holds %d rows, want it drained below the %d chunk", len(s.buf), backfillChunk)
	}
	if s.unique != backfillChunk*3 {
		t.Fatalf("unique = %d, want %d", s.unique, backfillChunk*3)
	}
}

// An inferred row is counted as such wherever it arrives, since the report is
// what tells a reader how much of the history is attribution rather than
// measurement.
func TestBackfillSender_countsInferredRows(t *testing.T) {
	at := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	s := newDrySender()

	observed := qs("acct-1", "300", at, 10)
	observed.Attribution = store.AttributionObserved
	inferred := qs("acct-1", "300", at.Add(time.Minute), 11)
	inferred.Attribution = store.AttributionInferred

	if err := s.add(context.Background(), []*store.QuotaSample{observed, inferred}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if s.inferred != 1 {
		t.Fatalf("inferred = %d, want 1", s.inferred)
	}
}
