package api

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
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

// The warning names CCTRACE_MAX_SYNC_BODY_BYTES only on the route it raises.
// Session observations share sync's 8 MiB but not its setting: their limit is
// compiled in, and the variable would change nothing there.
func TestBodyNearLimitNamesTheSettingOnlyForSync(t *testing.T) {
	srv := newTestServer(&mockStore{}, nil)
	for _, tc := range []struct {
		path  string
		limit int
		names bool
	}{
		{"/api/sync", maxSyncRequestBodyBytes, true},
		{"/api/session-observations", maxSessionObservationsRequestBodyBytes, false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			var logs bytes.Buffer
			log.SetOutput(&logs)
			defer log.SetOutput(os.Stderr)
			body := strings.Repeat("a", tc.limit-16)
			srv.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(body)))

			if !strings.Contains(logs.String(), tc.path+" body") {
				t.Fatalf("no headroom warning for %s: %q", tc.path, logs.String())
			}
			if got := strings.Contains(logs.String(), "CCTRACE_MAX_SYNC_BODY_BYTES"); got != tc.names {
				t.Errorf("warning names CCTRACE_MAX_SYNC_BODY_BYTES = %v, want %v: %q", got, tc.names, logs.String())
			}
		})
	}
}
