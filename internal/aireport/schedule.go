package aireport

import (
	"errors"
	"fmt"
	"time"

	"cctrace/internal/store"
)

// ErrInvalidSchedule is returned for a weekday outside Sunday..Saturday, a time
// outside 00:00..23:59, or an unknown time zone.
var ErrInvalidSchedule = errors.New("invalid schedule")

// SourceUser sits above SourceAdmin for a schedule. There is no environment
// layer here: no variable sets a weekly firing, so the layers are the user's
// own row, the admin's default, and the built-in one below.
const SourceUser = "user"

// The built-in schedule, used until an admin chooses: Monday 06:00, and off.
// Automatic runs spend tokens and send session text to a provider, so they
// begin when an admin turns them on rather than on their own.
const (
	defaultScheduleWeekday = time.Monday
	defaultScheduleHour    = 6
	defaultScheduleMinute  = 0
)

// ResolvedSchedule is the schedule one user actually runs on, and where each
// part of it came from.
type ResolvedSchedule struct {
	Schedule
	Enabled bool
	// EnabledSource and WhenSource say which layer decided, for the screen to
	// print "관리자 기본값" against a value the user has not overridden.
	EnabledSource string
	// WhenSource covers weekday and time together. They resolve field by field,
	// but one control sets them, so a user who moved any part of it reads as
	// having moved it.
	WhenSource string
}

// ResolveSchedule applies user > admin > built-in to each field on its own, so
// a user who moved only the hour still follows a later change of weekday.
//
// fallbackTZ stands in when the user has saved no zone -- the caller passes the
// zone of their last report. Empty stays empty, which FireTime reads as UTC.
func ResolveSchedule(admin *store.AIAutoSchedule, user *store.AIUserSchedule, fallbackTZ string) ResolvedSchedule {
	var (
		adminEnabled, userEnabled            *bool
		adminWeekday, adminHour, adminMinute *int
		userWeekday, userHour, userMinute    *int
		resolved                             ResolvedSchedule
	)
	if admin != nil {
		adminEnabled, adminWeekday, adminHour, adminMinute = admin.Enabled, admin.Weekday, admin.Hour, admin.Minute
	}
	if user != nil {
		userEnabled, userWeekday, userHour, userMinute = user.Enabled, user.Weekday, user.Hour, user.Minute
	}

	enabled, enabledSource := pickBool(userEnabled, adminEnabled, false)
	weekday, weekdaySource := pickInt(userWeekday, adminWeekday, int(defaultScheduleWeekday))
	hour, hourSource := pickInt(userHour, adminHour, defaultScheduleHour)
	minute, minuteSource := pickInt(userMinute, adminMinute, defaultScheduleMinute)

	resolved.Enabled, resolved.EnabledSource = enabled, enabledSource
	resolved.Weekday = time.Weekday(weekday)
	resolved.Hour, resolved.Minute = hour, minute
	resolved.WhenSource = highestSource(weekdaySource, hourSource, minuteSource)
	resolved.TZ = fallbackTZ
	if user != nil && user.TZ != "" {
		resolved.TZ = user.TZ
	}
	return resolved
}

// pickBool resolves the switch, and it is the one field where the user does not
// simply win.
//
// An administrator turning automatic runs off turns them off for everyone. A run
// sends session text to an outside provider and spends tokens, so stopping that
// for the organisation has to be possible; a per-user switch that outranked the
// administrator would leave no way to do it. Turning it *on* stays a default the
// user may decline -- only the off is binding.
func pickBool(user, admin *bool, def bool) (bool, string) {
	if admin != nil && !*admin {
		return false, SourceAdmin
	}
	switch {
	case user != nil:
		return *user, SourceUser
	case admin != nil:
		return *admin, SourceAdmin
	default:
		return def, SourceDefault
	}
}

func pickInt(user, admin *int, def int) (int, string) {
	switch {
	case user != nil:
		return *user, SourceUser
	case admin != nil:
		return *admin, SourceAdmin
	default:
		return def, SourceDefault
	}
}

