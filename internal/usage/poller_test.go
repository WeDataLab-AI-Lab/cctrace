package usage

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// stubServer installs a test server as the usage API and returns a counter of
// how many requests actually reached it. Counting requests is the point: the
// defect this package had was issuing one per second indefinitely.
func stubServer(t *testing.T, handler http.HandlerFunc) *atomic.Int64 {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	prevURL, prevToken, prevNow := apiURL, readAccessTokenFn, nowFn
	apiURL = srv.URL
	readAccessTokenFn = func(string) (Credentials, error) {
		return Credentials{AccessToken: "test-token"}, nil
	}
	clearCache()
	t.Cleanup(func() {
		apiURL, readAccessTokenFn, nowFn = prevURL, prevToken, prevNow
		clearCache()
	})
	return &hits
}

// dirA and dirB stand in for two Claude config dirs. The cache is keyed on the
// dir because one machine really does run several at once, holding different
// billing accounts.
const (
	dirA = "/home/alice/.claude"
	dirB = "/home/alice/.claude-work"
)

func decodeResponse(t *testing.T, body string) *Response {
	t.Helper()
	var r Response
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return &r
}

// useFakeClock makes cache expiry testable without sleeping.
func useFakeClock(t *testing.T) func(time.Duration) {
	t.Helper()
	now := time.Date(2026, 8, 13, 14, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return now }
	return func(d time.Duration) { now = now.Add(d) }
}

func okHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"five_hour":{"utilization":42,"resets_at":"2026-08-13T18:00:00Z"}}`))
}

func TestFetchCachesSuccess(t *testing.T) {
	hits := stubServer(t, okHandler)
	advance := useFakeClock(t)

	for i := 0; i < 5; i++ {
		u, err := Fetch(dirA)
		if err != nil {
			t.Fatalf("fetch: %v", err)
		}
		if u.FiveHour.Utilization != 42 {
			t.Fatalf("utilization = %v", u.FiveHour.Utilization)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("%d requests issued, want 1 (cached)", got)
	}

	advance(cacheTTL)
	if _, err := Fetch(dirA); err != nil {
		t.Fatalf("fetch after TTL: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("%d requests after TTL, want 2", got)
	}
}

// The defect: only successes were cached, so a failing endpoint was re-queried
// on every call. The sync loop calls this once per tick, which turned one
// failure into a request per second for as long as the failure lasted.
func TestFetchCachesFailure(t *testing.T) {
	hits := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	advance := useFakeClock(t)

	for i := 0; i < 10; i++ {
		if _, err := Fetch(dirA); err == nil {
			t.Fatal("expected an error")
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("%d requests issued while failing, want 1", got)
	}

	advance(failureCacheTTL)
	_, _ = Fetch(dirA)
	if got := hits.Load(); got != 2 {
		t.Fatalf("%d requests after failure TTL, want 2", got)
	}
}

// A 429 answered with retries is self-perpetuating: the retries keep the limit
// tripped, so the caller never escapes. Retry-After must be honoured.
func TestFetchHonoursRetryAfter(t *testing.T) {
	hits := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1800")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	advance := useFakeClock(t)

	_, err := Fetch(dirA)
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("error = %v, want RateLimitError", err)
	}
	if rl.RetryAfter != 30*time.Minute {
		t.Fatalf("RetryAfter = %v, want 30m", rl.RetryAfter)
	}

	// Well past the ordinary failure TTL but inside the server's window.
	advance(10 * time.Minute)
	_, _ = Fetch(dirA)
	if got := hits.Load(); got != 1 {
		t.Fatalf("%d requests inside the Retry-After window, want 1", got)
	}

	advance(21 * time.Minute)
	_, _ = Fetch(dirA)
	if got := hits.Load(); got != 2 {
		t.Fatalf("%d requests after the window, want 2", got)
	}
}

// A 429 without the header still must not spin; it falls back to the ordinary
// failure TTL.
func TestFetchRateLimitedWithoutRetryAfter(t *testing.T) {
	hits := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	useFakeClock(t)

	for i := 0; i < 5; i++ {
		_, _ = Fetch(dirA)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("%d requests issued, want 1", got)
	}
}

// An absurd Retry-After must not park the poller forever.
func TestFetchClampsRetryAfter(t *testing.T) {
	stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "999999")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	useFakeClock(t)

	_, err := Fetch(dirA)
	if ttl := failureTTL(err); ttl != maxFailureCacheTTL {
		t.Fatalf("cache ttl = %v, want the %v clamp", ttl, maxFailureCacheTTL)
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	now := time.Date(2026, 8, 13, 14, 0, 0, 0, time.UTC)
	got := parseRetryAfter(now.Add(90*time.Second).Format(http.TimeFormat), now)
	// HTTP-date has second granularity, so allow the boundary.
	if got < 89*time.Second || got > 90*time.Second {
		t.Fatalf("parseRetryAfter = %v, want ~90s", got)
	}
}

// A value large enough to overflow the seconds-to-Duration multiplication would
// wrap into a small or negative delay — turning "wait a very long time" into
// "retry at once", the exact behaviour this package exists to prevent.
func TestParseRetryAfterSaturatesHugeValues(t *testing.T) {
	now := time.Date(2026, 8, 13, 14, 0, 0, 0, time.UTC)
	for _, in := range []string{"99999999999999", "9223372036854775807"} {
		got := parseRetryAfter(in, now)
		if got != maxRetryAfter {
			t.Fatalf("parseRetryAfter(%q) = %v, want the %v saturation", in, got, maxRetryAfter)
		}
		if ttl := failureTTL(&RateLimitError{RetryAfter: got}); ttl != maxFailureCacheTTL {
			t.Fatalf("cache ttl for %q = %v, want the %v clamp", in, ttl, maxFailureCacheTTL)
		}
	}
}

func TestParseRetryAfterRejectsGarbage(t *testing.T) {
	now := time.Date(2026, 8, 13, 14, 0, 0, 0, time.UTC)
	for _, in := range []string{"", "soon", "-5", now.Add(-time.Hour).Format(http.TimeFormat)} {
		if got := parseRetryAfter(in, now); got != 0 {
			t.Fatalf("parseRetryAfter(%q) = %v, want 0", in, got)
		}
	}
}

// A missing token is a local condition — no request should be attempted at all.
func TestFetchWithoutTokenDoesNotCallAPI(t *testing.T) {
	hits := stubServer(t, okHandler)
	useFakeClock(t)
	readAccessTokenFn = func(string) (Credentials, error) { return Credentials{}, nil }

	if _, err := Fetch(dirA); err == nil {
		t.Fatal("expected an error for a missing token")
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("%d requests issued without a token, want 0", got)
	}
}

// One machine runs several Claude config dirs at once, holding *different*
// billing accounts. A single package-level cache would serve one dir's usage to
// another and the poller would report it under the wrong account — a wrong,
// plausible value rather than a missing one.
func TestFetchCacheIsPerConfigDir(t *testing.T) {
	hits := stubServer(t, okHandler)
	advance := useFakeClock(t)
	readAccessTokenFn = func(dir string) (Credentials, error) {
		return Credentials{AccessToken: "tok" + dir}, nil
	}

	if _, err := Fetch(dirA); err != nil {
		t.Fatalf("fetch A: %v", err)
	}
	if _, err := Fetch(dirB); err != nil {
		t.Fatalf("fetch B: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("%d requests for two dirs, want 2 (no cross-dir cache hit)", got)
	}

	// Each dir still caches on its own.
	_, _ = Fetch(dirA)
	_, _ = Fetch(dirB)
	if got := hits.Load(); got != 2 {
		t.Fatalf("%d requests after re-fetching both, want 2 (cached)", got)
	}

	advance(cacheTTL)
	_, _ = Fetch(dirA)
	if got := hits.Load(); got != 3 {
		t.Fatalf("%d requests after A's TTL, want 3", got)
	}
}

// The failure cache is what keeps a broken endpoint from being re-queried once
// per tick (#198). It must stay per-dir too: a 429 against one account is not a
// reason to stop polling another.
func TestFailureCacheIsPerConfigDir(t *testing.T) {
	hits := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	useFakeClock(t)

	for i := 0; i < 5; i++ {
		_, _ = Fetch(dirA)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("%d requests for dir A while failing, want 1", got)
	}

	_, _ = Fetch(dirB)
	if got := hits.Load(); got != 2 {
		t.Fatalf("%d requests after dir B, want 2 (A's failure must not park B)", got)
	}
}

// FetchedAt is the sampled_at of the history table. It has to be the moment the
// response was actually retrieved, not the moment it was sent on: the five
// minute cache means the same response is handed back repeatedly, and an
// identical FetchedAt is what lets the primary key drop the duplicate.
func TestFetchStampsFetchedAt(t *testing.T) {
	stubServer(t, okHandler)
	advance := useFakeClock(t)

	first, err := Fetch(dirA)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if first.FetchedAt.IsZero() {
		t.Fatal("FetchedAt is zero; the field is declared but never set")
	}

	advance(time.Minute)
	again, err := Fetch(dirA)
	if err != nil {
		t.Fatalf("fetch cached: %v", err)
	}
	if !again.FetchedAt.Equal(first.FetchedAt) {
		t.Errorf("cached FetchedAt = %v, want the original %v", again.FetchedAt, first.FetchedAt)
	}
}

// The cache was keyed on the config dir alone, so one dir that changes account
// -- the ordinary `claude login` to a second account -- kept serving the
// previous account's reading for up to five minutes under the new name. Keying
// on the identity too makes the switch invalidate the entry at once.
//
// Mutation: drop the account uuid from the cache key and this fails: the second
// Fetch is served from the first account's cached entry.
func TestFetchCacheIsInvalidatedByAnAccountSwitch(t *testing.T) {
	hits := stubServer(t, okHandler)
	useFakeClock(t)

	acct := "acct-user-a"
	prev := readAccountUUIDFn
	readAccountUUIDFn = func(string) (string, error) { return acct, nil }
	t.Cleanup(func() { readAccountUUIDFn = prev })

	if _, err := Fetch(dirA); err != nil {
		t.Fatalf("fetch before switch: %v", err)
	}
	if _, err := Fetch(dirA); err != nil {
		t.Fatalf("re-fetch before switch: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("%d requests before the switch, want 1 (cached)", got)
	}

	acct = "acct-wedata"
	if _, err := Fetch(dirA); err != nil {
		t.Fatalf("fetch after switch: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("%d requests after the switch, want 2 (cache must not carry over)", got)
	}
}

// The reading has to say which credentials produced it, or the caller cannot
// tell that the identity it is about to stamp came from somewhere else.
//
// Mutation: stop setting TokenFingerprint in fetch() and this fails.
func TestFetchStampsTheCredentialsItUsed(t *testing.T) {
	stubServer(t, okHandler)
	useFakeClock(t)

	u, err := Fetch(dirA)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if u.TokenFingerprint != fingerprintToken("test-token") {
		t.Fatalf("TokenFingerprint = %q, want the fingerprint of the token used", u.TokenFingerprint)
	}
}
