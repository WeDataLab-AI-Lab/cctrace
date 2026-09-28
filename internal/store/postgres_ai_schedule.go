package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// GetAIAutoSchedule returns the admin's default for the weekly automatic run,
// or nil when no admin has set any part of it.
//
// A row whose four columns are all NULL reads as nil rather than as a schedule
// of zeroes: the runtime choice and the on/off switch share this row, so the
// row exists long before anyone touches the schedule.
func (s *PgStore) GetAIAutoSchedule(ctx context.Context) (*AIAutoSchedule, error) {
	a := &AIAutoSchedule{}
	err := s.pool.QueryRow(ctx, `
		SELECT auto_enabled, auto_weekday, auto_hour, auto_minute, updated_by, updated_at
		FROM ai_settings
		WHERE id = 1 AND (auto_enabled IS NOT NULL OR auto_weekday IS NOT NULL
			OR auto_hour IS NOT NULL OR auto_minute IS NOT NULL)`).
		Scan(&a.Enabled, &a.Weekday, &a.Hour, &a.Minute, &a.UpdatedBy, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get ai auto schedule: %w", err)
	}
	return a, nil
}

// SetAIAutoSchedule saves the admin's default; a nil field clears that one.
// The runtime choice and the enabled switch on the same row are kept.
func (s *PgStore) SetAIAutoSchedule(ctx context.Context, enabled *bool, weekday, hour, minute *int, actor string) error {
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO ai_settings (id, auto_enabled, auto_weekday, auto_hour, auto_minute, updated_by, updated_at)
		VALUES (1, $1, $2, $3, $4, $5, now())
		ON CONFLICT (id) DO UPDATE
		SET auto_enabled = EXCLUDED.auto_enabled,
			auto_weekday = EXCLUDED.auto_weekday,
			auto_hour = EXCLUDED.auto_hour,
			auto_minute = EXCLUDED.auto_minute,
			updated_by = EXCLUDED.updated_by,
			updated_at = now()`, enabled, weekday, hour, minute, actor); err != nil {
		return fmt.Errorf("set ai auto schedule: %w", err)
	}
	return nil
}

// GetAIUserSchedule returns one user's override, or nil when they have none and
// so follow the admin's default.
func (s *PgStore) GetAIUserSchedule(ctx context.Context, userID int64) (*AIUserSchedule, error) {
	u := &AIUserSchedule{}
	err := s.pool.QueryRow(ctx, `
		SELECT dashboard_user_id, enabled, weekday, hour, minute, tz, updated_at
		FROM ai_report_schedules WHERE dashboard_user_id = $1`, userID).
		Scan(&u.DashboardUserID, &u.Enabled, &u.Weekday, &u.Hour, &u.Minute, &u.TZ, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get ai user schedule: %w", err)
	}
	return u, nil
}

// UpsertAIUserSchedule replaces a user's override row. Nil fields are stored as
// NULL, which puts those back under the admin's default.
func (s *PgStore) UpsertAIUserSchedule(ctx context.Context, u AIUserSchedule) error {
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO ai_report_schedules (dashboard_user_id, enabled, weekday, hour, minute, tz, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (dashboard_user_id) DO UPDATE
		SET enabled = EXCLUDED.enabled,
			weekday = EXCLUDED.weekday,
			hour = EXCLUDED.hour,
			minute = EXCLUDED.minute,
			tz = EXCLUDED.tz,
			updated_at = now()`,
		u.DashboardUserID, u.Enabled, u.Weekday, u.Hour, u.Minute, u.TZ); err != nil {
		return fmt.Errorf("upsert ai user schedule: %w", err)
	}
	return nil
}

// ScheduledAIRunExists reports whether the scheduler has already fired for this
// user and week. It is how a firing stays once-per-week across restarts, so it
// counts a run in any state: a failed automatic run has been spent, and
// retrying it every minute until the week turns would be worse than leaving it.
//
// Runs the user started by hand do not count. Otherwise one manual report would
// swallow that week's automatic one.
func (s *PgStore) ScheduledAIRunExists(ctx context.Context, userID int64, week string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM ai_report_runs
			WHERE dashboard_user_id = $1 AND week = $2 AND started_by = $3
		)`, userID, week, AIRunStartedBySchedule).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("scheduled ai run exists: %w", err)
	}
	return exists, nil
}

// LatestAIReportTZ is the zone of the user's most recent report, or "" when
// they have none.
//
// It is the same value the candidate query joins for the scheduler. Reading it
// through one method lets the screen and the ticker answer "which zone is this
// firing read in" identically -- they used to decide separately, and a user
// with no saved schedule was shown a time nine hours from the one that fired.
func (s *PgStore) LatestAIReportTZ(ctx context.Context, userID int64) (string, error) {
	var tz string
	err := s.pool.QueryRow(ctx, `
		SELECT tz FROM ai_reports
		WHERE dashboard_user_id = $1
		ORDER BY generated_at DESC
		LIMIT 1`, userID).Scan(&tz)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("latest ai report tz: %w", err)
	}
	return tz, nil
}

// ListAIScheduleCandidates returns every active user the scheduler weighs on a
// tick, each with the override row they may never have saved and the zone to
// fall back on.
//
// It does not pre-filter by consent or by having a schedule: automatic runs
// cover everyone once an admin turns them on, and the caller resolves each
// user's own schedule before deciding. Deactivated accounts drop out, since
// they cannot read the report either.
//
// updated_at is deliberately not selected -- resolution reads only the switch,
// the weekday, the time and the zone.
func (s *PgStore) ListAIScheduleCandidates(ctx context.Context) ([]AIScheduleCandidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id, u.email, u.cctrace_user_id,
			sch.dashboard_user_id, sch.enabled, sch.weekday, sch.hour, sch.minute, sch.tz,
			COALESCE(last.tz, '')
		FROM dashboard_users u
		LEFT JOIN ai_report_schedules sch ON sch.dashboard_user_id = u.id
		LEFT JOIN LATERAL (
			SELECT r.tz FROM ai_reports r
			WHERE r.dashboard_user_id = u.id
			ORDER BY r.generated_at DESC
			LIMIT 1
		) last ON true
		WHERE u.is_active
		ORDER BY u.id`)
	if err != nil {
		return nil, fmt.Errorf("list ai schedule candidates: %w", err)
	}
	defer rows.Close()

	out := []AIScheduleCandidate{}
	for rows.Next() {
		var (
			c       AIScheduleCandidate
			schUser *int64
			enabled *bool
			weekday *int
			hour    *int
			minute  *int
			tz      *string
		)
		if err := rows.Scan(&c.DashboardUserID, &c.Email, &c.CctraceUserID,
			&schUser, &enabled, &weekday, &hour, &minute, &tz, &c.FallbackTZ); err != nil {
			return nil, fmt.Errorf("scan ai schedule candidate: %w", err)
		}
		// The left join matched nothing: this user has saved no override and
		// follows the admin's default whole.
		if schUser != nil {
			c.Schedule = &AIUserSchedule{
				DashboardUserID: *schUser,
				Enabled:         enabled,
				Weekday:         weekday,
				Hour:            hour,
				Minute:          minute,
			}
			if tz != nil {
				c.Schedule.TZ = *tz
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
