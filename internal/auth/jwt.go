package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims represents JWT claims for dashboard authentication.
type Claims struct {
	jwt.RegisteredClaims
	Role               string `json:"role"`
	Name               string `json:"name"`
	UserID             int64  `json:"user_id"`
	CctraceUserID      string `json:"cctrace_user_id"`
	MustChangePassword bool   `json:"must_change_password,omitempty"`
	Type               string `json:"type,omitempty"` // "refresh" for refresh tokens
}

// JWTManager handles JWT generation and validation.
type JWTManager struct {
	secret []byte
}

// NewJWTManager creates a JWTManager. Secret must be at least 32 bytes.
func NewJWTManager(secret string) (*JWTManager, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("JWT secret must be at least 32 bytes, got %d", len(secret))
	}
	return &JWTManager{secret: []byte(secret)}, nil
}

// GenerateAccessToken creates a 15-minute access token.
func (m *JWTManager) GenerateAccessToken(userID int64, email, role, name, cctraceUserID string, mustChangePassword bool) (string, error) {
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   email,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(15 * time.Minute)),
		},
		Role:               role,
		Name:               name,
		UserID:             userID,
		CctraceUserID:      cctraceUserID,
		MustChangePassword: mustChangePassword,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(m.secret)
}

// GenerateRefreshToken creates a 7-day refresh token.
func (m *JWTManager) GenerateRefreshToken(userID int64, email string) (string, error) {
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   email,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(7 * 24 * time.Hour)),
		},
		UserID: userID,
		Type:   "refresh",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(m.secret)
}

// ValidateToken parses and validates a JWT token string.
func (m *JWTManager) ValidateToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return m.secret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	return claims, nil
}
