package store

import (
	"context"
)

func (s *PgStore) UpsertQuotaSnapshot(ctx context.Context, q *QuotaSnapshot) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO quota_snapshots
			(profile_email, user_id, five_hour_pct, five_hour_resets_at,
			 seven_day_pct, seven_day_resets_at,
			 seven_day_sonnet_pct, seven_day_sonnet_resets_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now())
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
	)
	return err
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
