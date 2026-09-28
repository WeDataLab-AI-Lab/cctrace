package store

import (
	"context"
	"time"
)

// AccountSwitchStat reports how many distinct login accounts one user_id has
// been seen with, and how many of that user's sessions hold more than one.
type AccountSwitchStat struct {
	UserID         string   `json:"user_id"`
	LoginEmails    []string `json:"login_emails"`
	MultiAcctSess  int64    `json:"multi_account_sessions"`
	TotalSessions  int64    `json:"total_sessions"`
	LastSeenSwitch *string  `json:"last_seen_switch,omitempty"`
}

// AccountSwitchStats surfaces how often a user_id carries more than one login
// account -- the frequency the `[audit] action=user_id_email_mismatch` log line
// could only hint at.
//
// The log-based signal is an in-process heuristic that resets on restart and
// counts events rather than switches. otel_events is the durable record: every
// row carries user_id and login_email as recorded at run time, so the same
// question can be answered exactly, and per session rather than per process
// lifetime.
//
// since bounds the scan; the zero value means all retained history. Note that
// otel_events is under a retention policy, so "all history" is the retention
// window, not all time.
func (s *PgStore) AccountSwitchStats(ctx context.Context, since time.Time) ([]*AccountSwitchStat, error) {
	const q = `
	WITH scoped AS (
		SELECT user_id, session_id, login_email, ts
		FROM visible_events
		WHERE user_id <> '' AND login_email <> '' AND session_id <> ''
		  AND ($1::timestamptz IS NULL OR ts >= $1::timestamptz)
	), per_session AS (
		SELECT user_id, session_id,
		       COUNT(DISTINCT login_email) AS accounts,
		       MAX(ts) AS last_ts
		FROM scoped
		GROUP BY user_id, session_id
	)
	SELECT p.user_id,
	       (SELECT ARRAY_AGG(DISTINCT s.login_email ORDER BY s.login_email)
	          FROM scoped s WHERE s.user_id = p.user_id),
	       COUNT(*) FILTER (WHERE p.accounts > 1),
	       COUNT(*),
	       to_char(MAX(p.last_ts) FILTER (WHERE p.accounts > 1) AT TIME ZONE 'UTC',
	               'YYYY-MM-DD"T"HH24:MI:SS"Z"')
	FROM per_session p
	GROUP BY p.user_id
	HAVING COUNT(*) FILTER (WHERE p.accounts > 1) > 0
	    OR (SELECT COUNT(DISTINCT s.login_email) FROM scoped s WHERE s.user_id = p.user_id) > 1
	ORDER BY COUNT(*) FILTER (WHERE p.accounts > 1) DESC, p.user_id`

	var arg any
	if !since.IsZero() {
		arg = since
	}
	rows, err := s.pool.Query(ctx, q, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*AccountSwitchStat, 0)
	for rows.Next() {
		st := &AccountSwitchStat{}
		if err := rows.Scan(&st.UserID, &st.LoginEmails, &st.MultiAcctSess, &st.TotalSessions, &st.LastSeenSwitch); err != nil {
			return nil, err
		}
		result = append(result, st)
	}
	return result, rows.Err()
}
