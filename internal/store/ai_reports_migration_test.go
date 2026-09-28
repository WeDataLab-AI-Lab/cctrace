package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func aiTestRun(t *testing.T, s *PgStore) AIReportRun {
	t.Helper()
	u, err := s.CreateDashboardUser(context.Background(), &DashboardUser{Email: t.Name() + "@example.com", PasswordHash: "test", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	since := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	return AIReportRun{DashboardUserID: u.ID, Week: "2026-W37", TZ: "UTC", Since: since, Until: since.AddDate(0, 0, 7), Runtime: "codex-app-server", Status: AIRunRunning}
}

func TestAIReportsMigration(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	found := false
	for _, q := range migrations {
		if strings.Contains(q, "CREATE TABLE IF NOT EXISTS ai_report_runs") {
			found = true
			for i := 0; i < 2; i++ {
				if _, err := s.pool.Exec(ctx, q); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if !found {
		t.Fatal("AI migration missing")
	}
	for _, name := range []string{"ai_report_runs", "ai_report_tool_calls", "ai_reports", "ai_consents", "uq_ai_report_runs_one_running", "idx_ai_report_runs_user_week", "idx_ai_report_runs_started"} {
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists); err != nil || !exists {
			t.Fatalf("%s exists=%v err=%v", name, exists, err)
		}
	}
	r := aiTestRun(t, s)
	id, err := s.CreateAIReportRun(ctx, &r)
	if err != nil {
		t.Fatal(err)
	}
	r.Week = "2026-W38"
	if _, err = s.CreateAIReportRun(ctx, &r); !errors.Is(err, ErrAIRunAlreadyRunning) {
		t.Fatalf("duplicate running: %v", err)
	}
	if err = s.FailAIReportRun(ctx, id, AIRunFailed, "test", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateAIReportRun(ctx, &r); err != nil {
		t.Fatal(err)
	}
}
