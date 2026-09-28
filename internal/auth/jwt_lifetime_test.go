package auth

import (
	"strings"
	"testing"
	"time"
)

func TestAccessTokenLifetime(t *testing.T) {
	mgr, err := NewJWTManager(strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	token, err := mgr.GenerateAccessToken(7, "user@example.com", "user", "User", "user", false)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := mgr.ValidateToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if got := claims.ExpiresAt.Sub(claims.IssuedAt.Time); got != 15*time.Minute {
		t.Fatalf("access lifetime = %v, want 15m", got)
	}
}
