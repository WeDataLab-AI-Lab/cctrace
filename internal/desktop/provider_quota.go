package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cctrace/internal/codexappserver"
	"cctrace/internal/usage"
)

const providerReadTimeout = 25 * time.Second
const maxCodexAuthBytes = 1 << 20

// ProviderQuotaReader supplies typed quota data for one explicitly selected
// provider home. Callers pass a bounded context and invoke readers synchronously.
type ProviderQuotaReader interface {
	ReadClaudeQuota(ctx context.Context, claudeHome string) (*usage.Response, error)
	ReadCodexQuota(ctx context.Context, codexHome string) (*codexappserver.Snapshot, error)
}

type liveProviderQuotaReader struct{}

func (liveProviderQuotaReader) ReadClaudeQuota(ctx context.Context, claudeHome string) (*usage.Response, error) {
	return usage.FetchContext(ctx, claudeHome)
}

func (liveProviderQuotaReader) ReadCodexQuota(ctx context.Context, codexHome string) (*codexappserver.Snapshot, error) {
	return codexappserver.Fetch(ctx, codexHome)
}

type providerQuotaSnapshot struct {
	lastSuccessAt time.Time
	identity      string
	windows       []quotaWindow
}

func (s *Service) readProviderQuota(ctx context.Context, provider string) quotaResult {
	home := s.claudeHome
	if provider == "codex" {
		home = s.codexHome
	}
	result, _ := s.readProviderQuotaAtHome(ctx, provider, home, provider, nil)
	return result
}

