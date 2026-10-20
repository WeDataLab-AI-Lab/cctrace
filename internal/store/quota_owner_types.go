package store

import (
	"errors"
	"time"
)

var (
	ErrQuotaBindingNotFound   = errors.New("quota account binding not found")
	ErrQuotaBindingUnverified = errors.New("quota account binding is not approved")
	ErrQuotaBindingInvalid    = errors.New("invalid quota account binding")
	ErrQuotaAccountExcluded   = errors.New("quota account is excluded")
)

// QuotaAccountBinding is a dashboard owner's claim that one provider account is
// theirs. Owner and administrator API handlers select the fields they expose.
type QuotaAccountBinding struct {
	ID                    int64
	DashboardUserID       int64
	OwnerEmail            string
	BillingProvider       string
	AccountID             string
	Status                string
	RequestedAt           time.Time
	UpdatedAt             time.Time
	VerifiedAt            *time.Time
	ApprovedBy            *int64
	VerificationMethod    string
	VerificationReference string
}

// QuotaOwnerSample is a client-reported rate-limit observation. It purposely
// has no login email, profile email, or session path field.
type QuotaOwnerSample struct {
	BillingProvider string     `json:"billing_provider"`
	AccountID       string     `json:"account_id"`
	WindowKey       string     `json:"window_key"`
	SampledAt       time.Time  `json:"sampled_at"`
	UsedPct         float64    `json:"used_pct"`
	ResetsAt        *time.Time `json:"resets_at,omitempty"`
	WindowMinutes   *int       `json:"window_minutes,omitempty"`
	Plan            string     `json:"plan,omitempty"`
	ScopeLabel      string     `json:"scope_label,omitempty"`
	Severity        string     `json:"severity,omitempty"`
	IsActive        *bool      `json:"is_active,omitempty"`
	Attribution     string     `json:"attribution"`
}

// QuotaUsageWindow distinguishes a measured zero from an unavailable reading.
type QuotaUsageWindow struct {
	UsedPct     *float64   `json:"used_pct"`
	ResetsAt    *time.Time `json:"resets_at"`
	SampledAt   *time.Time `json:"sampled_at"`
	Attribution string     `json:"attribution"`
	State       string     `json:"state"`
	Source      string     `json:"source"`
	Reason      string     `json:"reason,omitempty"`
}

type QuotaUsageWindows struct {
	FiveHour QuotaUsageWindow `json:"five_hour"`
	SevenDay QuotaUsageWindow `json:"seven_day"`
}

type QuotaUsageAccount struct {
	BillingProvider string            `json:"billing_provider"`
	AccountID       string            `json:"account_id"`
	Label           string            `json:"label"`
	Plan            string            `json:"plan"`
	BindingStatus   string            `json:"binding_status"`
	Windows         QuotaUsageWindows `json:"windows"`
}

type QuotaWindowPoint struct {
	ElapsedFraction float64   `json:"elapsed_fraction"`
	UsedPct         float64   `json:"used_pct"`
	SampledAt       time.Time `json:"sampled_at"`
	Attribution     string    `json:"attribution"`
}

type QuotaComparisonWindow struct {
	WindowStart time.Time          `json:"window_start"`
	ResetsAt    time.Time          `json:"resets_at"`
	WindowKey   string             `json:"window_key"`
	Points      []QuotaWindowPoint `json:"points"`
}

type QuotaQuantilePoint struct {
	ElapsedFraction float64  `json:"elapsed_fraction"`
	P25             *float64 `json:"p25"`
	P50             *float64 `json:"p50"`
	P75             *float64 `json:"p75"`
	N               int      `json:"n"`
}

type QuotaWindowComparison struct {
	BillingProvider string                  `json:"billing_provider"`
	AccountID       string                  `json:"account_id"`
	WindowMinutes   int                     `json:"window_minutes"`
	Current         *QuotaComparisonWindow  `json:"current"`
	History         []QuotaComparisonWindow `json:"history"`
	Quantiles       []QuotaQuantilePoint    `json:"quantiles"`
	HistoryCount    int                     `json:"history_count"`
	Truncated       bool                    `json:"truncated"`
	Source          string                  `json:"source"`
	Reason          string                  `json:"reason,omitempty"`
}
