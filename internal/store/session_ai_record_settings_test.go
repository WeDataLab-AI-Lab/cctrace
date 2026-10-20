package store

import (
	"context"
	"testing"
	"time"
)

func TestSessionAIRecordSettingsStayIndependentFromWeeklyAI(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	if err := s.SetAISettings(ctx, "openai-api", "weekly-model", "medium", "", "weekly-admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAIRecordRuntimeSettings(ctx, "openai-api", "record-model", "low", "record-admin"); err != nil {
		t.Fatal(err)
	}
	cap := int64(500000)
	days := 21
	budget := int64(9000000)
	since := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	want := SessionAIRecordSettings{Enabled: true, Runtime: "litellm-api", DailyTokenCap: &cap, BackfillEnabled: true, BackfillDays: &days, BackfillTokenBudget: &budget, BackfillSince: &since}
	if err := s.SetSessionAIRecordSettings(ctx, want, "records-admin"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSessionAIRecordSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled != want.Enabled || got.Runtime != want.Runtime || got.DailyTokenCap == nil || *got.DailyTokenCap != cap || got.BackfillDays == nil || *got.BackfillDays != days || got.BackfillTokenBudget == nil || *got.BackfillTokenBudget != budget || got.BackfillSince == nil || got.BackfillSince.Before(since) || got.BackfillSince.After(time.Now().Add(time.Minute)) {
		t.Fatalf("record settings = %+v, want %+v", got, want)
	}
	weekly, err := s.GetAISettings(ctx, "openai-api")
	if err != nil {
		t.Fatal(err)
	}
	if weekly.Model != "weekly-model" || weekly.ReasoningEffort != "medium" {
		t.Fatalf("session record settings overwrote weekly AI settings: %+v", weekly)
	}
	recordRuntime, err := s.GetAIRecordRuntimeSettings(ctx, "openai-api")
	if err != nil {
		t.Fatal(err)
	}
	if recordRuntime.Model != "record-model" || recordRuntime.ReasoningEffort != "low" {
		t.Fatalf("record runtime settings = %+v", recordRuntime)
	}
}