func (s *Service) readProviderQuotaAtHome(ctx context.Context, provider, home, cacheKey string, observeIdentity func(string) bool) (quotaResult, bool) {
	if home == "" {
		return quotaUnavailable(provider, "provider_not_configured", safeProviderErrorMessage("provider_not_configured")), false
	}

	readCtx, cancel := context.WithTimeout(ctx, providerReadTimeout)
	defer cancel()

	var (
		sampledAt     time.Time
		identity      string
		codexChangeAt time.Time
		windows       []quotaWindow
		err           error
	)
	switch provider {
	case "claude":
		var response *usage.Response
		response, err = s.providerReader.ReadClaudeQuota(readCtx, home)
		if response != nil {
			identity = response.TokenFingerprint
		}
		if ctxErr := readCtx.Err(); ctxErr != nil {
			err = ctxErr
		}
		if observeIdentity != nil && identity != "" && !observeIdentity(identity) {
			return quotaUnavailable(provider, "account_changed", safeProviderErrorMessage("account_changed")), true
		}
		if err == nil {
			windows, err = claudeQuotaWindows(response)
			if response != nil {
				sampledAt = response.FetchedAt
			}
		}
	case "codex":
		if err := readCtx.Err(); err != nil {
			return s.providerQuotaFailureForKey(provider, cacheKey, err, ""), false
		}
		var fingerprint string
		fingerprint, err = codexAuthFingerprint(home)
		if err != nil {
			return s.providerQuotaFailureForKey(provider, cacheKey, err, ""), false
		}
		identity = "codex-auth:" + fingerprint
		codexChangeAt = s.observeCodexAuth(home, fingerprint)
		if observeIdentity != nil && !observeIdentity(identity) {
			return quotaUnavailable(provider, "account_changed", safeProviderErrorMessage("account_changed")), true
		}
		if err := readCtx.Err(); err != nil {
			return s.providerQuotaFailureForKey(provider, cacheKey, err, identity), false
		}
		var snapshot *codexappserver.Snapshot
		snapshot, err = s.providerReader.ReadCodexQuota(readCtx, home)
		if ctxErr := readCtx.Err(); ctxErr != nil {
			return s.providerQuotaFailureForKey(provider, cacheKey, ctxErr, identity), false
		}
		fingerprintAfter, fingerprintErr := codexAuthFingerprint(home)
		if fingerprintErr != nil {
			return s.providerQuotaFailureForKey(provider, cacheKey, fingerprintErr, ""), false
		}
		if fingerprintAfter != fingerprint {
			codexChangeAt = s.observeCodexAuth(home, fingerprintAfter)
			identity = "codex-auth:" + fingerprintAfter
			if observeIdentity != nil {
				_ = observeIdentity(identity)
			}
			err = errCodexIdentityChanged
		}
		if ctxErr := readCtx.Err(); ctxErr != nil {
			return s.providerQuotaFailureForKey(provider, cacheKey, ctxErr, identity), false
		}
		if err == nil {
			windows, err = codexQuotaWindows(snapshot)
			if snapshot != nil {
				sampledAt = snapshot.FetchedAt
			}
		}
	}
	if err != nil {
		return s.providerQuotaFailureForKey(provider, cacheKey, err, identity), false
	}
	if provider == "codex" && sampledAt.Before(codexChangeAt) {
		return s.providerQuotaFailureForKey(provider, cacheKey, errCodexIdentityChanged, identity), false
	}
	now := s.now().UTC()
	if sampledAt.IsZero() || sampledAt.After(now) || len(windows) == 0 {
		return s.providerQuotaFailureForKey(provider, cacheKey, errInvalidProviderResponse, identity), false
	}
	if now.Sub(sampledAt) > quotaStaleAfter {
		return s.providerQuotaFailureForKey(provider, cacheKey, errQuotaSnapshotExpired, identity), false
	}
	snapshot := providerQuotaSnapshot{
		lastSuccessAt: sampledAt.UTC(),
		identity:      identity,
		windows:       append([]quotaWindow(nil), windows...),
	}
	s.quotaMu.Lock()
	if provider == "codex" && cacheKey == provider && s.codexAuthHash[home] != strings.TrimPrefix(identity, "codex-auth:") {
		delete(s.lastQuota, cacheKey)
		s.quotaMu.Unlock()
		return quotaUnavailable(provider, "account_changed", safeProviderErrorMessage("account_changed")), false
	}
	previous := s.lastQuota[cacheKey]
	if previous != nil && (previous.lastSuccessAt.After(now) || now.Sub(previous.lastSuccessAt) > quotaStaleAfter) {
		delete(s.lastQuota, cacheKey)
		previous = nil
	}
	if identity == "" {
		delete(s.lastQuota, cacheKey)
	} else if previous != nil && previous.identity == identity && previous.identity != "" && previous.lastSuccessAt.After(snapshot.lastSuccessAt) {
		snapshot = *previous
	} else {
		s.lastQuota[cacheKey] = &snapshot
	}
	if provider == "codex" && s.codexAuthHash[home] == strings.TrimPrefix(identity, "codex-auth:") {
		delete(s.codexChangedAt, home)
	}
	s.quotaMu.Unlock()
	return providerQuotaResult(provider, snapshot, now, nil), false
}

func (s *Service) providerQuotaFailureForKey(provider, cacheKey string, err error, identity string) quotaResult {
	code := providerQuotaErrorCode(err)
	if code == "authentication_required" || code == "account_changed" {
		s.quotaMu.Lock()
		delete(s.lastQuota, cacheKey)
		s.quotaMu.Unlock()
		return quotaUnavailable(provider, code, safeProviderErrorMessage(code))
	}

	now := s.now().UTC()
	s.quotaMu.Lock()
	snapshot := s.lastQuota[cacheKey]
	if snapshot != nil && snapshot.identity != "" && identity != "" && snapshot.identity != identity {
		delete(s.lastQuota, cacheKey)
		snapshot = nil
		code = "account_changed"
	}
	if snapshot != nil && (identity == "" || snapshot.identity == "" || snapshot.identity != identity || snapshot.lastSuccessAt.After(now) || now.Sub(snapshot.lastSuccessAt) > quotaStaleAfter) {
		delete(s.lastQuota, cacheKey)
		snapshot = nil
	}
	if snapshot != nil {
		copyOfSnapshot := *snapshot
		copyOfSnapshot.windows = append([]quotaWindow(nil), snapshot.windows...)
		snapshot = &copyOfSnapshot
	}
	s.quotaMu.Unlock()
	if snapshot == nil {
		return quotaUnavailable(provider, code, safeProviderErrorMessage(code))
	}
	return providerQuotaResult(provider, *snapshot, now, &ErrorObject{Code: code, Message: safeProviderErrorMessage(code)})
}

