package store

import (
	"context"
	"testing"
	"time"
)

func TestPgStore_CostByUser_CodexInputExcludesCacheRead(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{
			Ts:              now,
			SessionID:       "codex-cost-by-user",
			RecordType:      "usage",
			ProfileEmail:    "codex@ex.com",
			UserID:          "uid-codex",
			Model:           "gpt-5.5",
			InputTokens:     ptrInt(100),
			OutputTokens:    ptrInt(20),
			CacheReadTokens: ptrInt(40),
			Agent:           "codex",
			BillingProvider: "openai",
		},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	refreshCodexImputed(t, s)

	summaries, err := s.CostByUser(ctx, now.Add(-time.Minute), now.Add(time.Minute), "", "", "")
	if err != nil {
		t.Fatalf("CostByUser: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("summaries = %d, want 1", len(summaries))
	}

	got := summaries[0]
	if got.TotalInput != 60 || got.TotalOutput != 20 {
		t.Fatalf("tokens = %d/%d, want 60/20", got.TotalInput, got.TotalOutput)
	}
	wantCost := ((float64(100-40) * 5.00) + (float64(40) * 0.50) + (float64(20) * 30.00)) / 1000000.0
	if got.TotalCost < wantCost-0.0000001 || got.TotalCost > wantCost+0.0000001 {
		t.Fatalf("cost = %.8f, want %.8f", got.TotalCost, wantCost)
	}
}
