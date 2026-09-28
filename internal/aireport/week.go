package aireport

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// ErrInvalidWeek is returned for a malformed week id, a week that does not exist
// in its year, or an unknown time zone.
var ErrInvalidWeek = errors.New("invalid week")

var weekIDPattern = regexp.MustCompile(`^(\d{4})-W(\d{2})$`)

// Week is one ISO 8601 week in a time zone: Monday 00:00 to the next Monday
// 00:00, local time. The report key is (user, ID); TZ only moves the bounds.
type Week struct {
	ID    string
	TZ    string
	Since time.Time
	Until time.Time
}

// ParseISOWeek resolves "2026-W37" in tz (empty means UTC) to [Since, Until).
// The calendar arithmetic runs on UTC dates; only the local midnight conversion
// consults tz, so a DST week is 167 or 169 hours long.
func ParseISOWeek(id, tz string) (Week, error) {
	m := weekIDPattern.FindStringSubmatch(id)
	if m == nil {
		return Week{}, fmt.Errorf("%w: %q", ErrInvalidWeek, id)
	}
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return Week{}, fmt.Errorf("%w: time zone %q", ErrInvalidWeek, tz)
	}
	year, _ := strconv.Atoi(m[1])
	week, _ := strconv.Atoi(m[2])
	jan4 := time.Date(year, time.January, 4, 0, 0, 0, 0, time.UTC)
	monday := jan4.AddDate(0, 0, -((int(jan4.Weekday())+6)%7)+(week-1)*7)
	// Week 53 exists only in some years; past the last week the date rolls over.
	if y, w := monday.ISOWeek(); week < 1 || y != year || w != week {
		return Week{}, fmt.Errorf("%w: %q", ErrInvalidWeek, id)
	}
	since := time.Date(monday.Year(), monday.Month(), monday.Day(), 0, 0, 0, 0, loc)
	return Week{ID: id, TZ: tz, Since: since, Until: since.AddDate(0, 0, 7)}, nil
}

// WeekContaining returns the week holding at, as seen in tz.
func WeekContaining(at time.Time, tz string) (Week, error) {
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return Week{}, fmt.Errorf("%w: time zone %q", ErrInvalidWeek, tz)
	}
	y, w := at.In(loc).ISOWeek()
	return ParseISOWeek(fmt.Sprintf("%04d-W%02d", y, w), tz)
}

// Previous is the ISO week before w in the same time zone.
func (w Week) Previous() Week {
	prev, err := WeekContaining(w.Since.AddDate(0, 0, -7), w.TZ)
	if err != nil {
		// w came from ParseISOWeek, so its zone already loaded.
		panic(err)
	}
	return prev
}

// IsFuture reports whether the week has not started at now.
func (w Week) IsFuture(now time.Time) bool { return now.Before(w.Since) }

// InProgress reports whether now falls inside the week.
func (w Week) InProgress(now time.Time) bool {
	return !now.Before(w.Since) && now.Before(w.Until)
}
