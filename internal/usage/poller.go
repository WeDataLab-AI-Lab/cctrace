package usage

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"cctrace/internal/claudeauth"
)

const (
	betaHeader = "oauth-2025-04-20"
	cacheTTL   = 5 * time.Minute

	// failureCacheTTL keeps a failure from being retried immediately. Only
	// successes used to be cached, so a failing endpoint was re-queried on every
	// call — and the sync loop calls this once per tick, turning one failure into
	// a request per second for as long as it lasted.
	failureCacheTTL = time.Minute

	// maxFailureCacheTTL bounds how long a server-supplied Retry-After may park
	// the poller, so an absurd value cannot silence quota reporting indefinitely.
	maxFailureCacheTTL = time.Hour

	// maxRetryAfter saturates a parsed Retry-After, and maxRetryAfterSeconds is
	// the same bound in whole seconds so the header can be range-checked before
	// it is multiplied into a time.Duration. The value only has to exceed
	// maxFailureCacheTTL for the clamp above to take over.
	maxRetryAfter        = 24 * time.Hour
	maxRetryAfterSeconds = int(maxRetryAfter / time.Second)
)

// Overridable for tests: the API address, the HTTP client, the token source and
// the clock are all seams the package had no way to substitute before.
var (
	apiURL            = "https://api.anthropic.com/api/oauth/usage"
	httpClient        = &http.Client{Timeout: 10 * time.Second}
	readAccessTokenFn = ReadAccessTokenFor
	readAccountUUIDFn = claudeauth.ReadAccountUUID
	nowFn             = time.Now
)

// RateLimitError reports a 429 and, when the server said so, how long to wait.
//
// Retrying through a rate limit is self-perpetuating: the retries keep the limit
// tripped, so the caller never escapes it. One client was observed issuing
// 1,293,935 such requests.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("rate limited (retry in %s)", e.RetryAfter.Round(time.Second))
	}
	return "rate limited (retry later)"
}

// UsageWindow holds utilization and reset time for one rate-limit bucket.
type UsageWindow struct {
	Utilization float64 `json:"utilization"` // 0.0–100.0
	ResetsAt    string  `json:"resets_at"`   // ISO8601

	// Severity and ScopeLabel only ever come from limits[]; the fixed
	// top-level fields carry neither. Severity is the server's own
	// normal/critical call, which saves the chart from inventing a threshold.
	Severity   string `json:"-"`
	ScopeLabel string `json:"-"`
	// IsActive is the server's own flag, carried through rather than acted on
	// here — see limits.go for why it does not mean "in force".
	IsActive *bool `json:"-"`
}

// ResetTime parses ResetsAt as time.Time. Returns zero value on error.
func (w *UsageWindow) ResetTime() time.Time {
	t, _ := time.Parse(time.RFC3339Nano, w.ResetsAt)
	return t
}

// ExtraUsage holds overage/add-on credit info.
type ExtraUsage struct {
	IsEnabled    bool     `json:"is_enabled"`
	MonthlyLimit *float64 `json:"monthly_limit"`
	UsedCredits  *float64 `json:"used_credits"`
	Utilization  *float64 `json:"utilization"`
}

// Response is the full response from the OAuth usage API.
//
// Read windows through Window(kind) rather than these fields — see limits.go
// for why the fixed fields cannot be trusted on their own any more.
type Response struct {
	FiveHour       *UsageWindow `json:"five_hour"`
	SevenDay       *UsageWindow `json:"seven_day"`
	SevenDaySonnet *UsageWindow `json:"seven_day_sonnet"`
	SevenDayOpus   *UsageWindow `json:"seven_day_opus"`
	ExtraUsage     *ExtraUsage  `json:"extra_usage"`
	Limits         []Limit      `json:"limits"`
	Spend          *Spend       `json:"spend"`

	// FetchedAt is when this response actually came off the wire, and it is the
	// sampled_at of the history table. The five minute cache hands the same
	// response back repeatedly; stamping it here rather than at send time is
	// what lets the primary key drop those repeats.
	FetchedAt time.Time `json:"-"`

	// Plan is the subscription tier of the credentials this was fetched with.
	// The response itself does not name it — identity is implicit in which
	// token asked — so it is carried over from the credentials. It is the
	// lookup key for the price the weighted average divides by.
	Plan string `json:"-"`

	// TokenFingerprint says which credentials produced this reading, without
	// carrying them: a truncated digest of the token, never the token. The
	// caller labels the reading from a second store, and this is the only way
	// it can tell that the two stores stopped describing the same account --
	// see CheckAccountBinding.
	TokenFingerprint string `json:"-"`
}

// cached holds the last outcome — success or failure — and how long it stands.
type cached struct {
	data      *Response
	err       error
	fetchedAt time.Time
	ttl       time.Duration
}

