package store

import "context"

// UpsertRetentionSetting records an admin's durable retention choice for an axis
// (0 = permanent). One row per axis; last write wins.
func (s *PgStore) UpsertRetentionSetting(ctx context.Context, axis string, days int, updatedBy string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO retention_settings (axis, days, updated_by, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (axis) DO UPDATE SET
			days       = EXCLUDED.days,
			updated_by = EXCLUDED.updated_by,
			updated_at = now()`,
		axis, days, updatedBy)
	return err
}

// ListRetentionSettings returns all persisted per-axis retention choices.
func (s *PgStore) ListRetentionSettings(ctx context.Context) ([]*RetentionSetting, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT axis, days, updated_by, updated_at FROM retention_settings ORDER BY axis`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RetentionSetting
	for rows.Next() {
		r := &RetentionSetting{}
		if err := rows.Scan(&r.Axis, &r.Days, &r.UpdatedBy, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EffectiveRetentionConfig resolves the retention config to apply at boot:
// per axis, an env override wins (explicit ops override); otherwise a persisted
// setting is used (so a UI edit survives reboot and is re-asserted after Migrate
// re-adds a default policy); otherwise nil (leave the policy exactly as-is, so a
// plain deploy with neither env nor a stored setting never changes anything).
func (s *PgStore) EffectiveRetentionConfig(ctx context.Context, env RetentionConfig) (RetentionConfig, error) {
	settings, err := s.ListRetentionSettings(ctx)
	if err != nil {
		return env, err
	}
	byAxis := make(map[string]int, len(settings))
	for _, st := range settings {
		byAxis[st.Axis] = st.Days
	}
	out := env
	if out.OtelDays == nil {
		if d, ok := byAxis["otel"]; ok {
			v := d
			out.OtelDays = &v
		}
	}
	if out.SessionDays == nil {
		if d, ok := byAxis["session"]; ok {
			v := d
			out.SessionDays = &v
		}
	}
	return out, nil
}
