package aireport

import (
	"errors"
	"testing"
	"time"
)

func TestParseISOWeek(t *testing.T) {
	seoul, _ := time.LoadLocation("Asia/Seoul")
	ny, _ := time.LoadLocation("America/New_York")
	tests := []struct {
		name, id, tz string
		since, until time.Time
	}{
		{"mid year seoul", "2026-W37", "Asia/Seoul",
			time.Date(2026, 9, 7, 0, 0, 0, 0, seoul), time.Date(2026, 9, 14, 0, 0, 0, 0, seoul)},
		{"week 1 starts in previous year", "2026-W01", "UTC",
			time.Date(2025, 12, 29, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)},
		{"W53 in a long year", "2020-W53", "UTC",
			time.Date(2020, 12, 28, 0, 0, 0, 0, time.UTC), time.Date(2021, 1, 4, 0, 0, 0, 0, time.UTC)},
		{"DST week lasts 167 hours", "2026-W11", "America/New_York",
			time.Date(2026, 3, 9, 0, 0, 0, 0, ny), time.Date(2026, 3, 16, 0, 0, 0, 0, ny)},
		{"empty tz is UTC", "2026-W37", "",
			time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wk, err := ParseISOWeek(tt.id, tt.tz)
			if err != nil {
				t.Fatal(err)
			}
			if !wk.Since.Equal(tt.since) || !wk.Until.Equal(tt.until) {
				t.Fatalf("got [%s, %s), want [%s, %s)", wk.Since, wk.Until, tt.since, tt.until)
			}
			if wk.ID != tt.id {
				t.Fatalf("ID = %q", wk.ID)
			}
		})
	}
}

func TestParseISOWeekRejectsInvalid(t *testing.T) {
	for _, tc := range []struct{ id, tz string }{
		{"2026-37", "UTC"},
		{"2026-W00", "UTC"},
		{"2026-W54", "UTC"},
		{"2021-W53", "UTC"}, // 2021 has 52 weeks
		{"26-W01", "UTC"},
		{"2026-W37", "Not/AZone"},
		{"2026-W37 ", "UTC"},
	} {
		if _, err := ParseISOWeek(tc.id, tc.tz); !errors.Is(err, ErrInvalidWeek) {
			t.Errorf("ParseISOWeek(%q, %q) err = %v, want ErrInvalidWeek", tc.id, tc.tz, err)
		}
	}
}

func TestWeekPreviousAndFuture(t *testing.T) {
	wk, err := ParseISOWeek("2026-W01", "Asia/Seoul")
	if err != nil {
		t.Fatal(err)
	}
	prev := wk.Previous()
	if prev.ID != "2025-W52" || !prev.Until.Equal(wk.Since) {
		t.Fatalf("previous = %+v", prev)
	}
	if wk.IsFuture(wk.Since) || !wk.IsFuture(wk.Since.Add(-time.Second)) {
		t.Fatal("IsFuture boundary wrong")
	}
	if !wk.InProgress(wk.Since) || wk.InProgress(wk.Until) {
		t.Fatal("InProgress boundary wrong")
	}
}
