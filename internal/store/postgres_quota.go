package store

import (
	"context"
)

// UpsertQuotaSnapshot stores the reading under the address the profile has been
// merged into, if it has been: the alias is read from user_aliases in the same
// statement, not from the API server's cache, which lags a merge by up to 30
// seconds. Without one the address is stored as received. The write takes the
// shared form of the lock MergeUsers holds, so it cannot read the aliases just
// before a merge commits and write the source's row back after the merge deleted
// it. Alias chains resolve up to two steps here (the handler's cache, then this
// lookup), unlike the other ingest paths, which resolve one.
func (s *PgStore) UpsertQuotaSnapshot(ctx context.Context, q *QuotaSnapshot) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO quota_snapshots
			(profile_email, user_id, five_hour_pct, five_hour_resets_at,
			 seven_day_pct, seven_day_resets_at,
			 seven_day_sonnet_pct, seven_day_sonnet_resets_at, updated_at)
		VALUES (COALESCE((SELECT to_profile_email FROM user_aliases WHERE from_profile_email = lower($1::text)), $1::text),
			$2,$3,$4,$5,$6,$7,$8,now())
		ON CONFLICT (profile_email) DO UPDATE SET
			user_id                    = EXCLUDED.user_id,
			five_hour_pct              = EXCLUDED.five_hour_pct,
			five_hour_resets_at        = EXCLUDED.five_hour_resets_at,
			seven_day_pct              = EXCLUDED.seven_day_pct,
			seven_day_resets_at        = EXCLUDED.seven_day_resets_at,
			seven_day_sonnet_pct       = EXCLUDED.seven_day_sonnet_pct,
			seven_day_sonnet_resets_at = EXCLUDED.seven_day_sonnet_resets_at,
			updated_at                 = now()`,
		q.ProfileEmail, q.UserID,
		q.FiveHourPct, q.FiveHourResetsAt,
		q.SevenDayPct, q.SevenDayResetsAt,
		q.SevenDaySonnetPct, q.SevenDaySonnetResetsAt,
	); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PgStore) GetQuotaSnapshot(ctx context.Context, profileEmail string) (*QuotaSnapshot, error) {
	q := &QuotaSnapshot{}
	err := s.pool.QueryRow(ctx, `
		SELECT profile_email, user_id,
		       five_hour_pct, five_hour_resets_at,
		       seven_day_pct, seven_day_resets_at,
		       seven_day_sonnet_pct, seven_day_sonnet_resets_at,
		       updated_at
		FROM quota_snapshots WHERE profile_email = $1`, profileEmail).
		Scan(&q.ProfileEmail, &q.UserID,
			&q.FiveHourPct, &q.FiveHourResetsAt,
			&q.SevenDayPct, &q.SevenDayResetsAt,
			&q.SevenDaySonnetPct, &q.SevenDaySonnetResetsAt,
			&q.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return q, nil
}

func (s *PgStore) ListQuotaSnapshots(ctx context.Context) ([]*QuotaSnapshot, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT profile_email, user_id,
		       five_hour_pct, five_hour_resets_at,
		       seven_day_pct, seven_day_resets_at,
		       seven_day_sonnet_pct, seven_day_sonnet_resets_at,
		       updated_at
		FROM quota_snapshots ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*QuotaSnapshot
	for rows.Next() {
		q := &QuotaSnapshot{}
		if err := rows.Scan(&q.ProfileEmail, &q.UserID,
			&q.FiveHourPct, &q.FiveHourResetsAt,
			&q.SevenDayPct, &q.SevenDayResetsAt,
			&q.SevenDaySonnetPct, &q.SevenDaySonnetResetsAt,
			&q.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, q)
	}
	return result, rows.Err()
}
