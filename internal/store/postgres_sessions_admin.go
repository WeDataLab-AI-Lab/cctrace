package store

import (
	"context"
	"fmt"
)

func (s *PgStore) SessionList(ctx context.Context, profileEmail string, limit, offset int) ([]string, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT DISTINCT session_id FROM visible_events
		WHERE session_id != '' AND ($1 = '' OR profile_email = $1)
		ORDER BY session_id DESC LIMIT $2 OFFSET $3`

	rows, err := s.pool.Query(ctx, q, profileEmail, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]string, 0)
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			return nil, err
		}
		result = append(result, sid)
	}
	return result, rows.Err()
}

func (s *PgStore) DeleteUserData(ctx context.Context, profileEmail string, userID string) error {
	if profileEmail == "" && userID == "" {
		return fmt.Errorf("profileEmail or userID required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, true); err != nil {
		return err
	}
	column, value := "user_id", userID
	if profileEmail != "" {
		column, value = "profile_email", profileEmail
	}
	affected, err := querySessionIDs(ctx, tx, fmt.Sprintf(`
		SELECT session_id FROM otel_events WHERE %s = $1 AND session_id <> ''
		UNION SELECT session_id FROM session_records WHERE %s = $1 AND session_id <> ''
		UNION SELECT session_id FROM claude_imputed_cost WHERE %s = $1 AND session_id <> ''
		UNION SELECT session_id FROM codex_imputed_cost WHERE %s = $1 AND session_id <> ''`, column, column, column, column), value)
	if err != nil {
		return err
	}
	if err := lockSessionOverviewRollups(ctx, tx, affected); err != nil {
		return err
	}
	if err := lockPluginInvocationFacts(ctx, tx, false); err != nil {
		return err
	}
	if err := lockTaskSegmentFacts(ctx, tx, false); err != nil {
		return err
	}
	for _, table := range []string{"otel_events", "otel_metrics", "session_records", "claude_imputed_cost", "codex_imputed_cost", "plugin_invocation_facts", "task_segment_facts"} {
		q := fmt.Sprintf("DELETE FROM %s WHERE %s = $1", table, column)
		if _, err := tx.Exec(ctx, q, value); err != nil {
			return fmt.Errorf("delete from %s: %w", table, err)
		}
	}
	if err := refreshSessionOverviewRollups(ctx, tx, affected); err != nil {
		return err
	}
	if err := refreshPluginInvocationFacts(ctx, tx, affected); err != nil {
		return err
	}
	if err := refreshTaskSegmentFacts(ctx, tx, affected); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PgStore) MergeUsers(ctx context.Context, fromProfileEmail string, fromUserID string, toProfileEmail string) error {
	if toProfileEmail == "" {
		return fmt.Errorf("toProfileEmail required")
	}
	if fromProfileEmail == "" && fromUserID == "" {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, true); err != nil {
		return err
	}
	column, value, extra := "user_id", fromUserID, " AND profile_email = ''"
	if fromProfileEmail != "" {
		column, value, extra = "profile_email", fromProfileEmail, ""
	}
	affected, err := querySessionIDs(ctx, tx, fmt.Sprintf(`
		SELECT session_id FROM otel_events WHERE %s = $1%s AND session_id <> ''
		UNION SELECT session_id FROM session_records WHERE %s = $1%s AND session_id <> ''
		UNION SELECT session_id FROM claude_imputed_cost WHERE %s = $1%s AND session_id <> ''
		UNION SELECT session_id FROM codex_imputed_cost WHERE %s = $1%s AND session_id <> ''`, column, extra, column, extra, column, extra, column, extra), value)
	if err != nil {
		return err
	}
	if err := lockSessionOverviewRollups(ctx, tx, affected); err != nil {
		return err
	}
	if err := lockPluginInvocationFacts(ctx, tx, false); err != nil {
		return err
	}
	for _, table := range []string{"otel_events", "otel_metrics", "session_records", "claude_imputed_cost", "codex_imputed_cost", "plugin_invocation_facts", "task_segment_facts"} {
		q := fmt.Sprintf("UPDATE %s SET profile_email = $1 WHERE %s = $2%s", table, column, extra)
		if _, err := tx.Exec(ctx, q, toProfileEmail, value); err != nil {
			return fmt.Errorf("merge %s: %w", table, err)
		}
	}
	if fromProfileEmail != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO user_aliases (from_profile_email, to_profile_email)
			VALUES (lower($1), lower($2)) ON CONFLICT (from_profile_email)
			DO UPDATE SET to_profile_email = EXCLUDED.to_profile_email`, fromProfileEmail, toProfileEmail); err != nil {
			return fmt.Errorf("upsert alias: %w", err)
		}
	}
	if err := refreshSessionOverviewRollups(ctx, tx, affected); err != nil {
		return err
	}
	if err := refreshPluginInvocationFacts(ctx, tx, affected); err != nil {
		return err
	}
	if err := refreshTaskSegmentFacts(ctx, tx, affected); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CleanupOrphanSessionRecords deletes session_records that no event backs.
//
// It must read unified_events, never visible_events: an excluded account's
// events are hidden from the visible view, so filtering here would classify its
// sessions as orphans and delete them — turning a reversible exclusion into
// permanent data loss.
func (s *PgStore) CleanupOrphanSessionRecords(ctx context.Context) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, `
		DELETE FROM session_records sr
		WHERE NOT EXISTS (
			SELECT 1 FROM unified_events e WHERE e.session_id = sr.session_id
		)
		RETURNING sr.session_id`)
	if err != nil {
		return 0, fmt.Errorf("cleanup orphan session records: %w", err)
	}
	seen := map[string]struct{}{}
	var affected []string
	var deleted int64
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		deleted++
		if _, ok := seen[id]; !ok && id != "" {
			seen[id] = struct{}{}
			affected = append(affected, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if err := refreshSessionOverviewRollups(ctx, tx, affected); err != nil {
		return 0, err
	}
	if err := lockPluginInvocationFacts(ctx, tx, false); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM plugin_invocation_facts f WHERE NOT EXISTS (
		SELECT 1 FROM session_records sr WHERE sr.id = f.source_record_id)`); err != nil {
		return 0, err
	}
	if err := refreshPluginInvocationFacts(ctx, tx, affected); err != nil {
		return 0, err
	}
	if err := lockTaskSegmentFacts(ctx, tx, false); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM task_segment_facts f WHERE NOT EXISTS (
		SELECT 1 FROM session_records sr WHERE sr.id = f.boundary_record_id)`); err != nil {
		return 0, err
	}
	if err := refreshTaskSegmentFacts(ctx, tx, affected); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return deleted, nil
}
