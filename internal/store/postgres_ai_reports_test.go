package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestAIReportsLifecycle(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	r := aiTestRun(t, s)
	id, err := s.CreateAIReportRun(ctx, &r)
	if err != nil {
		t.Fatal(err)
	}
	assertNull := func() {
		t.Helper()
		var null bool
		if err := s.pool.QueryRow(ctx, `SELECT NOT tokens_reported AND input_tokens IS NULL AND cached_input_tokens IS NULL AND output_tokens IS NULL FROM ai_report_runs WHERE id=$1`, id).Scan(&null); err != nil || !null {
			t.Fatalf("NULL usage=%v err=%v", null, err)
		}
	}
	assertNull()
	if err = s.UpdateAIRunUsage(ctx, id, AIUsage{Reported: true, InputTokens: 12, CachedInputTokens: 3, OutputTokens: 4}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAIReportRun(ctx, id)
	if err != nil || !got.Usage.Reported || got.Usage.InputTokens != 12 {
		t.Fatalf("run=%+v err=%v", got, err)
	}
	if err = s.UpdateAIRunUsage(ctx, id, AIUsage{InputTokens: 99}); err != nil {
		t.Fatal(err)
	}
	assertNull()
	for _, seq := range []int{3, 1, 2} {
		if err = s.AppendAIToolCall(ctx, id, AIToolCall{Seq: seq, Tool: "query_segments", Args: json.RawMessage(`{"limit":2}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.FinishAIToolCall(ctx, id, 2, AIToolCallOK, 2, 100, 20); err != nil {
		t.Fatal(err)
	}
	calls, err := s.ListAIToolCalls(ctx, id, 1)
	if err != nil || len(calls) != 2 {
		t.Fatalf("calls=%+v err=%v", calls, err)
	}
	if calls[0].Seq != 2 || calls[0].Status != AIToolCallOK || calls[0].ResultRows == nil || *calls[0].ResultRows != 2 || calls[1].Seq != 3 || calls[1].ResultRows != nil {
		t.Fatalf("calls=%+v", calls)
	}
	report := AIReport{DashboardUserID: r.DashboardUserID, Week: r.Week, TZ: r.TZ, Since: r.Since, Until: r.Until, Summary: "first", Items: []AIReportItem{{SegmentID: "42", Title: "title", Reason: "reason"}}}
	if err = s.CompleteAIReportRun(ctx, id, report, AIUsage{}, 1); err != nil {
		t.Fatal(err)
	}
	assertNull()
	got, err = s.GetAIReportRun(ctx, id)
	if err != nil || got.Status != AIRunCompleted || got.FinishedAt == nil || got.ItemsDropped != 1 {
		t.Fatalf("completed=%+v err=%v", got, err)
	}
	id2, err := s.CreateAIReportRun(ctx, &r)
	if err != nil {
		t.Fatal(err)
	}
	report.Summary = "replacement"
	report.Items = []AIReportItem{}
	if err = s.CompleteAIReportRun(ctx, id2, report, AIUsage{Reported: true, InputTokens: 10, OutputTokens: 2}, 0); err != nil {
		t.Fatal(err)
	}
	saved, err := s.GetAIReport(ctx, r.DashboardUserID, r.Week)
	if err != nil || saved.RunID != id2 || saved.Summary != "replacement" || len(saved.Items) != 0 {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	latest, err := s.LatestAIReportRun(ctx, r.DashboardUserID, r.Week)
	if err != nil || latest.ID != id2 {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
	id3, err := s.CreateAIReportRun(ctx, &r)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FailAIReportRun(ctx, id3, "completed", "bad", "bad"); err == nil {
		t.Fatal("accepted invalid failure status")
	}
	n, err := s.FailRunningAIReportRuns(ctx, "server_restarted")
	if err != nil || n != 1 {
		t.Fatalf("recovered=%d err=%v", n, err)
	}
	got, err = s.GetAIReportRun(ctx, id3)
	if err != nil || got.Status != AIRunFailed || got.ErrorCode != "server_restarted" || got.FinishedAt == nil {
		t.Fatalf("recovered=%+v err=%v", got, err)
	}
	saved, err = s.GetAIReport(ctx, r.DashboardUserID, r.Week)
	if err != nil || saved.RunID != id2 {
		t.Fatalf("previous report lost: %+v %v", saved, err)
	}
	usage, err := s.AIUsageSince(ctx, time.Now().Add(-time.Hour))
	if err != nil || usage.Runs != 3 || usage.Failed != 1 || usage.InputTokens != 10 || usage.OutputTokens != 2 {
		t.Fatalf("usage=%+v err=%v", usage, err)
	}
	empty, err := s.AIUsageSince(ctx, time.Now().Add(time.Hour))
	if err != nil || empty.Runs != 0 {
		t.Fatalf("future usage=%+v err=%v", empty, err)
	}
	for _, v := range []string{"v1", "v2"} {
		if err = s.UpsertAIConsent(ctx, r.DashboardUserID, "codex-app-server:chatgpt", v); err != nil {
			t.Fatal(err)
		}
	}
	consent, err := s.GetAIConsent(ctx, r.DashboardUserID, "codex-app-server:chatgpt")
	if err != nil || consent.DisclosureVersion != "v2" || consent.ConsentedAt.IsZero() {
		t.Fatalf("consent=%+v err=%v", consent, err)
	}
	if got, err := s.GetAIConsent(ctx, r.DashboardUserID, "other"); err != nil || got != nil {
		t.Fatalf("missing=%+v %v", got, err)
	}
	if got, err := s.GetAIReport(ctx, r.DashboardUserID, "missing"); err != nil || got != nil {
		t.Fatalf("missing=%+v %v", got, err)
	}
	if got, err := s.GetAIReportRun(ctx, -1); err != nil || got != nil {
		t.Fatalf("missing=%+v %v", got, err)
	}
	if got, err := s.LatestAIReportRun(ctx, r.DashboardUserID, "missing"); err != nil || got != nil {
		t.Fatalf("missing=%+v %v", got, err)
	}
}

func TestAIReportCompletionRollsBack(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	r := aiTestRun(t, s)
	id, err := s.CreateAIReportRun(ctx, &r)
	if err != nil {
		t.Fatal(err)
	}
	// A report constraint failure must roll back the run's terminal transition.
	if _, err = s.pool.Exec(ctx, `ALTER TABLE ai_reports ADD CONSTRAINT ai_test_reject_summary CHECK (summary <> 'reject')`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `ALTER TABLE ai_reports DROP CONSTRAINT ai_test_reject_summary`)
	})
	report := AIReport{DashboardUserID: r.DashboardUserID, Week: r.Week, TZ: r.TZ, Since: r.Since, Until: r.Until, Summary: "reject"}
	if err = s.CompleteAIReportRun(ctx, id, report, AIUsage{Reported: true, InputTokens: 12}, 0); err == nil {
		t.Fatal("expected constraint error")
	}
	got, err := s.GetAIReportRun(ctx, id)
	if err != nil || got.Status != AIRunRunning || got.FinishedAt != nil || got.Usage.Reported {
		t.Fatalf("not rolled back: %+v %v", got, err)
	}
}

func TestAIReportRunningRunAndModel(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	r := aiTestRun(t, s)
	if got, err := s.RunningAIReportRun(ctx, r.DashboardUserID); err != nil || got != nil {
		t.Fatalf("no running run = %+v err=%v", got, err)
	}
	id, err := s.CreateAIReportRun(ctx, &r)
	if err != nil {
		t.Fatal(err)
	}
	// Any week: the one-running-run rule is per user.
	got, err := s.RunningAIReportRun(ctx, r.DashboardUserID)
	if err != nil || got == nil || got.ID != id {
		t.Fatalf("running = %+v err=%v", got, err)
	}
	if err = s.SetAIRunModel(ctx, id, "gpt-resolved"); err != nil {
		t.Fatal(err)
	}
	if got, err = s.GetAIReportRun(ctx, id); err != nil || got.Model != "gpt-resolved" {
		t.Fatalf("model = %+v err=%v", got, err)
	}
	if err = s.FailAIReportRun(ctx, id, AIRunFailed, "x", ""); err != nil {
		t.Fatal(err)
	}
	if got, err := s.RunningAIReportRun(ctx, r.DashboardUserID); err != nil || got != nil {
		t.Fatalf("finished run still running = %+v err=%v", got, err)
	}
}
