package store

import (
	"context"
	"testing"
	"time"
)

// The scheduler fires at most once per user per week, and it decides by asking
// whether its own run already exists. A run the user started by hand must not
// answer yes: one manual report would otherwise swallow that week's automatic
// one, and a failed automatic run must still be distinguishable from no run.
func TestScheduledAIRunExists(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	uid := insertScheduleUser(t, s, "sched@example.test")

	newRun := func(week, startedBy string) *AIReportRun {
		since := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
		return &AIReportRun{
			DashboardUserID: uid, Week: week, TZ: "Asia/Seoul",
			Since: since, Until: since.AddDate(0, 0, 7),
			Status: AIRunCompleted, Runtime: "codex-app-server", StartedBy: startedBy,
		}
	}

	ok, err := s.ScheduledAIRunExists(ctx, uid, "2026-W37")
	if err != nil || ok {
		t.Fatalf("no runs = %v, %v", ok, err)
	}

	// Started by hand: not the scheduler's own.
	if _, err := s.CreateAIReportRun(ctx, newRun("2026-W37", AIRunStartedByManual)); err != nil {
		t.Fatalf("create manual run: %v", err)
	}
	if ok, err = s.ScheduledAIRunExists(ctx, uid, "2026-W37"); err != nil || ok {
		t.Fatalf("manual run answered as the scheduler's own: %v, %v", ok, err)
	}

	// The scheduler's own run, even though it failed, stops a second firing.
	failed := newRun("2026-W37", AIRunStartedBySchedule)
	failed.Status = AIRunFailed
	if _, err := s.CreateAIReportRun(ctx, failed); err != nil {
		t.Fatalf("create scheduled run: %v", err)
	}
	if ok, err = s.ScheduledAIRunExists(ctx, uid, "2026-W37"); err != nil || !ok {
		t.Fatalf("scheduled run = %v, %v", ok, err)
	}

	// It is per week and per user.
	if ok, _ = s.ScheduledAIRunExists(ctx, uid, "2026-W38"); ok {
		t.Fatal("another week answered yes")
	}
	other := insertScheduleUser(t, s, "sched2@example.test")
	if ok, _ = s.ScheduledAIRunExists(ctx, other, "2026-W37"); ok {
		t.Fatal("another user answered yes")
	}
}

