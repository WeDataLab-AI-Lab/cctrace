package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/time/rate"
)

// SyncRateLimiter returns middleware that rate-limits requests.
// rps is the steady-state requests per second; burst is the max burst size.
func SyncRateLimiter(rps float64, burst int) func(http.Handler) http.Handler {
	limiter := rate.NewLimiter(rate.Limit(rps), burst)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !limiter.Allow() {
				writeRateLimited(w, limiter)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// writeRateLimited sends the 429 both limiters share. Kept in one place so a caller
// that hits either one gets the same status, the same Retry-After, and the same body
// -- a client should not have to learn two shapes for the same refusal.
func writeRateLimited(w http.ResponseWriter, limiter *rate.Limiter) {
	retryAfter := time.Second // minimum retry hint
	if reservation := limiter.Reserve(); reservation.OK() {
		retryAfter = reservation.Delay()
		reservation.Cancel()
	}
	if retryAfter < time.Second {
		retryAfter = time.Second
	}
	w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds()+0.5)))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": "rate limit exceeded",
	})
}
