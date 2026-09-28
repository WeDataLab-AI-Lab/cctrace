package aireport

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The admin's default is read back as saved, and each field keeps its own
// meaning: a nil is no admin word, not a zero.
func TestSetAutoScheduleRoundTrip(t *testing.T) {
	f := tickFixture(t)
	ctx := context.Background()

	got, err := f.svc.AutoSchedule(ctx)
	if err != nil {
		t.Fatalf("AutoSchedule: %v", err)
	}
	if got.Enabled || got.EnabledSource != SourceDefault {
		t.Fatalf("nothing saved = %+v, want the built-in default", got)
	}
	if got.Weekday != defaultScheduleWeekday || got.Hour != defaultScheduleHour {
		t.Fatalf("default when = %s %02d:%02d", got.Weekday, got.Hour, got.Minute)
	}

	on := true
	weekday, hour, minute := int(time.Wednesday), 15, 30
	if err := f.svc.SetAutoSchedule(ctx, &on, &weekday, &hour, &minute, "admin@example.com"); err != nil {
		t.Fatalf("SetAutoSchedule: %v", err)
	}
	got, err = f.svc.AutoSchedule(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.EnabledSource != SourceAdmin {
		t.Fatalf("enabled = %v (%s)", got.Enabled, got.EnabledSource)
	}
	if got.Weekday != time.Wednesday || got.Hour != 15 || got.Minute != 30 || got.WhenSource != SourceAdmin {
		t.Fatalf("when = %s %02d:%02d (%s)", got.Weekday, got.Hour, got.Minute, got.WhenSource)
	}

	// All nil hands the schedule back to the built-in default.
	if err := f.svc.SetAutoSchedule(ctx, nil, nil, nil, nil, "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	if got, _ = f.svc.AutoSchedule(ctx); got.EnabledSource != SourceDefault || got.WhenSource != SourceDefault {
		t.Fatalf("cleared = %+v", got)
	}
}

// A weekday or time outside its range is refused before it is stored, so the
// scheduler never reads a row it cannot turn into a firing.
func TestSetAutoScheduleRejectsOutOfRange(t *testing.T) {
	f := tickFixture(t)
	ctx := context.Background()
	on := true

	for _, tc := range []struct {
		name                  string
		weekday, hour, minute int
	}{
		{"weekday above saturday", 7, 6, 0},
		{"negative weekday", -1, 6, 0},
		{"hour 24", 1, 24, 0},
		{"negative hour", 1, -1, 0},
		{"minute 60", 1, 6, 60},
		{"negative minute", 1, 6, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, h, m := tc.weekday, tc.hour, tc.minute
			err := f.svc.SetAutoSchedule(ctx, &on, &w, &h, &m, "admin@example.com")
			if !errors.Is(err, ErrInvalidSchedule) {
				t.Fatalf("err = %v, want ErrInvalidSchedule", err)
			}
			if f.st.AutoSchedule != nil {
				t.Fatalf("a refused schedule was stored: %+v", f.st.AutoSchedule)
			}
		})
	}
}
