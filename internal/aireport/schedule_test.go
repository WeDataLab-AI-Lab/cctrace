package aireport

import (
	"errors"
	"testing"
	"time"

	"cctrace/internal/store"
)

func intPtr(i int) *int { return &i }

func mustWeek(t *testing.T, id, tz string) Week {
	t.Helper()
	w, err := ParseISOWeek(id, tz)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// A scheduled run covers the week that has ended, so its firing is pinned to
// the first weekday+time at or after that week's end. One rule covers every
// weekday a user might pick, including the Monday the week ends on.
func TestScheduleFireTime(t *testing.T) {
	seoul, _ := time.LoadLocation("Asia/Seoul")
	tests := []struct {
		name string
		s    Schedule
		week string
		want time.Time
	}{
		{"monday morning fires the monday the week ends",
			Schedule{Weekday: time.Monday, Hour: 6, TZ: "Asia/Seoul"}, "2026-W37",
			time.Date(2026, 9, 14, 6, 0, 0, 0, seoul)},
		{"midnight monday is the boundary itself",
			Schedule{Weekday: time.Monday, Hour: 0, TZ: "Asia/Seoul"}, "2026-W37",
			time.Date(2026, 9, 14, 0, 0, 0, 0, seoul)},
		{"midweek fires inside the following week",
			Schedule{Weekday: time.Wednesday, Hour: 15, Minute: 30, TZ: "Asia/Seoul"}, "2026-W37",
			time.Date(2026, 9, 16, 15, 30, 0, 0, seoul)},
		{"sunday is the last weekday to come round",
			Schedule{Weekday: time.Sunday, Hour: 23, TZ: "Asia/Seoul"}, "2026-W37",
			time.Date(2026, 9, 20, 23, 0, 0, 0, seoul)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.s.FireTime(mustWeek(t, tt.week, tt.s.TZ))
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(tt.want) {
				t.Fatalf("FireTime = %s, want %s", got, tt.want)
			}
		})
	}
}

// The scheduler ticks every minute and asks which week it owes a run for. The
// answer must not change to the next week one second early, or a week is
// skipped entirely.
func TestScheduleDueWeek(t *testing.T) {
	seoul, _ := time.LoadLocation("Asia/Seoul")
	s := Schedule{Weekday: time.Monday, Hour: 6, TZ: "Asia/Seoul"}
	tests := []struct {
		name string
		now  time.Time
		want string
	}{
		{"a second before the firing the earlier week is still owed",
			time.Date(2026, 9, 14, 5, 59, 59, 0, seoul), "2026-W36"},
		{"at the firing the week that just ended comes due",
			time.Date(2026, 9, 14, 6, 0, 0, 0, seoul), "2026-W37"},
		{"it stays due for the rest of the week",
			time.Date(2026, 9, 20, 23, 59, 0, 0, seoul), "2026-W37"},
		{"the next firing moves it on",
			time.Date(2026, 9, 21, 6, 0, 0, 0, seoul), "2026-W38"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.DueWeek(tt.now)
			if err != nil {
				t.Fatal(err)
			}
			if got.ID != tt.want {
				t.Fatalf("DueWeek = %q, want %q", got.ID, tt.want)
			}
		})
	}
}

// The weekly screen prints this, so it is the next firing and never one that
// has already passed.
func TestScheduleNextRun(t *testing.T) {
	seoul, _ := time.LoadLocation("Asia/Seoul")
	s := Schedule{Weekday: time.Monday, Hour: 6, TZ: "Asia/Seoul"}
	tests := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{"before today's firing it is today",
			time.Date(2026, 9, 14, 5, 59, 0, 0, seoul), time.Date(2026, 9, 14, 6, 0, 0, 0, seoul)},
		{"on the firing it is already the next one",
			time.Date(2026, 9, 14, 6, 0, 0, 0, seoul), time.Date(2026, 9, 21, 6, 0, 0, 0, seoul)},
		{"mid week it is the coming monday",
			time.Date(2026, 9, 17, 12, 0, 0, 0, seoul), time.Date(2026, 9, 21, 6, 0, 0, 0, seoul)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.NextRun(tt.now)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(tt.want) {
				t.Fatalf("NextRun = %s, want %s", got, tt.want)
			}
		})
	}
}

// Wall-clock time is what a user set, so a firing keeps its local hour across a
// daylight saving change rather than drifting by an hour.
func TestScheduleKeepsLocalHourAcrossDST(t *testing.T) {
	s := Schedule{Weekday: time.Monday, Hour: 6, TZ: "America/New_York"}
	// US DST began Sunday 2026-03-08, inside 2026-W10.
	for _, id := range []string{"2026-W09", "2026-W10", "2026-W11"} {
		got, err := s.FireTime(mustWeek(t, id, s.TZ))
		if err != nil {
			t.Fatal(err)
		}
		if got.Hour() != 6 || got.Minute() != 0 {
			t.Fatalf("%s fired at %s, want local 06:00", id, got)
		}
		if got.Weekday() != time.Monday {
			t.Fatalf("%s fired on %s", id, got.Weekday())
		}
	}
}

