package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The limit is a cliff: a body under it is accepted in full and one over it is
// refused, with nothing in between to warn on. An operator learns the deployment
// outgrew its limit when collection stops, not before. This turns the cliff into
// a slope -- accepted bodies near the limit say so while they are still working.
func TestBodyHeadroomWarning(t *testing.T) {
	const limit = 1 << 20
	for _, tc := range []struct {
		name string
		size int
		warn bool
	}{
		{"comfortably under", limit / 4, false},
		{"just under the warning line", int(float64(limit)*headroomWarnFraction) - 512, false},
		{"close to the limit", int(float64(limit)*headroomWarnFraction) + 512, true},
		{"at the limit", limit, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nearBodyLimit(int64(tc.size), limit); got != tc.warn {
				t.Errorf("size %d of limit %d: warn = %v, want %v", tc.size, limit, got, tc.warn)
			}
		})
	}

	// A body that is refused is not a headroom problem -- it is the limit doing
	// its job, and it already reports itself as 413.
	if nearBodyLimit(limit+1, limit) {
		t.Error("an over-limit body reported as a headroom warning")
	}
}

// The warning must not cost the request anything: it is a log line beside a
// response that still succeeds.
func TestBodyNearLimitStillSucceeds(t *testing.T) {
	const limit = 1 << 20
	body := strings.Repeat("a", limit-16)
	req := httptest.NewRequest(http.MethodPost, "/api/sync", strings.NewReader(body))
	rr := httptest.NewRecorder()

	reached := false
	withRequestBodyLimit(limit, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	})).ServeHTTP(rr, req)

	if !reached || rr.Code != http.StatusOK {
		t.Fatalf("reached = %v status = %d, want the handler to run normally", reached, rr.Code)
	}
}