// highestSource is the topmost layer any of the parts came from, because the
// screen shows weekday and time as one value.
func highestSource(sources ...string) string {
	out := SourceDefault
	for _, s := range sources {
		switch s {
		case SourceUser:
			return SourceUser
		case SourceAdmin:
			out = SourceAdmin
		}
	}
	return out
}

// Schedule is when a user's weekly report runs by itself: a weekday and a
// wall-clock time in one zone.
//
// A run covers the week that has ended, never the one in progress, so a firing
// belongs to the first Weekday Hour:Minute at or after that week's end. Pinning
// it to the end rather than to a fixed offset keeps one rule for every weekday
// a user might pick -- Monday 06:00 fires six hours after the week closes,
// Wednesday 15:00 fires two and a half days later, and both cover the same week.
//
// The zone is the user's, so two people with the same weekday and time run at
// different instants. That also spreads the load, which a single org-wide
// instant would pile onto one minute.
type Schedule struct {
	Weekday time.Weekday
	Hour    int
	Minute  int
	// TZ is an IANA name. Empty means UTC, as it does for a week id.
	TZ string
}

func (s Schedule) location() (*time.Location, error) {
	if s.Weekday < time.Sunday || s.Weekday > time.Saturday {
		return nil, fmt.Errorf("%w: weekday %d", ErrInvalidSchedule, int(s.Weekday))
	}
	if s.Hour < 0 || s.Hour > 23 || s.Minute < 0 || s.Minute > 59 {
		return nil, fmt.Errorf("%w: time %02d:%02d", ErrInvalidSchedule, s.Hour, s.Minute)
	}
	tz := s.TZ
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("%w: time zone %q", ErrInvalidSchedule, s.TZ)
	}
	return loc, nil
}

// FireTime is when the run covering w happens: the first Weekday Hour:Minute at
// or after w ends.
//
// The hour is built from the local date rather than added to the boundary, so a
// daylight saving change inside the gap moves the instant and leaves the wall
// clock where the user set it.
func (s Schedule) FireTime(w Week) (time.Time, error) {
	loc, err := s.location()
	if err != nil {
		return time.Time{}, err
	}
	end := w.Until.In(loc)
	ahead := (int(s.Weekday) - int(end.Weekday()) + 7) % 7
	at := time.Date(end.Year(), end.Month(), end.Day()+ahead, s.Hour, s.Minute, 0, 0, loc)
	// Same weekday but an earlier time than the boundary: that firing belongs to
	// the week before, so take the next one.
	if at.Before(w.Until) {
		at = time.Date(at.Year(), at.Month(), at.Day()+7, s.Hour, s.Minute, 0, 0, loc)
	}
	return at, nil
}

// DueWeek is the latest week whose firing has passed at now -- the week a run
// started now would cover. The caller decides whether that run already exists.
func (s Schedule) DueWeek(now time.Time) (Week, error) {
	loc, err := s.location()
	if err != nil {
		return Week{}, err
	}
	current, err := WeekContaining(now, loc.String())
	if err != nil {
		return Week{}, err
	}
	// Firings are exactly one week apart, so the week before the current one is
	// due unless its firing is still ahead, and then the one before that is.
	cand := current.Previous()
	for i := 0; i < 2; i++ {
		at, err := s.FireTime(cand)
		if err != nil {
			return Week{}, err
		}
		if !at.After(now) {
			return cand, nil
		}
		cand = cand.Previous()
	}
	return cand, nil
}

// NextRun is the first firing strictly after now, for the screen to print.
func (s Schedule) NextRun(now time.Time) (time.Time, error) {
	loc, err := s.location()
	if err != nil {
		return time.Time{}, err
	}
	current, err := WeekContaining(now, loc.String())
	if err != nil {
		return time.Time{}, err
	}
	cand := current.Previous()
	for i := 0; i < 3; i++ {
		at, err := s.FireTime(cand)
		if err != nil {
			return time.Time{}, err
		}
		if at.After(now) {
			return at, nil
		}
		cand, err = WeekContaining(cand.Until, cand.TZ)
		if err != nil {
			return time.Time{}, err
		}
	}
	return time.Time{}, fmt.Errorf("%w: no firing after %s", ErrInvalidSchedule, now)
}
