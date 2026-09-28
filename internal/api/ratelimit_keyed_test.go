package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cctrace/internal/auth"
)

// These test UserRateLimiter because that is what is wired. An earlier round tested
// a token-keyed variant that the wiring then stopped using, so removing the per-user
// keying entirely left every test green -- the suite was guarding an implementation
// nobody called.

func userReq(userID int64) *http.Request {
	r := httptest.NewRequest("GET", "/api/open/v1/sessions", nil)
	if userID > 0 {
		ctx := auth.WithUser(r.Context(), &auth.DashboardUser{ID: userID, Role: "user"})
		r = r.WithContext(ctx)
	}
	return r
}

func serveAs(h http.Handler, userID int64) int {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, userReq(userID))
	return rec.Code
}

func okHandler(rl func(http.Handler) http.Handler) http.Handler {
	return rl(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
}

// The property the whole change exists for. Remove the per-user keying and this is
// the test that goes red.
func TestUserRateLimiterIsolatesCallers(t *testing.T) {
	h := okHandler(UserRateLimiter(1, 1))

	if code := serveAs(h, 1); code != http.StatusOK {
		t.Fatalf("first request for user 1 = %d, want 200", code)
	}
	if code := serveAs(h, 1); code != http.StatusTooManyRequests {
		t.Fatalf("second request for user 1 = %d, want 429", code)
	}
	if code := serveAs(h, 2); code != http.StatusOK {
		t.Fatalf("user 2 = %d, want 200 -- blocked by user 1's usage", code)
	}
}

// Requests with no user in context share one bucket. This limiter sits behind
// authentication, so that should not happen; if it ever does, unattributed traffic
// must not each get a fresh budget.
func TestUserRateLimiterSharesOneBucketWhenUnattributed(t *testing.T) {
	h := okHandler(UserRateLimiter(1, 1))

	if code := serveAs(h, 0); code != http.StatusOK {
		t.Fatalf("first unattributed request = %d, want 200", code)
	}
	if code := serveAs(h, 0); code != http.StatusTooManyRequests {
		t.Fatalf("second unattributed request = %d, want 429", code)
	}
}

func TestUserRateLimiterBoundsMemory(t *testing.T) {
	rl := newTokenRateLimiter(100, 100)
	rl.maxKeys = 4
	for i := 0; i < 50; i++ {
		rl.limiterFor(fmt.Sprintf("user-%d", i), time.Now())
	}
	if n := rl.size(); n > 4 {
		t.Fatalf("limiter holds %d buckets, cap is 4", n)
	}
}

func TestUserRateLimiterReclaimsIdleBuckets(t *testing.T) {
	rl := newTokenRateLimiter(100, 100)
	rl.idleTTL = 10 * time.Millisecond
	rl.limiterFor("transient", time.Now())
	if rl.size() != 1 {
		t.Fatalf("size = %d, want 1", rl.size())
	}
	time.Sleep(20 * time.Millisecond)
	rl.reap(time.Now())
	if n := rl.size(); n != 0 {
		t.Fatalf("size = %d after going idle, want 0", n)
	}
}

var _ = context.Background
