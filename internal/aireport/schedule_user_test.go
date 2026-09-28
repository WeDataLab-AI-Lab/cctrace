package aireport

import (
	"context"
	"errors"
	"testing"
	"time"

	"cctrace/internal/store"
)

// With nothing of their own a user follows the admin, and the screen is told
// so, because "관리자 기본값" and "내가 정한 시각" read differently to a user
// deciding whether to change it.
func TestUserScheduleFollowsAdminUntilSet(t *testing.T) {
	f := tickFixture(t)
	ctx := context.Background()
	on := true
	weekday, hour := int(time.Wednesday), 9
	if err := f.svc.SetAutoSchedule(ctx, &on, &weekday, &hour, nil, "admin@example.com"); err != nil {
		t.Fatal(err)
	}

	got, err := f.svc.UserSchedule(ctx, 1, "Asia/Seoul")
	if err != nil {
		t.Fatalf("UserSchedule: %v", err)
	}
	if !got.Enabled || got.EnabledSource != SourceAdmin || got.WhenSource != SourceAdmin {
		t.Fatalf("got %+v, want the admin's", got)
	}
	if got.Weekday != time.Wednesday || got.Hour != 9 {
		t.Fatalf("when = %s %02d:%02d", got.Weekday, got.Hour, got.Minute)
	}
	// No row of their own, so the caller's zone stands in.
	if got.TZ != "Asia/Seoul" {
		t.Fatalf("TZ = %q", got.TZ)
	}
}

// The zone the screen prints has to be the zone the firing is read in.
//
// The two were found separately: the weekly handler passed the browser's zone
// from the query string, while the scheduler falls back to the zone of the
// user's latest report. A Seoul user who had never saved a schedule was shown
// "매주 월요일 06:00" and fired at 06:00 UTC -- nine hours off, with nothing
// reporting an error. The service decides the fallback once so both callers
// get the same answer; what the caller offers is only used when nothing is
// recorded.
func TestUserScheduleIgnoresCallerZoneWhenOneIsRecorded(t *testing.T) {
	f := tickFixture(t)
	ctx := context.Background()
	since := f.wk.Since

	// A report this user already has, carrying the zone it was generated in.
	f.st.Reports[memKey(1, f.wk.ID)] = &store.AIReport{
		DashboardUserID: 1, Week: f.wk.ID, TZ: "America/New_York",
		Since: since, Until: since.AddDate(0, 0, 7), GeneratedAt: since,
	}

	got, err := f.svc.UserSchedule(ctx, 1, "Asia/Seoul")
	if err != nil {
		t.Fatalf("UserSchedule: %v", err)
	}
	if got.TZ != "America/New_York" {
		t.Fatalf("TZ = %q, want the recorded zone -- the screen would print a firing the ticker never makes", got.TZ)
	}
}

