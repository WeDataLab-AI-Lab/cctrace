package aireport

import (
	"context"
	"errors"
	"log"
	"time"

	"cctrace/internal/store"
)

// ScheduleTick starts the automatic runs that are due at now and returns how
// many it started. A minute ticker calls it; now is a parameter so the decision
// is testable without waiting for a clock.
//
// scopeFor builds each user's scope. The server owns that rule -- it depends on
// whether the deployment scopes reads by cctrace user id or by profile email --
// and passing it in keeps that knowledge out of here while guaranteeing an
// automatic run reads exactly what the user's own run would.
//
// One tick weighs every active user, so a refusal for one must not stop the
// rest. Only a failure to read the schedule itself aborts the tick.
func (s *Service) ScheduleTick(ctx context.Context, now time.Time, scopeFor func(store.AIScheduleCandidate) Scope) (int, error) {
	admin, err := s.st.GetAIAutoSchedule(ctx)
	if err != nil {
		return 0, err
	}
	candidates, err := s.st.ListAIScheduleCandidates(ctx)
	if err != nil {
		return 0, err
	}

	started := 0
	for _, c := range candidates {
		// No report to take a zone from: the deployment's default, the same last
		// resort UserSchedule shows on the screen.
		fallbackTZ := c.FallbackTZ
		if fallbackTZ == "" {
			fallbackTZ = s.cfg.DefaultTZ
		}
		sch := ResolveSchedule(admin, c.Schedule, fallbackTZ)
		if !sch.Enabled {
			continue
		}
		week, err := sch.DueWeek(now)
		if err != nil {
			// A zone that no longer loads belongs to this user's row; the others
			// still run.
			log.Printf("[ai-reports] schedule skipped for user %d: %v", c.DashboardUserID, err)
			continue
		}
		fired, err := s.st.ScheduledAIRunExists(ctx, c.DashboardUserID, week.ID)
		if err != nil {
			// One user's failed read must not cost the rest their week. Candidates
			// come back ordered by id, so aborting here would cut the same tail of
			// users off every minute until the week turned.
			log.Printf("[ai-reports] schedule check failed for user %d: %v", c.DashboardUserID, err)
			continue
		}
		if fired {
			continue
		}
		if _, err := s.start(ctx, scopeFor(c), week, store.AIRunStartedBySchedule); err != nil {
			logScheduleRefusal(c.DashboardUserID, week.ID, err)
			continue
		}
		started++
	}
	return started, nil
}

// logScheduleRefusal keeps the ordinary reasons quiet. A week with no segments,
// a user who has not consented, and a runtime an admin turned off are all
// answers the tick will get every week for the same people; writing a line for
// each would bury the failures that mean something. None of them leaves a run
// behind either, since Start refuses before it creates the row.
func logScheduleRefusal(userID int64, week string, err error) {
	var running *AlreadyRunningError
	switch {
	case errors.Is(err, ErrNoRecords),
		errors.Is(err, ErrConsentRequired),
		errors.Is(err, ErrRuntimeDisabled),
		errors.Is(err, ErrAccountChanging),
		errors.Is(err, ErrRuntimeChanging),
		errors.As(err, &running):
		return
	}
	log.Printf("[ai-reports] scheduled run for user %d week %s did not start: %v", userID, week, err)
}
