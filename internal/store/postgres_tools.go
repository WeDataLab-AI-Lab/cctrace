package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

func (s *PgStore) ToolUsage(ctx context.Context, since, until time.Time, profileEmail string, loginEmail string, userID string) ([]*ToolUsageSummary, error) {
	q := `SELECT tool_name,
		SUM(uses)::bigint AS use_count,
		SUM(successes)::bigint AS success_count,
		SUM(fails)::bigint AS fail_count
		FROM ` + toolCallRowsSQL + `
		WHERE ts >= $1 AND ts < $2 AND ($3 = '' OR profile_email = $3) AND ($4 = '' OR login_email = $4) AND ($5 = '' OR user_id = $5)
		GROUP BY tool_name
		ORDER BY use_count DESC`

	rows, err := s.pool.Query(ctx, q, since, until, profileEmail, loginEmail, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*ToolUsageSummary, 0)
	for rows.Next() {
		t := &ToolUsageSummary{}
		if err := rows.Scan(&t.ToolName, &t.UseCount, &t.SuccessCount, &t.FailCount); err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

func (s *PgStore) ToolTimeSeries(ctx context.Context, toolName string, since, until time.Time, profileEmail string, loginEmail string, userID string, granularity string, tz string) ([]*ToolTimeBucket, error) {
	allowed := map[string]bool{"minute": true, "hour": true, "day": true, "week": true, "month": true}
	if !allowed[granularity] {
		granularity = "day"
	}
	dateFmt := map[string]string{
		"minute": `YYYY-MM-DD"T"HH24:MI:SS`,
		"hour":   `YYYY-MM-DD"T"HH24:MI:SS`,
		"day":    `YYYY-MM-DD`,
		"week":   `YYYY-MM-DD`,
		"month":  `YYYY-MM-DD`,
	}[granularity]

	// Bucket in the caller's timezone, matching every other chart
	// (TimeSeriesStats and friends). The frontend slices the returned string
	// rather than parsing it as an instant, so a UTC bucket would render as a
	// UTC wall clock labelled as local -- 9 hours off for a Seoul user.
	if tz == "" {
		tz = "UTC"
	}

	q := fmt.Sprintf(`SELECT to_char(date_trunc('%s', ts AT TIME ZONE '%s'), '%s'),
		SUM(successes)::bigint,
		SUM(fails)::bigint
	FROM `+toolCallRowsSQL+`
	WHERE tool_name = $1 AND ts >= $2 AND ts < $3
		AND ($4 = '' OR profile_email = $4) AND ($5 = '' OR login_email = $5) AND ($6 = '' OR user_id = $6)
	GROUP BY date_trunc('%s', ts AT TIME ZONE '%s')
	ORDER BY date_trunc('%s', ts AT TIME ZONE '%s') ASC`, granularity, tz, dateFmt, granularity, tz, granularity, tz)

	rows, err := s.pool.Query(ctx, q, toolName, since, until, profileEmail, loginEmail, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*ToolTimeBucket, 0)
	for rows.Next() {
		b := &ToolTimeBucket{}
		if err := rows.Scan(&b.Date, &b.SuccessCount, &b.FailCount); err != nil {
			return nil, err
		}
		result = append(result, b)
	}
	return result, rows.Err()
}

func (s *PgStore) ToolFailures(ctx context.Context, toolName string, since, until time.Time, profileEmail string, loginEmail string, userID string, limit int) ([]*ToolFailure, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT e.ts, COALESCE(e.session_id,''),
		COALESCE(e.user_id, ''),
		COALESCE(e.profile_email, ''),
		COALESCE(e.login_email, ''),
		COALESCE((SELECT e2.model FROM visible_events e2 WHERE e2.session_id = e.session_id AND e2.model != '' LIMIT 1), ''),
		e.duration_ms, e.attrs
	FROM visible_events e
	WHERE e.event_name = 'tool_result' AND e.tool_name = $1 AND e.tool_success = false
		AND e.ts >= $2 AND e.ts < $3
		AND ($4 = '' OR e.profile_email = $4) AND ($5 = '' OR e.login_email = $5) AND ($6 = '' OR e.user_id = $6)
	ORDER BY e.ts DESC
	LIMIT $7`

	rows, err := s.pool.Query(ctx, q, toolName, since, until, profileEmail, loginEmail, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*ToolFailure, 0)
	for rows.Next() {
		f := &ToolFailure{}
		var attrsJSON []byte
		if err := rows.Scan(&f.Ts, &f.SessionID, &f.UserID, &f.ProfileEmail, &f.LoginEmail, &f.Model, &f.DurationMs, &attrsJSON); err != nil {
			return nil, err
		}
		if len(attrsJSON) > 0 {
			_ = json.Unmarshal(attrsJSON, &f.Attrs)
		}
		result = append(result, f)
	}
	return result, rows.Err()
}
