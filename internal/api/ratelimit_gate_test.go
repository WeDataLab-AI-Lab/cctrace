package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"cctrace/internal/auth"
)

// The outer gate exists to cap what unauthenticated traffic can make the auth lookup
// do. It is shared by everyone, so its budget must be large enough that honest
// callers never reach it -- otherwise the per-user limiter behind it never gets to
// speak, and one caller's burst is refused to everybody.
//
// The number is therefore not free: it has to exceed the per-user rate times the
// number of callers the deployment expects to serve at once. This pins that
// relationship so the two constants cannot drift apart unnoticed.
func TestOpenAPIGateOutrunsConcurrentUsers(t *testing.T) {
	if openAPIGateRPS < openAPIUserRPS*float64(openAPIExpectedConcurrentUsers) {
		t.Fatalf("gate %v rps cannot carry %d callers at %v rps each -- honest users will be "+
			"refused at the shared gate before their own budget is touched",
			openAPIGateRPS, openAPIExpectedConcurrentUsers, openAPIUserRPS)
	}
}

// With the gate sized correctly, a caller spending its own budget must not consume
// anybody else's chance to be served.
func TestOpenAPIGateLetsOthersThroughWhileOneCallerIsThrottled(t *testing.T) {
	gate := SyncRateLimiter(openAPIGateRPS, openAPIGateBurst)
	perUser := UserRateLimiter(openAPIUserRPS, openAPIUserBurst)
	h := gate(perUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	serve := func(id int64) int {
		r := httptest.NewRequest("GET", "/api/open/v1/sessions", nil)
		r = r.WithContext(auth.WithUser(r.Context(), &auth.DashboardUser{ID: id, Role: "user"}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}

	// One caller burns well past its own allowance.
	for i := 0; i < openAPIUserBurst*4; i++ {
		serve(1)
	}
	// Everyone else still gets served.
	for id := int64(2); id <= int64(openAPIExpectedConcurrentUsers); id++ {
		if code := serve(id); code != http.StatusOK {
			t.Fatalf("caller %d = %d, want 200 -- it was refused for traffic it did not send "+
				"(gate %v/%d exhausted by caller 1)", id, code, openAPIGateRPS, openAPIGateBurst)
		}
	}
	_ = fmt.Sprint()
}
