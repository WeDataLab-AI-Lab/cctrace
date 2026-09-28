package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

const dashboardUserColumns = `id, email, password_hash, role, name, team, is_active,
	cctrace_user_id, must_change_password, api_token, created_at, updated_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanDashboardUser(row rowScanner) (*DashboardUser, error) {
	u := &DashboardUser{}
	if err := row.Scan(
		&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.Name, &u.Team,
		&u.IsActive, &u.CctraceUserID, &u.MustChangePassword, &u.ApiToken,
		&u.CreatedAt, &u.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return u, nil
}

// ListUsers lists each user_id with its emails and cost. Usage with no user_id
// is left out on purpose: it has no id to list under and is not one person. Its
// cost still counts in the dashboard totals and the Cost by User chart.
func (s *PgStore) ListUsers(ctx context.Context) ([]*UserInfo, error) {
	q := `SELECT
		COALESCE(user_id, '') as user_id,
		COALESCE(MAX(profile_email), '') as profile_email,
		array_remove(array_agg(DISTINCT login_email), '') as login_emails,
		COALESCE(SUM(cost_usd), 0) as total_cost
	FROM visible_events
	WHERE model != '' AND user_id IS NOT NULL AND user_id != ''
	GROUP BY user_id
	ORDER BY SUM(cost_usd) DESC`

	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*UserInfo, 0)
	for rows.Next() {
		u := &UserInfo{}
		if err := rows.Scan(&u.UserID, &u.ProfileEmail, &u.LoginEmails, &u.TotalCost); err != nil {
			return nil, err
		}
		result = append(result, u)
	}
	return result, rows.Err()
}

func (s *PgStore) ListOtelUserIDs(ctx context.Context) ([]string, error) {
	q := `SELECT DISTINCT user_id FROM visible_events WHERE user_id != '' ORDER BY user_id`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// CheckUserID answers "is this user_id already taken?" for client setup. It reads
// unified_events, not visible_events: reporting an excluded account's user_id as
// free would let a second client claim it and mis-attribute its data.
func (s *PgStore) CheckUserID(ctx context.Context, userID string) (bool, []string, error) {
	q := `SELECT DISTINCT profile_email FROM unified_events WHERE user_id = $1 LIMIT 10`
	rows, err := s.pool.Query(ctx, q, userID)
	if err != nil {
		return false, nil, err
	}
	defer rows.Close()
	var emails []string
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return false, nil, err
		}
		emails = append(emails, email)
	}
	if err := rows.Err(); err != nil {
		return false, nil, err
	}
	return len(emails) > 0, emails, nil
}

func (s *PgStore) CountDashboardUsers(ctx context.Context) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM dashboard_users`).Scan(&count)
	return count, err
}