// Every active user is a candidate: automatic runs are on for everyone once an
// admin turns them on, so the query does not pre-filter by consent or by having
// saved a schedule. Deactivated accounts drop out -- they cannot log in to read
// the report either.
func TestListAIScheduleCandidates(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	plain := insertScheduleUser(t, s, "plain@example.test")
	custom := insertScheduleUser(t, s, "custom@example.test")
	gone := insertScheduleUser(t, s, "gone@example.test")
	if _, err := s.pool.Exec(ctx, `UPDATE dashboard_users SET is_active = false WHERE id = $1`, gone); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertAIUserSchedule(ctx, AIUserSchedule{
		DashboardUserID: custom, Enabled: boolPtr(true), Weekday: intPtr(5), TZ: "Asia/Seoul",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListAIScheduleCandidates(ctx)
	if err != nil {
		t.Fatalf("ListAIScheduleCandidates: %v", err)
	}
	by := map[int64]AIScheduleCandidate{}
	for _, c := range got {
		by[c.DashboardUserID] = c
	}
	if len(got) != 2 {
		t.Fatalf("candidates = %d, want 2 (the deactivated one is out)", len(got))
	}
	if _, ok := by[gone]; ok {
		t.Fatal("a deactivated user is still a candidate")
	}

	// No saved row: the user still comes through, following the admin's default.
	if c := by[plain]; c.Schedule != nil || c.Email != "plain@example.test" || c.CctraceUserID != "plain@example.test" {
		t.Fatalf("plain = %+v", c)
	}
	if c := by[custom]; c.Schedule == nil || c.Schedule.Weekday == nil || *c.Schedule.Weekday != 5 || c.Schedule.TZ != "Asia/Seoul" {
		t.Fatalf("custom = %+v", by[custom].Schedule)
	}
}

// Without a saved zone the scheduler reads the weekday and time in the zone of
// the user's latest report, because that is the only one the server has seen.
func TestListAIScheduleCandidatesFallbackTZ(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	uid := insertScheduleUser(t, s, "tz@example.test")

	since := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	runID, err := s.CreateAIReportRun(ctx, &AIReportRun{
		DashboardUserID: uid, Week: "2026-W37", TZ: "America/New_York",
		Since: since, Until: since.AddDate(0, 0, 7),
		Status: AIRunCompleted, Runtime: "codex-app-server",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO ai_reports (dashboard_user_id, week, run_id, tz, since, until, summary, items)
		VALUES ($1, $2, $3, $4, $5, $6, 's', '[]'::jsonb)`,
		uid, "2026-W37", runID, "America/New_York", since, since.AddDate(0, 0, 7)); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListAIScheduleCandidates(ctx)
	if err != nil || len(got) != 1 {
		t.Fatalf("candidates = %+v, %v", got, err)
	}
	if got[0].FallbackTZ != "America/New_York" {
		t.Fatalf("FallbackTZ = %q", got[0].FallbackTZ)
	}
}

// A run created without saying who started it is manual: that is what every row
// written before this column existed was.
func TestAIRunStartedByDefaultsToManual(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	uid := insertScheduleUser(t, s, "default@example.test")

	since := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	id, err := s.CreateAIReportRun(ctx, &AIReportRun{
		DashboardUserID: uid, Week: "2026-W37", TZ: "UTC",
		Since: since, Until: since.AddDate(0, 0, 7),
		Status: AIRunCompleted, Runtime: "codex-app-server",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetAIReportRun(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if got.StartedBy != AIRunStartedByManual {
		t.Fatalf("StartedBy = %q, want %q", got.StartedBy, AIRunStartedByManual)
	}
}

func boolPtr(b bool) *bool { return &b }

// insertScheduleUser makes a dashboard user for the schedule's foreign key.
func insertScheduleUser(t *testing.T, s *PgStore, email string) int64 {
	t.Helper()
	var id int64
	err := s.pool.QueryRow(context.Background(), `
		INSERT INTO dashboard_users (email, password_hash, role, cctrace_user_id)
		VALUES ($1, 'x', 'user', $2) RETURNING id`, email, email).Scan(&id)
	if err != nil {
		t.Fatalf("insert dashboard user: %v", err)
	}
	return id
}

// The admin's default lives on the same single row as the runtime choice and the
// on/off switch, so saving one must not wipe the others.
func TestAIAutoScheduleRoundTrip(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	// No row is "no admin word", not an error.
	got, err := s.GetAIAutoSchedule(ctx)
	if err != nil || got != nil {
		t.Fatalf("empty = %+v, %v", got, err)
	}

	if err := s.SetAIAutoSchedule(ctx, boolPtr(true), intPtr(1), intPtr(6), intPtr(30), "admin@example.com"); err != nil {
		t.Fatalf("SetAIAutoSchedule: %v", err)
	}
	got, err = s.GetAIAutoSchedule(ctx)
	if err != nil || got == nil {
		t.Fatalf("after set = %+v, %v", got, err)
	}
	if got.Enabled == nil || !*got.Enabled || got.Weekday == nil || *got.Weekday != 1 ||
		got.Hour == nil || *got.Hour != 6 || got.Minute == nil || *got.Minute != 30 {
		t.Fatalf("schedule = %+v", got)
	}
	if got.UpdatedBy != "admin@example.com" || got.UpdatedAt.IsZero() {
		t.Fatalf("actor = %+v", got)
	}

	// The runtime choice shares the row; writing the schedule keeps it.
	if err := s.SetAIRuntimeChoice(ctx, "codex-app-server", "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAIAutoSchedule(ctx, boolPtr(false), intPtr(3), intPtr(15), intPtr(0), "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	choice, err := s.GetAIRuntimeChoice(ctx)
	if err != nil || choice == nil || choice.Runtime != "codex-app-server" {
		t.Fatalf("runtime choice after schedule write = %+v, %v", choice, err)
	}

	// nil clears the admin's word and leaves the environment deciding.
	if err := s.SetAIAutoSchedule(ctx, nil, nil, nil, nil, "other@example.com"); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetAIAutoSchedule(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("cleared = %+v", got)
	}
}

// A user's own row overrides the admin default field by field: a NULL means
// "keep following the default", not "off" or "midnight".
func TestAIUserScheduleRoundTrip(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	uid := insertScheduleUser(t, s, "user1@example.test")

	got, err := s.GetAIUserSchedule(ctx, uid)
	if err != nil || got != nil {
		t.Fatalf("empty = %+v, %v", got, err)
	}

	if err := s.UpsertAIUserSchedule(ctx, AIUserSchedule{
		DashboardUserID: uid, Enabled: boolPtr(true),
		Weekday: intPtr(3), Hour: intPtr(15), Minute: intPtr(0), TZ: "Asia/Seoul",
	}); err != nil {
		t.Fatalf("UpsertAIUserSchedule: %v", err)
	}
	got, err = s.GetAIUserSchedule(ctx, uid)
	if err != nil || got == nil {
		t.Fatalf("after upsert = %+v, %v", got, err)
	}
	if got.DashboardUserID != uid || got.Weekday == nil || *got.Weekday != 3 ||
		got.Hour == nil || *got.Hour != 15 || got.TZ != "Asia/Seoul" || got.UpdatedAt.IsZero() {
		t.Fatalf("schedule = %+v", got)
	}

	// A second write replaces the row; a nil field goes back to following the default.
	if err := s.UpsertAIUserSchedule(ctx, AIUserSchedule{
		DashboardUserID: uid, Enabled: boolPtr(false), TZ: "UTC",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetAIUserSchedule(ctx, uid)
	if got.Enabled == nil || *got.Enabled || got.Weekday != nil || got.Hour != nil || got.Minute != nil || got.TZ != "UTC" {
		t.Fatalf("replaced = %+v", got)
	}

	// One user's schedule never shows up as another's.
	other := insertScheduleUser(t, s, "user2@example.test")
	if got, _ = s.GetAIUserSchedule(ctx, other); got != nil {
		t.Fatalf("other user = %+v", got)
	}

	var rows int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM ai_report_schedules`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("rows = %d, %v", rows, err)
	}
}