func (c *cached) isStale(now time.Time) bool {
	return c == nil || now.Sub(c.fetchedAt) >= c.ttl
}

// cache is keyed on the Claude config dir *and* the account it currently holds.
// One machine really does run several dirs at once holding *different* billing
// accounts, so a single shared entry would serve one account's usage under
// another's name — a wrong, plausible value rather than a missing one. The
// account is part of the key because one dir does the same thing across time:
// `claude login` to a second account leaves an entry that stays fresh for five
// more minutes and is then read under the new identity. The TTLs, the failure
// caching and the Retry-After clamp all keep their meaning.
var (
	cacheMu sync.Mutex
	cache   = map[string]*cached{}
)

// cacheKey pairs the dir with the account, using a separator that cannot occur
// in either so two different pairs cannot spell the same key.
func cacheKey(claudeDir, accountUUID string) string {
	return claudeDir + "\x00" + accountUUID
}

func clearCache() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cache = map[string]*cached{}
}

// Fetch returns usage data for one Claude config dir from an in-memory cache,
// refreshing it when stale. An empty claudeDir means the default home.
//
// Failures are cached too. Caching only successes meant a failing endpoint was
// re-queried by every caller on every call, with no upper bound on the rate.
func Fetch(claudeDir string) (*Response, error) {
	// Read outside the lock, and treat a failure as an unknown account rather
	// than a failed poll: the account is only the cache key here, and the
	// caller that stamps rows reads the same file and reports the same error a
	// moment later. Losing the reading over it would turn one unreadable config
	// file into missing usage as well as missing history.
	accountUUID, _ := readAccountUUIDFn(claudeDir)
	key := cacheKey(claudeDir, accountUUID)

	cacheMu.Lock()
	defer cacheMu.Unlock()

	now := nowFn()
	if c := cache[key]; !c.isStale(now) {
		return c.data, c.err
	}
	data, err := fetch(claudeDir, now)
	if err != nil {
		cache[key] = &cached{err: err, fetchedAt: now, ttl: failureTTL(err)}
		return nil, err
	}
	cache[key] = &cached{data: data, fetchedAt: now, ttl: cacheTTL}
	return data, nil
}

// failureTTL decides how long a failure stands. A server that told us when to
// come back is obeyed, within a clamp; anything else waits the ordinary
// failure interval.
func failureTTL(err error) time.Duration {
	var rl *RateLimitError
	if errors.As(err, &rl) && rl.RetryAfter > 0 {
		if rl.RetryAfter > maxFailureCacheTTL {
			return maxFailureCacheTTL
		}
		return rl.RetryAfter
	}
	return failureCacheTTL
}

// parseRetryAfter reads the Retry-After header in either permitted form —
// delay-seconds or an HTTP-date — and returns 0 for anything unusable, so a
// malformed header falls back to the ordinary failure interval rather than
// parking the poller on a bogus value.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		// Saturate instead of multiplying blindly: a large enough value overflows
		// time.Duration and wraps to a small or negative delay, inverting the
		// header into "retry at once".
		if secs > maxRetryAfterSeconds {
			return maxRetryAfter
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

func fetch(claudeDir string, now time.Time) (*Response, error) {
	creds, err := readAccessTokenFn(claudeDir)
	if err != nil {
		return nil, fmt.Errorf("read oauth token: %w", err)
	}
	if creds.AccessToken == "" {
		return nil, fmt.Errorf("no OAuth token found (run 'claude login')")
	}

	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	req.Header.Set("anthropic-beta", betaHeader)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, &RateLimitError{RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), nowFn())}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("api returned HTTP %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	result.FetchedAt = now
	result.Plan = planOf(creds)
	result.TokenFingerprint = fingerprintToken(creds.AccessToken)
	return &result, nil
}

// planOf picks the identifier the price lookup is keyed on.
//
// rateLimitTier is preferred because it is the finer of the two: both a Max 5x
// and a Max 20x account report subscriptionType "max", and those are different
// subscriptions at different prices. Keyed on the coarser value the two would
// collapse into one row and the weighted average would price one of them wrong
// while still drawing a line.
//
// subscriptionType remains the floor for anything that reports no tier, so the
// worst case equals the previous behaviour rather than an empty plan.
func planOf(creds Credentials) string {
	if creds.RateLimitTier != "" {
		return creds.RateLimitTier
	}
	return creds.SubscriptionType
}

// FormatBar renders a utilization percentage as a compact progress bar.
// e.g. FormatBar(37.0, 20) → "[███████░░░░░░░░░░░░░]  37%"
func FormatBar(pct float64, width int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(pct / 100.0 * float64(width))
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	return fmt.Sprintf("[%s] %5.1f%%", bar, pct)
}
