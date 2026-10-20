package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"time"
)

var readContextCredentials = credentialsForContext

// FetchContext reads the explicitly selected Claude home without the legacy
// cache. Credential subprocesses and HTTP share a 20-second request budget.
// Callers must apply their own polling backoff and account-bound stale policy.
func FetchContext(ctx context.Context, claudeDir string) (*Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if claudeDir == "" || !filepath.IsAbs(claudeDir) {
		return nil, errors.New("explicit absolute Claude home required")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	creds, err := readContextCredentials(ctx, filepath.Clean(claudeDir))
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if creds.AccessToken == "" {
		return nil, errors.New("no selected-home OAuth token")
	}
	if err := credentialExpiryError(creds, nowFn()); err != nil {
		return nil, err
	}
	return fetchContextWithCredentials(ctx, creds)
}

func fetchContextWithCredentials(ctx context.Context, creds Credentials) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	req.Header.Set("anthropic-beta", betaHeader)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("usage request: %w", err)
	}
	defer resp.Body.Close()
	if err := credentialHTTPError(resp.StatusCode, creds); err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, &RateLimitError{RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), nowFn())}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("api returned HTTP %d", resp.StatusCode)
	}
	const limit = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("usage body read: %w", err)
	}
	if len(body) > limit {
		return nil, errors.New("usage response exceeds size limit")
	}
	result, err := decodeContextUsage(body)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result.FetchedAt = nowFn()
	result.Plan = planOf(creds)
	result.TokenFingerprint = fingerprintToken(creds.AccessToken)
	return result, nil
}

func decodeContextUsage(body []byte) (*Response, error) {
	// A missing/null percentage must not become an explicit zero through Go's
	// numeric zero value. Preserve the existing typed modern/legacy reader.
	var wire struct {
		FiveHour *struct {
			Utilization *float64 `json:"utilization"`
		} `json:"five_hour"`
		SevenDay *struct {
			Utilization *float64 `json:"utilization"`
		} `json:"seven_day"`
		Limits []struct {
			Kind    string   `json:"kind"`
			Percent *float64 `json:"percent"`
		} `json:"limits"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, errors.New("invalid usage response")
	}
	valid := func(percent *float64) bool { return percent != nil && *percent >= 0 && *percent <= 100 }
	if wire.FiveHour != nil && !valid(wire.FiveHour.Utilization) || wire.SevenDay != nil && !valid(wire.SevenDay.Utilization) {
		return nil, errors.New("invalid usage percentage")
	}
	seen := map[string]bool{}
	for _, limit := range wire.Limits {
		if limit.Kind != KindSession && limit.Kind != KindWeeklyAll {
			continue
		}
		if seen[limit.Kind] || !valid(limit.Percent) {
			return nil, errors.New("invalid or ambiguous usage limit")
		}
		seen[limit.Kind] = true
	}
	var result Response
	if json.Unmarshal(body, &result) != nil || result.Window(KindSession) == nil && result.Window(KindWeeklyAll) == nil {
		return nil, errors.New("no supported usage window")
	}
	return &result, nil
}
