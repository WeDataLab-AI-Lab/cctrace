package api

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"cctrace/internal/auth"
)

// SyncRateLimiter puts every caller in one bucket. In front of login and CLI auth
// that is correct: the request is not identified yet, so a per-caller budget would
// only invite an attacker to vary the key.
//
// In front of a read API whose point is that people write their own scripts, it is
// wrong. One person's loop spends the budget and everyone else is refused for
// something they did not do. UserRateLimiter gives each authenticated caller its own.
//
// It is keyed on the user the authentication middleware already resolved, which is
// why it must be wrapped INSIDE that middleware. An earlier version keyed on the
// bearer token and ran in front, and had to ask the database on every request whether
// the token was real -- a round trip the middleware behind it then repeated. Sitting
// behind authentication removes the question entirely.
type tokenRateLimiter struct {
	rps   rate.Limit
	burst int
	// maxKeys caps how many buckets are held. Beyond it, new callers share the
	// anonymous bucket: a bounded refusal to isolate is better than unbounded
	// memory held on behalf of whoever sent the most distinct tokens.
	maxKeys int
	// idleTTL is how long a bucket outlives its last request.
	idleTTL time.Duration

	mu        sync.Mutex
	buckets   map[string]*tokenBucket
	anonymous *rate.Limiter
	lastReap  time.Time
}

type tokenBucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func newTokenRateLimiter(rps float64, burst int) *tokenRateLimiter {
	return &tokenRateLimiter{
		rps:       rate.Limit(rps),
		burst:     burst,
		maxKeys:   10000,
		idleTTL:   30 * time.Minute,
		buckets:   make(map[string]*tokenBucket),
		anonymous: rate.NewLimiter(rate.Limit(rps), burst),
		lastReap:  time.Now(),
	}
}

func (l *tokenRateLimiter) limiterFor(token string, now time.Time) *rate.Limiter {
	if token == "" {
		return l.anonymous
	}
	// Buckets are keyed by a digest, not the token. A limiter map is not a secret
	// store, and nothing here needs to recover the original value.
	sum := sha256.Sum256([]byte(token))
	key := hex.EncodeToString(sum[:])

	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastReap) >= l.idleTTL {
		l.reapLocked(now)
	}
	if b, ok := l.buckets[key]; ok {
		b.lastSeen = now
		return b.limiter
	}
	if len(l.buckets) >= l.maxKeys {
		l.reapLocked(now)
	}
	if len(l.buckets) >= l.maxKeys {
		return l.anonymous
	}
	b := &tokenBucket{limiter: rate.NewLimiter(l.rps, l.burst), lastSeen: now}
	l.buckets[key] = b
	return b.limiter
}

func (l *tokenRateLimiter) reap(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reapLocked(now)
}

func (l *tokenRateLimiter) reapLocked(now time.Time) {
	for key, b := range l.buckets {
		if now.Sub(b.lastSeen) >= l.idleTTL {
			delete(l.buckets, key)
		}
	}
	l.lastReap = now
}

func (l *tokenRateLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// UserRateLimiter limits each authenticated caller separately, keyed on the user the
// authentication middleware already resolved. It must be wrapped INSIDE that
// middleware, not outside it.
//
// Keying on the bearer token instead would mean looking the token up to know whether
// it is real -- a database round trip on every request, with the middleware behind
// repeating the same lookup. Sitting behind authentication removes the question: the
// user is in the context already.
//
// A thin global limiter still belongs in front, so an unauthenticated flood cannot
// drive the auth lookup itself. Two layers, each doing what it is good at.
func UserRateLimiter(rps float64, burst int) func(http.Handler) http.Handler {
	l := newTokenRateLimiter(rps, burst)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := ""
			if user, ok := auth.UserFromContext(r.Context()); ok && user != nil {
				key = strconv.FormatInt(user.ID, 10)
			}
			limiter := l.limiterFor(key, time.Now())
			if !limiter.Allow() {
				writeRateLimited(w, limiter)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Open API rate limits. The gate is shared; the per-user budget is not.
const (
	// openAPIUserRPS is what one caller may sustain. Chosen so a script polling a
	// few endpoints in a loop is comfortable and a runaway one is not.
	openAPIUserRPS   = 20.0
	openAPIUserBurst = 50

	// openAPIExpectedConcurrentUsers is how many callers the gate must carry at full
	// per-user rate without refusing any of them. Sized for a team, not a crowd --
	// raise it, and the gate, before onboarding a larger fleet.
	openAPIExpectedConcurrentUsers = 25

	// openAPIGateRPS caps unauthenticated pressure on the auth lookup. It is
	// deliberately well above what honest callers can consume: it is a backstop, not
	// a ration. See TestOpenAPIGateOutrunsConcurrentUsers.
	openAPIGateRPS   = openAPIUserRPS * openAPIExpectedConcurrentUsers
	openAPIGateBurst = openAPIUserBurst * openAPIExpectedConcurrentUsers
)