func TestScheduleRejectsInvalid(t *testing.T) {
	for _, s := range []Schedule{
		{Weekday: time.Monday, Hour: 6, TZ: "Not/AZone"},
		{Weekday: time.Monday, Hour: 24, TZ: "UTC"},
		{Weekday: time.Monday, Hour: -1, TZ: "UTC"},
		{Weekday: time.Monday, Hour: 6, Minute: 60, TZ: "UTC"},
		{Weekday: time.Monday, Hour: 6, Minute: -1, TZ: "UTC"},
		{Weekday: time.Weekday(7), Hour: 6, TZ: "UTC"},
	} {
		if _, err := s.NextRun(time.Now()); !errors.Is(err, ErrInvalidSchedule) {
			t.Errorf("NextRun(%+v) err = %v, want ErrInvalidSchedule", s, err)
		}
	}
}

// Nothing saved anywhere still yields a usable schedule, and it is off: an
// admin turns automatic runs on, they do not arrive by default.
func TestResolveScheduleDefaults(t *testing.T) {
	got := ResolveSchedule(nil, nil, "")
	if got.Enabled || got.EnabledSource != SourceDefault {
		t.Fatalf("enabled = %v (%s), want off by default", got.Enabled, got.EnabledSource)
	}
	if got.Weekday != time.Monday || got.Hour != 6 || got.Minute != 0 {
		t.Fatalf("when = %s %02d:%02d, want Monday 06:00", got.Weekday, got.Hour, got.Minute)
	}
	if got.WhenSource != SourceDefault || got.TZ != "" {
		t.Fatalf("got %+v", got)
	}
}

// The layers are user over admin over built-in, item by item: a user who moved
// only the hour still follows a later change of weekday by the admin.
func TestResolveScheduleLayers(t *testing.T) {
	admin := &store.AIAutoSchedule{
		Enabled: boolPtr(true), Weekday: intPtr(int(time.Wednesday)), Hour: intPtr(9), Minute: intPtr(0),
	}
	tests := []struct {
		name          string
		user          *store.AIUserSchedule
		enabled       bool
		enabledSource string
		weekday       time.Weekday
		hour, minute  int
		whenSource    string
	}{
		{"no override follows the admin", nil,
			true, SourceAdmin, time.Wednesday, 9, 0, SourceAdmin},
		{"hour only keeps the admin's weekday",
			&store.AIUserSchedule{Hour: intPtr(21)},
			true, SourceAdmin, time.Wednesday, 21, 0, SourceUser},
		{"a user can move the whole thing",
			&store.AIUserSchedule{Weekday: intPtr(int(time.Friday)), Hour: intPtr(18), Minute: intPtr(30)},
			true, SourceAdmin, time.Friday, 18, 30, SourceUser},
		{"a user can turn it off while the admin leaves it on",
			&store.AIUserSchedule{Enabled: boolPtr(false)},
			false, SourceUser, time.Wednesday, 9, 0, SourceAdmin},
		{"a nil switch keeps following the admin",
			&store.AIUserSchedule{Hour: intPtr(7)},
			true, SourceAdmin, time.Wednesday, 7, 0, SourceUser},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveSchedule(admin, tt.user, "")
			if got.Enabled != tt.enabled || got.EnabledSource != tt.enabledSource {
				t.Fatalf("enabled = %v (%s), want %v (%s)", got.Enabled, got.EnabledSource, tt.enabled, tt.enabledSource)
			}
			if got.Weekday != tt.weekday || got.Hour != tt.hour || got.Minute != tt.minute {
				t.Fatalf("when = %s %02d:%02d, want %s %02d:%02d", got.Weekday, got.Hour, got.Minute, tt.weekday, tt.hour, tt.minute)
			}
			if got.WhenSource != tt.whenSource {
				t.Fatalf("WhenSource = %s, want %s", got.WhenSource, tt.whenSource)
			}
		})
	}
}

// The zone is the user's own. Their saved row carries it; without one the
// caller's fallback -- the zone of their last report -- stands in.
func TestResolveScheduleTimeZone(t *testing.T) {
	tests := []struct {
		name     string
		user     *store.AIUserSchedule
		fallback string
		want     string
	}{
		{"the saved row wins", &store.AIUserSchedule{TZ: "Asia/Seoul"}, "America/New_York", "Asia/Seoul"},
		{"no row falls back", nil, "America/New_York", "America/New_York"},
		{"a row without a zone falls back", &store.AIUserSchedule{Hour: intPtr(8)}, "Asia/Seoul", "Asia/Seoul"},
		{"nothing at all stays empty, which FireTime reads as UTC", nil, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveSchedule(nil, tt.user, tt.fallback); got.TZ != tt.want {
				t.Fatalf("TZ = %q, want %q", got.TZ, tt.want)
			}
		})
	}
}

// The resolved value is the one the firing rule runs on, so the two fit
// together without the caller rebuilding a Schedule by hand.
func TestResolvedScheduleFeedsNextRun(t *testing.T) {
	seoul, _ := time.LoadLocation("Asia/Seoul")
	got := ResolveSchedule(
		&store.AIAutoSchedule{Enabled: boolPtr(true)},
		&store.AIUserSchedule{TZ: "Asia/Seoul"},
		"",
	)
	at, err := got.NextRun(time.Date(2026, 9, 14, 5, 0, 0, 0, seoul))
	if err != nil {
		t.Fatal(err)
	}
	if !at.Equal(time.Date(2026, 9, 14, 6, 0, 0, 0, seoul)) {
		t.Fatalf("NextRun = %s", at)
	}
}

// An empty zone means UTC, as it does for a week id.
func TestScheduleEmptyTZIsUTC(t *testing.T) {
	s := Schedule{Weekday: time.Monday, Hour: 6}
	got, err := s.FireTime(mustWeek(t, "2026-W37", ""))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(time.Date(2026, 9, 14, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("FireTime = %s", got)
	}
}
