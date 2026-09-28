package store

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

func (s *PgStore) UpsertAlias(ctx context.Context, fromEmail, toEmail string) error {
	if fromEmail == "" || toEmail == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO user_aliases (from_profile_email, to_profile_email)
		 VALUES ($1, $2)
		 ON CONFLICT (from_profile_email) DO UPDATE SET to_profile_email = EXCLUDED.to_profile_email`,
		strings.ToLower(fromEmail), strings.ToLower(toEmail),
	)
	return err
}

func (s *PgStore) LoadAliases(ctx context.Context) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT from_profile_email, to_profile_email FROM user_aliases`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := make(map[string]string)
	for rows.Next() {
		var from, to string
		if err := rows.Scan(&from, &to); err != nil {
			return nil, err
		}
		m[from] = to
	}
	return m, rows.Err()
}

func (s *PgStore) SetPrivacy(ctx context.Context, userID, profileEmail, scopeType, scopeValue string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO privacy_settings (user_id, profile_email, scope_type, scope_value)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (profile_email, scope_type, scope_value) DO NOTHING`,
		userID, profileEmail, scopeType, scopeValue)
	return err
}

func (s *PgStore) RemovePrivacy(ctx context.Context, userID, profileEmail, scopeType, scopeValue string) error {
	if userID != "" {
		_, err := s.pool.Exec(ctx,
			`DELETE FROM privacy_settings
			 WHERE user_id = $1 AND scope_type = $2 AND scope_value = $3`,
			userID, scopeType, scopeValue)
		return err
	}
	_, err := s.pool.Exec(ctx,
		`DELETE FROM privacy_settings
		 WHERE profile_email = $1 AND scope_type = $2 AND scope_value = $3`,
		profileEmail, scopeType, scopeValue)
	return err
}

func (s *PgStore) ListPrivacySettings(ctx context.Context, userID, profileEmail string) ([]*PrivacySetting, error) {
	var rows pgx.Rows
	var err error
	if userID != "" {
		rows, err = s.pool.Query(ctx,
			`SELECT id, profile_email, user_id, scope_type, scope_value, created_at
			 FROM privacy_settings WHERE user_id = $1 ORDER BY created_at DESC`,
			userID)
	} else {
		rows, err = s.pool.Query(ctx,
			`SELECT id, profile_email, user_id, scope_type, scope_value, created_at
			 FROM privacy_settings WHERE profile_email = $1 ORDER BY created_at DESC`,
			profileEmail)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var settings []*PrivacySetting
	for rows.Next() {
		p := &PrivacySetting{}
		if err := rows.Scan(&p.ID, &p.ProfileEmail, &p.UserID, &p.ScopeType, &p.ScopeValue, &p.CreatedAt); err != nil {
			return nil, err
		}
		settings = append(settings, p)
	}
	return settings, rows.Err()
}

func (s *PgStore) IsPrivate(ctx context.Context, profileEmail, projectHash, sessionID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM privacy_settings
			WHERE profile_email = $1
			AND (
				(scope_type = 'user' AND scope_value = '')
				OR (scope_type = 'project' AND scope_value = $2)
				OR (scope_type = 'session' AND scope_value = $3)
			)
		)`, profileEmail, projectHash, sessionID).Scan(&exists)
	return exists, err
}

func (s *PgStore) PrivateSessionIDs(ctx context.Context, sessions []*SessionOverview) (map[string]bool, error) {
	if len(sessions) == 0 {
		return make(map[string]bool), nil
	}

	// Collect unique profile_emails
	emailSet := make(map[string]bool)
	for _, sess := range sessions {
		emailSet[sess.ProfileEmail] = true
	}

	// Get all privacy settings for those emails
	emails := make([]string, 0, len(emailSet))
	for e := range emailSet {
		emails = append(emails, e)
	}

	rows, err := s.pool.Query(ctx,
		`SELECT profile_email, scope_type, scope_value
		 FROM privacy_settings
		 WHERE profile_email = ANY($1)`, emails)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Build lookup structures
	type privRule struct {
		scopeType  string
		scopeValue string
	}
	rules := make(map[string][]privRule) // keyed by profile_email
	for rows.Next() {
		var email, scopeType, scopeValue string
		if err := rows.Scan(&email, &scopeType, &scopeValue); err != nil {
			return nil, err
		}
		rules[email] = append(rules[email], privRule{scopeType, scopeValue})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Check each session against rules
	result := make(map[string]bool)
	for _, sess := range sessions {
		for _, rule := range rules[sess.ProfileEmail] {
			switch rule.scopeType {
			case "user":
				result[sess.SessionID] = true
			case "project":
				if rule.scopeValue == sess.ProjectHash {
					result[sess.SessionID] = true
				}
			case "session":
				if rule.scopeValue == sess.SessionID {
					result[sess.SessionID] = true
				}
			}
		}
	}
	return result, nil
}

func (s *PgStore) ResolveUserEmails(ctx context.Context, email string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT from_profile_email FROM user_aliases WHERE to_profile_email = $1`, email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []string{email}
	for rows.Next() {
		var alias string
		if err := rows.Scan(&alias); err != nil {
			return nil, err
		}
		result = append(result, alias)
	}
	return result, rows.Err()
}
