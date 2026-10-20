package usage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// AccountResponse binds quota and profile to one OAuth credential epoch.
// Stable identity stays inside the Go provider boundary and is never a wire DTO.
type AccountResponse struct {
	Usage          *Response `json:"-"`
	AccountID      string    `json:"-"`
	OrganizationID string    `json:"-"`
}

var ErrAccountAuthentication = errors.New("account authentication required")

var accountIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// FetchAccountContext uses the same token for /api/oauth/profile and usage.
// It reads neither cached account metadata nor another provider home.
func FetchAccountContext(ctx context.Context, home string) (*AccountResponse, error) {
	return fetchAccountContext(ctx, home, true)
}

// FetchAccountIdentityContext proves the explicit exclusion action without quota opt-in.
func FetchAccountIdentityContext(ctx context.Context, home string) (*AccountResponse, error) {
	return fetchAccountContext(ctx, home, false)
}

func fetchAccountContext(ctx context.Context, home string, includeUsage bool) (*AccountResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if home == "" || !filepath.IsAbs(home) {
		return nil, errors.New("explicit absolute Claude home required")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	creds, err := readContextCredentials(ctx, filepath.Clean(home))
	if err != nil {
		return nil, sanitizeAccountError(ctx, err)
	}
	if creds.AccessToken == "" {
		return nil, ErrAccountAuthentication
	}
	if err := credentialExpiryError(creds, nowFn()); err != nil {
		return nil, err
	}
	endpoint, err := url.Parse(apiURL)
	if err != nil || endpoint.Host == "" {
		return nil, errors.New("account endpoint unavailable")
	}
	endpoint.Path = "/api/oauth/profile"
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	proofCtx, proofCancel := context.WithTimeout(ctx, 10*time.Second)
	defer proofCancel()
	req, err := http.NewRequestWithContext(proofCtx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, errors.New("account proof unavailable")
	}
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cache-Control", "no-cache")
	response, err := httpClient.Do(req)
	if err != nil {
		return nil, sanitizeAccountError(proofCtx, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests {
		return nil, &RateLimitError{RetryAfter: parseRetryAfter(response.Header.Get("Retry-After"), nowFn())}
	}
	if err := credentialHTTPError(response.StatusCode, creds); err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("account proof rejected")
	}
	const limit = 1 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || len(body) > limit {
		return nil, errors.New("account proof invalid")
	}
	var proof struct {
		Account struct {
			UUID string `json:"uuid"`
		} `json:"account"`
		Organization struct {
			UUID string `json:"uuid"`
		} `json:"organization"`
	}
	if json.Unmarshal(body, &proof) != nil || !accountIdentifier.MatchString(proof.Account.UUID) || !accountIdentifier.MatchString(proof.Organization.UUID) {
		return nil, errors.New("account proof invalid")
	}
	var usage *Response
	if includeUsage {
		usage, err = fetchContextWithCredentials(ctx, creds)
		if err != nil {
			return nil, sanitizeAccountError(ctx, err)
		}
	}
	after, err := readContextCredentials(ctx, filepath.Clean(home))
	if err != nil || after.AccessToken != creds.AccessToken {
		return nil, ErrAccountSwitch
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return &AccountResponse{Usage: usage, AccountID: proof.Account.UUID, OrganizationID: proof.Organization.UUID}, nil
}

func sanitizeAccountError(ctx context.Context, err error) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	for _, known := range []error{ErrTokenRefreshRequired, ErrAuthorizationDenied, ErrCredentialAccessRequired, ErrAccountAuthentication} {
		if errors.Is(err, known) {
			return known
		}
	}
	var rate *RateLimitError
	if errors.As(err, &rate) {
		return &RateLimitError{RetryAfter: rate.RetryAfter}
	}
	if err != nil {
		text := strings.ToLower(err.Error())
		if strings.Contains(text, "credential") || strings.Contains(text, "token") {
			return ErrCredentialAccessRequired
		}
		if strings.Contains(text, "invalid usage") || strings.Contains(text, "decode") {
			return errors.New("invalid usage response")
		}
	}
	return errors.New("account provider unavailable")
}