func providerQuotaResult(provider string, snapshot providerQuotaSnapshot, now time.Time, failure *ErrorObject) quotaResult {
	age := now.Sub(snapshot.lastSuccessAt)
	if age < 0 || age > quotaStaleAfter {
		return quotaUnavailable(provider, "provider_unavailable", safeProviderErrorMessage("provider_unavailable"))
	}
	lastSuccessAt := snapshot.lastSuccessAt.UTC().Format(time.RFC3339Nano)
	state := "fresh"
	if failure != nil {
		state = "stale"
	}
	windows := append([]quotaWindow(nil), snapshot.windows...)
	return quotaResult{Provider: provider, State: state, LastSuccessAt: &lastSuccessAt, Windows: windows, Error: failure}
}

func claudeQuotaWindows(response *usage.Response) ([]quotaWindow, error) {
	if response == nil {
		return nil, errInvalidProviderResponse
	}
	windows := make([]quotaWindow, 0, 2)
	for _, item := range []struct {
		name   string
		window *usage.UsageWindow
	}{
		{name: "five_hour", window: response.Window(usage.KindSession)},
		{name: "seven_day", window: response.Window(usage.KindWeeklyAll)},
	} {
		if item.window == nil {
			continue
		}
		if math.IsNaN(item.window.Utilization) || math.IsInf(item.window.Utilization, 0) || item.window.Utilization < 0 || item.window.Utilization > 100 {
			return nil, errInvalidProviderResponse
		}
		if _, err := time.Parse(time.RFC3339Nano, item.window.ResetsAt); err != nil {
			continue
		}
		windows = append(windows, quotaWindow{Name: item.name, Utilization: item.window.Utilization, ResetsAt: item.window.ResetsAt})
	}
	if len(windows) == 0 {
		return nil, errInvalidProviderResponse
	}
	return windows, nil
}

func codexQuotaWindows(snapshot *codexappserver.Snapshot) ([]quotaWindow, error) {
	if snapshot == nil {
		return nil, errInvalidProviderResponse
	}
	windows := make([]quotaWindow, 0, 2)
	seen := map[string]bool{}
	for _, reading := range snapshot.Readings {
		name := ""
		switch reading.WindowMinutes {
		case 300:
			name = "five_hour"
		case 10080:
			name = "seven_day"
		default:
			continue
		}
		if reading.LimitID != "codex" || seen[name] || reading.ResetsAt == nil {
			continue
		}
		if math.IsNaN(reading.UsedPercent) || math.IsInf(reading.UsedPercent, 0) || reading.UsedPercent < 0 || reading.UsedPercent > 100 {
			return nil, errInvalidProviderResponse
		}
		reset := reading.ResetsAt.UTC()
		windows = append(windows, quotaWindow{Name: name, Utilization: reading.UsedPercent, ResetsAt: reset.Format(time.RFC3339Nano)})
		seen[name] = true
	}
	if len(windows) == 0 {
		return nil, errInvalidProviderResponse
	}
	return windows, nil
}

var errInvalidProviderResponse = errors.New("invalid provider response")
var errCodexIdentityUnavailable = errors.New("codex authentication identity unavailable")
var errCodexIdentityChanged = errors.New("codex authentication identity changed")
var errQuotaSnapshotExpired = errors.New("quota snapshot expired")

