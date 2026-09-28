package airuntime

import (
	"context"
	"errors"
	"time"
)

// Errors an AccountManager returns. Each needs a different fix, so the API
// gives each its own code.
var (
	// ErrEnvManaged: the credentials come from the environment, which the
	// screen cannot change.
	ErrEnvManaged = errors.New("ai runtime account is managed by the environment")
	// ErrAuthFileIsSymlink: the login file links elsewhere, and changing it
	// would change whatever login it points to.
	ErrAuthFileIsSymlink = errors.New("ai runtime auth file is a symlink")
	ErrLoginInProgress   = errors.New("an ai runtime login is already in progress")
	ErrLoginNotFound     = errors.New("ai runtime login not found")
	ErrLoginFailed       = errors.New("ai runtime login failed")
)

// Login states. Pending is the only one that changes.
const (
	LoginPending   = "pending"
	LoginSucceeded = "succeeded"
	LoginFailed    = "failed"
	LoginCanceled  = "canceled"
	LoginExpired   = "expired"
)

// DeviceLogin.Error codes. The runtime's own text goes to the log only.
const (
	LoginErrorFailed         = "login_failed"
	LoginErrorRuntimeExited  = "runtime_exited"
	LoginErrorAuthFileLinked = "auth_file_is_symlink"
)

// AccountManager is implemented by a runtime whose login can be changed from
// the admin screen. Errors wrap ErrNotConfigured, ErrEnvManaged,
// ErrAuthFileIsSymlink, ErrLoginInProgress, ErrLoginNotFound or ErrLoginFailed.
type AccountManager interface {
	Account(ctx context.Context) (Account, error)
	// StartDeviceLogin begins a device code login. One runs at a time; it
	// ends on success, failure, cancel, expiry or CloseLogins.
	StartDeviceLogin(ctx context.Context) (DeviceLogin, error)
	// DeviceLogin reports the latest login by id, finished or not.
	DeviceLogin(id string) (DeviceLogin, error)
	CancelDeviceLogin(ctx context.Context, id string) (DeviceLogin, error)
	// LoginAPIKey stores key as the runtime's login. The key is never kept,
	// logged or echoed in an error.
	LoginAPIKey(ctx context.Context, key string) error
	Logout(ctx context.Context) error
	// CloseLogins ends a pending login, for server shutdown.
	CloseLogins()
}

// Account is the runtime's current login. Login is the pending device login,
// nil when none is pending.
type Account struct {
	AuthMode          string
	Email             string
	PlanType          string
	EnvManaged        bool
	AuthFileIsSymlink bool
	Login             *DeviceLogin
}

// DeviceLogin is one device code login. Error is set only when State is
// LoginFailed.
type DeviceLogin struct {
	ID              string
	VerificationURL string
	UserCode        string
	ExpiresAt       time.Time
	State           string
	Error           string
}
