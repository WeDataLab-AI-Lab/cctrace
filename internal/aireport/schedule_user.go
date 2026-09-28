package aireport

import (
	"context"
	"fmt"
	"time"

	"cctrace/internal/store"
)

// UserSchedule is the schedule one user's automatic runs follow: their own row
// where they have set something, the admin's default elsewhere, and the
// built-in one below that.
//
// fallbackTZ is the zone to read the weekday and time in when the user has
// saved none -- the caller passes the zone of their latest report, or the one
// the screen is showing. The row's own zone wins when it has one.
func (s *Service) UserSchedule(ctx context.Context, userID int64, fallbackTZ string) (ResolvedSchedule, error) {
	admin, err := s.st.GetAIAutoSchedule(ctx)
	if err != nil {
		return ResolvedSchedule{}, err
	}
	user, err := s.st.GetAIUserSchedule(ctx, userID)
	if err != nil {
		return ResolvedSchedule{}, err
	}
	// The zone is decided here rather than by each caller, so the screen prints
	// the firing the ticker actually makes. Order: what the user saved, then the
	// zone their last report was generated in, then whatever the caller offers,
	// then the deployment's default. The scheduler reads the same order, less
	// the caller's offer.
	if user == nil || user.TZ == "" {
		recorded, err := s.st.LatestAIReportTZ(ctx, userID)
		if err != nil {
			return ResolvedSchedule{}, err
		}
		if recorded != "" {
			fallbackTZ = recorded
		}
	}
	if fallbackTZ == "" {
		fallbackTZ = s.cfg.DefaultTZ
	}
	return ResolveSchedule(admin, user, fallbackTZ), nil
}

// SetUserSchedule saves one user's override and answers with what now applies,
// so the screen shows the resolved value rather than guessing how their change
// combined with the admin's default.
//
// A nil field clears that part, which puts it back under the admin's default --
// it does not mean "off" or "midnight". Everything the firing rule must be able
// to read is checked before anything is stored: a weekday or time out of range,
// and a zone that does not load. Otherwise the scheduler would hold a row it
// cannot turn into a firing, and the user would never learn why nothing ran.
func (s *Service) SetUserSchedule(ctx context.Context, userID int64, enabled *bool, weekday, hour, minute *int, tz string) (ResolvedSchedule, error) {
	if err := validateScheduleFields(weekday, hour, minute); err != nil {
		return ResolvedSchedule{}, err
	}
	// Empty hands the zone back to the fallback: the last report's, then the
	// deployment's default.
	if tz != "" {
		if _, err := time.LoadLocation(tz); err != nil {
			return ResolvedSchedule{}, fmt.Errorf("%w: time zone %q", ErrInvalidSchedule, tz)
		}
	}
	saved := store.AIUserSchedule{
		DashboardUserID: userID,
		Enabled:         enabled,
		Weekday:         weekday,
		Hour:            hour,
		Minute:          minute,
		TZ:              tz,
	}
	if err := s.st.UpsertAIUserSchedule(ctx, saved); err != nil {
		return ResolvedSchedule{}, err
	}
	// Resolved the way the screen reads it later, so a row saved without a zone
	// answers with the zone the ticker will fire in rather than UTC.
	return s.UserSchedule(ctx, userID, "")
}
