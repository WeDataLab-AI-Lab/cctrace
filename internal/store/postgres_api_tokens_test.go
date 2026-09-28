package store

import (
	"context"
	"testing"
	"time"
)

func TestPgStore_DashboardAPITokenLifecycle(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	var userID int64
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO dashboard_users (email, password_hash, role, name)
		VALUES ('tokens@example.com', 'hash', 'user', 'Token User')
		RETURNING id
	`).Scan(&userID); err != nil {
		t.Fatal(err)
	}

	first, err := s.CreateDashboardUserAPIToken(ctx, userID, "CI", "cct_first", "web", nil)
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Microsecond)
	second, err := s.CreateDashboardUserAPIToken(ctx, userID, "Reporting", "cct_second", "web", &expiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("expected distinct token records")
	}
	if first.CreatedVia != "web" || first.RotatedAt != nil || !first.IsActive || first.ExpiresAt != nil {
		t.Fatalf("unexpected creation metadata: %+v", first)
	}
	if second.ExpiresAt == nil || !second.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("expected custom expiration %v, got %+v", expiresAt, second.ExpiresAt)
	}

	tokens, err := s.ListDashboardUserAPITokens(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(tokens))
	}

	rotated, err := s.RotateDashboardUserAPIToken(ctx, userID, first.ID, "cct_rotated")
	if err != nil {
		t.Fatal(err)
	}
	if rotated.ID != first.ID || rotated.CreatedAt != first.CreatedAt || rotated.RotatedAt == nil {
		t.Fatalf("rotation did not preserve identity and creation metadata: before=%+v after=%+v", first, rotated)
	}
	if _, err := s.GetDashboardUserByApiToken(ctx, "cct_first"); err == nil {
		t.Fatal("old token still authenticates after rotation")
	}
	owner, err := s.GetDashboardUserByApiToken(ctx, "cct_rotated")
	if err != nil {
		t.Fatal(err)
	}
	if owner.ID != userID {
		t.Fatalf("expected owner %d, got %d", userID, owner.ID)
	}

	deleted, err := s.DeleteDashboardUserAPIToken(ctx, userID, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("expected token deletion")
	}
	tokens, err = s.ListDashboardUserAPITokens(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].ID != second.ID {
		t.Fatalf("expected only the second token to remain, got %+v", tokens)
	}
}

func TestPgStore_DashboardAPITokenActiveAndExpirationGateAuthentication(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	var userID int64
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO dashboard_users (email, password_hash, role, name)
		VALUES ('mode@example.com', 'hash', 'user', 'Mode User')
		RETURNING id
	`).Scan(&userID); err != nil {
		t.Fatal(err)
	}

	token, err := s.CreateDashboardUserAPIToken(ctx, userID, "Automation", "cct_mode", "web", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetDashboardUserByApiToken(ctx, "cct_mode"); err != nil {
		t.Fatalf("active unlimited token should authenticate: %v", err)
	}

	token, err = s.SetDashboardUserAPITokenActive(ctx, userID, token.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if token.IsActive {
		t.Fatal("expected inactive token metadata")
	}
	if _, err := s.GetDashboardUserByApiToken(ctx, "cct_mode"); err == nil {
		t.Fatal("inactive token authenticated")
	}

	past := time.Now().Add(-time.Hour)
	if _, err := s.SetDashboardUserAPITokenExpiration(ctx, userID, token.ID, &past); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetDashboardUserAPITokenActive(ctx, userID, token.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetDashboardUserByApiToken(ctx, "cct_mode"); err == nil {
		t.Fatal("expired token authenticated")
	}

	if _, err := s.SetDashboardUserAPITokenExpiration(ctx, userID, token.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetDashboardUserByApiToken(ctx, "cct_mode"); err != nil {
		t.Fatalf("active unlimited token should authenticate again: %v", err)
	}
}

func TestPgStore_PrimaryAPITokenTracksCLICompatibility(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	var userID int64
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO dashboard_users (email, password_hash, role, name)
		VALUES ('cli@example.com', 'hash', 'user', 'CLI User')
		RETURNING id
	`).Scan(&userID); err != nil {
		t.Fatal(err)
	}

	if err := s.SetDashboardUserApiToken(ctx, userID, "cct_cli"); err != nil {
		t.Fatal(err)
	}
	tokens, err := s.ListDashboardUserAPITokens(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].CreatedVia != "api" || tokens[0].Name != "CLI token" {
		t.Fatalf("unexpected CLI token metadata: %+v", tokens)
	}

	// Password reset is not revocation: the current credential remains valid.
	if err := s.UpdateDashboardUserPassword(ctx, userID, "replacement-hash", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetDashboardUserByApiToken(ctx, "cct_cli"); err != nil {
		t.Fatalf("password reset unexpectedly revoked the token: %v", err)
	}
	if _, err := s.CreateDashboardUserAPIToken(ctx, userID, "Integration", "cct_named", "web", nil); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteAllDashboardUserAPITokens(ctx, userID); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"cct_cli", "cct_named"} {
		if _, err := s.GetDashboardUserByApiToken(ctx, secret); err == nil {
			t.Fatalf("revoked token still authenticates: %s", secret)
		}
	}
	user, err := s.GetDashboardUserByID(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if user.ApiToken != "" {
		t.Fatal("legacy CLI token was not cleared")
	}
}
