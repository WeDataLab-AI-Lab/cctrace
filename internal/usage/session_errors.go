package usage

import (
	"errors"
	"time"
)

var (
	ErrTokenRefreshRequired     = errors.New("token refresh required")
	ErrAuthorizationDenied      = errors.New("authorization denied")
	ErrCredentialAccessRequired = errors.New("credential access required")
	ErrRecoveryBlocked          = errors.New("session recovery blocked")
)

func credentialExpiryError(c Credentials, now time.Time) error {
	if !c.ExpiresAt.IsZero() && !c.ExpiresAt.After(now) {
		if c.RefreshAvailable {
			return ErrTokenRefreshRequired
		}
		return ErrAccountAuthentication
	}
	return nil
}

func credentialHTTPError(status int, c Credentials) error {
	if status == 403 {
		return ErrAuthorizationDenied
	}
	if status == 401 {
		if c.RefreshAvailable {
			return ErrTokenRefreshRequired
		}
		return ErrAccountAuthentication
	}
	return nil
}