func (s *PgStore) CreateDashboardUser(ctx context.Context, u *DashboardUser) (*DashboardUser, error) {
	err := s.pool.QueryRow(ctx,
		`INSERT INTO dashboard_users (email, password_hash, role, name, team, cctrace_user_id, must_change_password)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, api_token, created_at, updated_at`,
		u.Email, u.PasswordHash, u.Role, u.Name, u.Team, u.CctraceUserID, u.MustChangePassword,
	).Scan(&u.ID, &u.ApiToken, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	u.IsActive = true
	return u, nil
}

func (s *PgStore) GetDashboardUserByEmail(ctx context.Context, email string) (*DashboardUser, error) {
	return scanDashboardUser(s.pool.QueryRow(ctx,
		`SELECT `+dashboardUserColumns+` FROM dashboard_users WHERE email = $1`, email,
	))
}

func (s *PgStore) GetDashboardUserByID(ctx context.Context, id int64) (*DashboardUser, error) {
	return scanDashboardUser(s.pool.QueryRow(ctx,
		`SELECT `+dashboardUserColumns+` FROM dashboard_users WHERE id = $1`, id,
	))
}

func (s *PgStore) GetDashboardUserByCctraceUserID(ctx context.Context, cctraceUserID string) (*DashboardUser, error) {
	return scanDashboardUser(s.pool.QueryRow(ctx,
		`SELECT `+dashboardUserColumns+` FROM dashboard_users WHERE cctrace_user_id = $1`, cctraceUserID,
	))
}

func (s *PgStore) GetDashboardUserByApiToken(ctx context.Context, token string) (*DashboardUser, error) {
	return scanDashboardUser(s.pool.QueryRow(ctx,
		`SELECT `+dashboardUserColumns+` FROM dashboard_users
		 WHERE id = (SELECT dashboard_user_id FROM dashboard_api_tokens
		             WHERE token = $1 AND is_active AND (expires_at IS NULL OR expires_at > now()))`, token,
	))
}

// GetDashboardUserByIngestionToken resolves only active CLI-issued tokens.
// Web tokens remain valid for reads, but cannot cross the collection boundary.
func (s *PgStore) GetDashboardUserByIngestionToken(ctx context.Context, token string) (*DashboardUser, error) {
	var userID int64
	var createdVia string
	err := s.pool.QueryRow(ctx,
		`SELECT dashboard_user_id, created_via FROM dashboard_api_tokens
   WHERE token = $1 AND is_active AND (expires_at IS NULL OR expires_at > now())`, token,
	).Scan(&userID, &createdVia)
	if err != nil {
		return nil, err
	}
	if createdVia != "api" {
		return nil, ErrTokenNotIngestion
	}
	return s.GetDashboardUserByID(ctx, userID)
}

// GetDashboardUserByOpenAPIToken resolves a token for the read API, refusing the
// ones `cctrace init` issues for uploading.
//
// The migration copies every existing dashboard_users.api_token into
// dashboard_api_tokens as the primary row, so without this filter the read API would
// open on day one to every CLI token in the fleet -- credentials sitting in plaintext
// on laptops, handed out for a different job.
//
// The distinction is already recorded: created_via is 'api' for the CLI's primary
// token and 'web' for one a person made in Settings. A token that exists and is
// active but was issued for ingestion returns ErrTokenNotOpenAPI, not a not-found,
// so the handler can tell the caller what to do about it.
func (s *PgStore) GetDashboardUserByOpenAPIToken(ctx context.Context, token string) (*DashboardUser, error) {
	var createdVia string
	var userID int64
	err := s.pool.QueryRow(ctx,
		`SELECT dashboard_user_id, created_via FROM dashboard_api_tokens
		 WHERE token = $1 AND is_active AND (expires_at IS NULL OR expires_at > now())`, token,
	).Scan(&userID, &createdVia)
	if err != nil {
		return nil, err
	}
	if createdVia != "web" && createdVia != "cli_read" {
		return nil, ErrTokenNotOpenAPI
	}
	user, err := scanDashboardUser(s.pool.QueryRow(ctx,
		`SELECT `+dashboardUserColumns+` FROM dashboard_users WHERE id = $1`, userID,
	))
	if err != nil {
		return nil, err
	}
	// Checked on every request, not only at issuance, so promoting the owner
	// does not turn a CLI read token into one that reads every user.
	if createdVia == "cli_read" && user.Role == "admin" {
		return nil, ErrCLIReadTokenAdmin
	}
	return user, nil
}

func (s *PgStore) SetDashboardUserApiToken(ctx context.Context, id int64, token string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `UPDATE dashboard_users SET api_token = $1, updated_at = now() WHERE id = $2`, token, id); err != nil {
		return err
	}
	if token == "" {
		if _, err := tx.Exec(ctx, `DELETE FROM dashboard_api_tokens WHERE dashboard_user_id = $1 AND is_primary`, id); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO dashboard_api_tokens (dashboard_user_id, name, token, created_via, is_primary)
		VALUES ($1, 'CLI token', $2, 'api', true)
		ON CONFLICT (dashboard_user_id) WHERE is_primary
		DO UPDATE SET
			token = EXCLUDED.token,
			is_active = true,
			expires_at = NULL,
			rotated_at = CASE
				WHEN dashboard_api_tokens.token <> EXCLUDED.token THEN now()
				ELSE dashboard_api_tokens.rotated_at
			END`, id, token); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PgStore) ListDashboardUsers(ctx context.Context) ([]*DashboardUser, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, email, password_hash, role, name, team, is_active, cctrace_user_id, must_change_password,
		 CASE WHEN api_token != '' OR EXISTS (SELECT 1 FROM dashboard_api_tokens t WHERE t.dashboard_user_id = dashboard_users.id) THEN 'present' ELSE '' END,
		 created_at, updated_at
		 FROM dashboard_users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []*DashboardUser
	for rows.Next() {
		u, err := scanDashboardUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *PgStore) UpdateDashboardUser(ctx context.Context, id int64, p UpdateDashboardUserParams) error {
	var sets []string
	var args []interface{}
	n := 0

	if p.Role != nil {
		n++
		sets = append(sets, fmt.Sprintf("role = $%d", n))
		args = append(args, *p.Role)
	}
	if p.Name != nil {
		n++
		sets = append(sets, fmt.Sprintf("name = $%d", n))
		args = append(args, *p.Name)
	}
	if p.Team != nil {
		n++
		sets = append(sets, fmt.Sprintf("team = $%d", n))
		args = append(args, *p.Team)
	}
	if p.IsActive != nil {
		n++
		sets = append(sets, fmt.Sprintf("is_active = $%d", n))
		args = append(args, *p.IsActive)
	}
	if p.CctraceUserID != nil {
		n++
		sets = append(sets, fmt.Sprintf("cctrace_user_id = $%d", n))
		args = append(args, *p.CctraceUserID)
	}
	if len(sets) == 0 {
		return nil
	}

	n++
	sets = append(sets, "updated_at = now()")
	args = append(args, id)

	q := fmt.Sprintf("UPDATE dashboard_users SET %s WHERE id = $%d", strings.Join(sets, ", "), n)
	_, err := s.pool.Exec(ctx, q, args...)
	return err
}

func (s *PgStore) UpdateDashboardUserPassword(ctx context.Context, id int64, passwordHash string, mustChange bool) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE dashboard_users SET password_hash = $1, must_change_password = $2, updated_at = now() WHERE id = $3`,
		passwordHash, mustChange, id)
	return err
}

// CreateInitialDashboardUser serializes setup across processes before checking users.
func (s *PgStore) CreateInitialDashboardUser(ctx context.Context, u *DashboardUser) error {
	// Refresh the snapshot after waiting for the lock, even if the session defaults to repeatable read.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// Stable, setup-only advisory key.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(4846816632899917136)`); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM dashboard_users`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return ErrSetupAlreadyCompleted
	}
	err = tx.QueryRow(ctx,
		`INSERT INTO dashboard_users (email, password_hash, role, name, team, cctrace_user_id, must_change_password)
   VALUES ($1, $2, $3, $4, $5, $6, $7)
   RETURNING id, api_token, created_at, updated_at`,
		u.Email, u.PasswordHash, u.Role, u.Name, u.Team, u.CctraceUserID, u.MustChangePassword,
	).Scan(&u.ID, &u.ApiToken, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	u.IsActive = true
	return nil
}