// An administrator turning automatic runs off turns them off for everyone.
//
// The switch is the one field where the user does not win. A run sends session
// text to an outside provider and spends tokens, so an administrator has to be
// able to stop that for the organisation; a per-user switch that outranks them
// leaves no way to do it. The weekday and time stay the user's own.
func TestAdminOffBeatsUserOn(t *testing.T) {
	f := tickFixture(t)
	ctx := context.Background()
	off, on := false, true

	if err := f.svc.SetAutoSchedule(ctx, &off, nil, nil, nil, "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	hour := 21
	got, err := f.svc.SetUserSchedule(ctx, 1, &on, nil, &hour, nil, "Asia/Seoul")
	if err != nil {
		t.Fatalf("SetUserSchedule: %v", err)
	}
	if got.Enabled {
		t.Fatalf("a user's switch outranked the administrator's off: %+v", got)
	}
	if got.EnabledSource != SourceAdmin {
		t.Fatalf("EnabledSource = %s, want %s", got.EnabledSource, SourceAdmin)
	}
	// The time they chose is still theirs.
	if got.Hour != 21 || got.WhenSource != SourceUser {
		t.Fatalf("when = %02d:%02d (%s)", got.Hour, got.Minute, got.WhenSource)
	}
}

// A user moves one part and keeps following the admin for the rest, and the
// zone they saved is the one their firing is read in from then on.
func TestSetUserScheduleOverridesItemByItem(t *testing.T) {
	f := tickFixture(t)
	ctx := context.Background()
	on := true
	weekday, adminHour := int(time.Wednesday), 9
	if err := f.svc.SetAutoSchedule(ctx, &on, &weekday, &adminHour, nil, "admin@example.com"); err != nil {
		t.Fatal(err)
	}

	hour := 21
	got, err := f.svc.SetUserSchedule(ctx, 1, nil, nil, &hour, nil, "Asia/Seoul")
	if err != nil {
		t.Fatalf("SetUserSchedule: %v", err)
	}
	if got.Hour != 21 || got.WhenSource != SourceUser {
		t.Fatalf("hour = %d (%s)", got.Hour, got.WhenSource)
	}
	if got.Weekday != time.Wednesday {
		t.Fatalf("weekday = %s, want the admin's Wednesday", got.Weekday)
	}
	if !got.Enabled || got.EnabledSource != SourceAdmin {
		t.Fatalf("switch = %v (%s), want the admin's", got.Enabled, got.EnabledSource)
	}
	if got.TZ != "Asia/Seoul" {
		t.Fatalf("TZ = %q", got.TZ)
	}

	// Read back with a different fallback: the saved zone wins.
	reread, err := f.svc.UserSchedule(ctx, 1, "America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	if reread.TZ != "Asia/Seoul" || reread.Hour != 21 {
		t.Fatalf("reread = %+v", reread)
	}
}

// A user can turn their own off while the admin leaves it on.
func TestSetUserScheduleCanTurnItOff(t *testing.T) {
	f := tickFixture(t)
	ctx := context.Background()
	on, off := true, false
	if err := f.svc.SetAutoSchedule(ctx, &on, nil, nil, nil, "admin@example.com"); err != nil {
		t.Fatal(err)
	}

	got, err := f.svc.SetUserSchedule(ctx, 1, &off, nil, nil, nil, "UTC")
	if err != nil {
		t.Fatalf("SetUserSchedule: %v", err)
	}
	if got.Enabled || got.EnabledSource != SourceUser {
		t.Fatalf("switch = %v (%s)", got.Enabled, got.EnabledSource)
	}
}

// Anything the firing rule cannot read is refused before it is stored: a
// weekday or time out of range, and a zone that does not load.
func TestSetUserScheduleRejectsInvalid(t *testing.T) {
	ctx := context.Background()
	bad := 99
	tests := []struct {
		name                  string
		weekday, hour, minute *int
		tz                    string
	}{
		{"weekday out of range", &bad, nil, nil, "UTC"},
		{"hour out of range", nil, &bad, nil, "UTC"},
		{"minute out of range", nil, nil, &bad, "UTC"},
		{"zone that does not load", nil, nil, nil, "Not/AZone"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := tickFixture(t)
			_, err := f.svc.SetUserSchedule(ctx, 1, nil, tt.weekday, tt.hour, tt.minute, tt.tz)
			if !errors.Is(err, ErrInvalidSchedule) {
				t.Fatalf("err = %v, want ErrInvalidSchedule", err)
			}
			if len(f.st.UserSchedules) != 0 {
				t.Fatalf("a refused schedule was stored: %+v", f.st.UserSchedules)
			}
		})
	}
}

// The resolved value drives the screen's "next run", so the two fit together
// without the caller rebuilding a Schedule by hand.
func TestUserScheduleFeedsNextRun(t *testing.T) {
	f := tickFixture(t)
	ctx := context.Background()
	on := true
	if err := f.svc.SetAutoSchedule(ctx, &on, nil, nil, nil, "admin@example.com"); err != nil {
		t.Fatal(err)
	}

	got, err := f.svc.UserSchedule(ctx, 1, "Asia/Seoul")
	if err != nil {
		t.Fatal(err)
	}
	seoul, _ := time.LoadLocation("Asia/Seoul")
	at, err := got.NextRun(time.Date(2026, 9, 14, 5, 0, 0, 0, seoul))
	if err != nil {
		t.Fatal(err)
	}
	if !at.Equal(time.Date(2026, 9, 14, 6, 0, 0, 0, seoul)) {
		t.Fatalf("NextRun = %s", at)
	}
}
