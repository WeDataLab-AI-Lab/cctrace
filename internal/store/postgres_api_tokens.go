package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

func apiTokenHint(token string) string {
	if len(token) <= 10 {
		return token
	}
	return token[:4] + "…" + token[len(token)-6:]
}

func applyAPITokenDerivedMetadata(token *DashboardAPIToken, secret string) {
	token.TokenHint = apiTokenHint(secret)
	token.IsExpired = token.ExpiresAt != nil && !token.ExpiresAt.After(time.Now())
}

func scanDashboardAPIToken(row rowScanner) (*DashboardAPIToken, error) {
	token := &DashboardAPIToken{}
	var secret string
	if err := row.Scan(
		&token.ID, &token.UserID, &token.Name, &secret, &token.CreatedVia,
		&token.IsActive, &token.ExpiresAt, &token.CreatedAt, &token.RotatedAt,
	); err != nil {
		return nil, err
	}
	applyAPITokenDerivedMetadata(token, secret)
	return token, nil
}

func (s *PgStore) ListDashboardUserAPITokens(ctx context.Context, userID int64) ([]*DashboardAPIToken, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, dashboard_user_id, name, token, created_via, is_active, expires_at, created_at, rotated_at
		FROM dashboard_api_tokens
		WHERE dashboard_user_id = $1
		ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []*DashboardAPIToken
	for rows.Next() {
		token, err := scanDashboardAPIToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

func (s *PgStore) CreateDashboardUserAPIToken(ctx context.Context, userID int64, name, secret, createdVia string, expiresAt *time.Time) (*DashboardAPIToken, error) {
	token := &DashboardAPIToken{}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO dashboard_api_tokens (dashboard_user_id, name, token, created_via, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, dashboard_user_id, name, created_via, is_active, expires_at, created_at, rotated_at`,
		userID, name, secret, createdVia, expiresAt,
	).Scan(&token.ID, &token.UserID, &token.Name, &token.CreatedVia, &token.IsActive, &token.ExpiresAt, &token.CreatedAt, &token.RotatedAt)
	if err != nil {
		return nil, err
	}
	applyAPITokenDerivedMetadata(token, secret)
	return token, nil
}

func (s *PgStore) RotateDashboardUserAPIToken(ctx context.Context, userID, tokenID int64, secret string) (*DashboardAPIToken, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	token := &DashboardAPIToken{}
	var primary bool
	err = tx.QueryRow(ctx, `
		UPDATE dashboard_api_tokens
		SET token = $1, rotated_at = now()
		WHERE id = $2 AND dashboard_user_id = $3
		RETURNING id, dashboard_user_id, name, created_via, is_active, expires_at, created_at, rotated_at, is_primary`,
		secret, tokenID, userID,
	).Scan(&token.ID, &token.UserID, &token.Name, &token.CreatedVia, &token.IsActive, &token.ExpiresAt, &token.CreatedAt, &token.RotatedAt, &primary)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if primary {
		if _, err := tx.Exec(ctx, `UPDATE dashboard_users SET api_token = $1, updated_at = now() WHERE id = $2`, secret, userID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	applyAPITokenDerivedMetadata(token, secret)
	return token, nil
}

func (s *PgStore) SetDashboardUserAPITokenActive(ctx context.Context, userID, tokenID int64, active bool) (*DashboardAPIToken, error) {
	token, err := scanDashboardAPIToken(s.pool.QueryRow(ctx, `
		UPDATE dashboard_api_tokens
		SET is_active = $1
		WHERE id = $2 AND dashboard_user_id = $3
		RETURNING id, dashboard_user_id, name, token, created_via, is_active, expires_at, created_at, rotated_at`,
		active, tokenID, userID,
	))
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return token, nil
}

func (s *PgStore) SetDashboardUserAPITokenExpiration(ctx context.Context, userID, tokenID int64, expiresAt *time.Time) (*DashboardAPIToken, error) {
	token, err := scanDashboardAPIToken(s.pool.QueryRow(ctx, `
		UPDATE dashboard_api_tokens
		SET expires_at = $1
		WHERE id = $2 AND dashboard_user_id = $3
		RETURNING id, dashboard_user_id, name, token, created_via, is_active, expires_at, created_at, rotated_at`,
		expiresAt, tokenID, userID,
	))
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return token, nil
}

func (s *PgStore) DeleteDashboardUserAPIToken(ctx context.Context, userID, tokenID int64) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	var primary bool
	err = tx.QueryRow(ctx, `
		DELETE FROM dashboard_api_tokens
		WHERE id = $1 AND dashboard_user_id = $2
		RETURNING is_primary`, tokenID, userID).Scan(&primary)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if primary {
		if _, err := tx.Exec(ctx, `UPDATE dashboard_users SET api_token = '', updated_at = now() WHERE id = $1`, userID); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (s *PgStore) DeleteAllDashboardUserAPITokens(ctx context.Context, userID int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM dashboard_api_tokens WHERE dashboard_user_id = $1`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE dashboard_users SET api_token = '', updated_at = now() WHERE id = $1`, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PgStore) DeleteCLIReadToken(ctx context.Context, userID int64, secret string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM dashboard_api_tokens
		WHERE dashboard_user_id = $1 AND token = $2 AND created_via = 'cli_read'`, userID, secret)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}
