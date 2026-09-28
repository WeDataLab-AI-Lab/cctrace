package aireport

import (
	"context"
	"fmt"
)

// AutoSchedule is the admin's default for automatic runs, resolved against the
// built-in one so the screen always has a weekday and a time to show.
//
// A firing is read in each user's own zone, and the admin's row carries none.
// The zone shown is the deployment's default, which is what applies to every
// user who has neither saved a zone nor had a report made.
func (s *Service) AutoSchedule(ctx context.Context) (ResolvedSchedule, error) {
	admin, err := s.st.GetAIAutoSchedule(ctx)
	if err != nil {
		return ResolvedSchedule{}, err
	}
	return ResolveSchedule(admin, nil, s.cfg.DefaultTZ), nil
}

// SetAutoSchedule saves the admin's default. A nil field clears that one and
// hands it back to the built-in default, as an empty model does for settings.
//
// The range is checked before anything is stored, so the scheduler never reads
// a row it cannot turn into a firing.
func (s *Service) SetAutoSchedule(ctx context.Context, enabled *bool, weekday, hour, minute *int, actor string) error {
	if err := validateScheduleFields(weekday, hour, minute); err != nil {
		return err
	}
	return s.st.SetAIAutoSchedule(ctx, enabled, weekday, hour, minute, actor)
}

// validateScheduleFields checks the parts that are set. The same ranges the
// firing rule enforces, stated once for both the admin's default and a user's
// own override.
func validateScheduleFields(weekday, hour, minute *int) error {
	if weekday != nil && (*weekday < 0 || *weekday > 6) {
		return fmt.Errorf("%w: weekday %d", ErrInvalidSchedule, *weekday)
	}
	if hour != nil && (*hour < 0 || *hour > 23) {
		return fmt.Errorf("%w: hour %d", ErrInvalidSchedule, *hour)
	}
	if minute != nil && (*minute < 0 || *minute > 59) {
		return fmt.Errorf("%w: minute %d", ErrInvalidSchedule, *minute)
	}
	return nil
}