func codexAuthFingerprint(home string) (string, error) {
	if home == "" || !filepath.IsAbs(home) {
		return "", errCodexIdentityUnavailable
	}
	path := filepath.Join(home, "auth.json")
	pathBefore, err := os.Lstat(path)
	if err != nil || pathBefore.Mode()&os.ModeSymlink != 0 || !pathBefore.Mode().IsRegular() || pathBefore.Size() > maxCodexAuthBytes {
		return "", errCodexIdentityUnavailable
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errCodexIdentityUnavailable
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Size() > maxCodexAuthBytes || !os.SameFile(pathBefore, opened) {
		return "", errCodexIdentityUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(file, maxCodexAuthBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxCodexAuthBytes {
		return "", errCodexIdentityUnavailable
	}
	after, fileErr := file.Stat()
	currentPath, pathErr := os.Lstat(path)
	if fileErr != nil || pathErr != nil || currentPath.Mode()&os.ModeSymlink != 0 || !currentPath.Mode().IsRegular() ||
		!os.SameFile(opened, after) || !os.SameFile(opened, currentPath) ||
		opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) ||
		after.Size() != currentPath.Size() || !after.ModTime().Equal(currentPath.ModTime()) {
		return "", errCodexIdentityUnavailable
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func (s *Service) observeCodexAuth(home, fingerprint string) time.Time {
	now := s.now().UTC()
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	previous, exists := s.codexAuthHash[home]
	if !exists || previous != fingerprint {
		s.codexAuthHash[home] = fingerprint
		s.codexChangedAt[home] = now
		delete(s.lastQuota, "codex")
	}
	return s.codexChangedAt[home]
}

func providerQuotaErrorCode(err error) string {
	for code, known := range map[string]error{"token_refresh_required": usage.ErrTokenRefreshRequired, "authorization_denied": usage.ErrAuthorizationDenied, "credential_access_required": usage.ErrCredentialAccessRequired, "recovery_blocked": usage.ErrRecoveryBlocked} {
		if errors.Is(err, known) {
			return code
		}
	}
	if errors.Is(err, context.Canceled) {
		return "request_cancelled"
	}
	if errors.Is(err, errCodexIdentityUnavailable) {
		return "authentication_required"
	}
	if errors.Is(err, errCodexIdentityChanged) {
		return "account_changed"
	}
	if errors.Is(err, errQuotaSnapshotExpired) {
		return "provider_unavailable"
	}
	if errors.Is(err, usage.ErrAccountSwitch) {
		return "account_changed"
	}
	var rateLimit *usage.RateLimitError
	if errors.As(err, &rateLimit) {
		return "rate_limited"
	}
	if errors.Is(err, errInvalidProviderResponse) {
		return "invalid_provider_response"
	}
	var syntaxError *json.SyntaxError
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &syntaxError) || errors.As(err, &typeError) {
		return "invalid_provider_response"
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "account changed") || strings.Contains(message, "identity changed") || strings.Contains(message, "account switch") {
		return "account_changed"
	}
	if strings.Contains(message, "no oauth token") || strings.Contains(message, "no selected-home oauth token") ||
		strings.Contains(message, "selected credential file unavailable") || strings.Contains(message, "no anthropic oauth token") ||
		strings.Contains(message, "not logged in") || strings.Contains(message, "login required") ||
		strings.Contains(message, "unauthorized") || strings.Contains(message, "authentication required") ||
		strings.Contains(message, "authorization required") || strings.Contains(message, "invalid_grant") ||
		strings.Contains(message, "http 401") || strings.Contains(message, "http 403") {
		return "authentication_required"
	}
	if strings.Contains(message, "decode") || strings.Contains(message, "invalid character") ||
		strings.Contains(message, "unexpected end of json") || strings.Contains(message, "invalid usage") ||
		strings.Contains(message, "no supported usage window") || strings.Contains(message, "usage response exceeds size limit") {
		return "invalid_provider_response"
	}
	return "provider_unavailable"
}

func safeProviderErrorMessage(code string) string {
	switch code {
	case "token_refresh_required":
		return "Claude session refresh is required"
	case "credential_access_required":
		return "Provider credential access is required"
	case "authorization_denied":
		return "Provider authorization was denied"
	case "recovery_blocked":
		return "Claude session recovery is unavailable"
	case "provider_not_configured":
		return "provider home is not configured"
	case "authentication_required":
		return "provider authorization is required"
	case "account_changed":
		return "provider account changed; refresh required"
	case "rate_limited":
		return "provider quota request was rate limited"
	case "request_cancelled":
		return "provider quota refresh was cancelled"
	case "invalid_provider_response":
		return "provider returned an unsupported quota response"
	default:
		return "provider quota is unavailable"
	}
}
