package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"cctrace/internal/store"
)

type coverageCaptureStore struct {
	mockStore
	calls  int
	filter store.CoverageGapFilter
}

func (c *coverageCaptureStore) CoverageGapStats(_ context.Context, f store.CoverageGapFilter) (*store.CoverageGap, error) {
	c.calls++
	c.filter = f
	return &store.CoverageGap{Eligible: true, CoverageRatio: 0.78, KMethod: store.CoverageKMethod}, nil
}

func coverageGet(t *testing.T, c *coverageCaptureStore, query string) store.CoverageGap {
	t.Helper()
	srv := newTestServer(c, nil)
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stats/coverage-gap?"+query, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got store.CoverageGap
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

const coverageRange = "since=2026-08-24T00:00:00Z&until=2026-08-31T00:00:00Z"

func TestCoverageGap_passesRangeAndAccountToStore(t *testing.T) {
	c := &coverageCaptureStore{}
	got := coverageGet(t, c, coverageRange+"&login_email=A@x.test&granularity=hour&tz=Asia/Seoul&model_category=anthropic")

	if !got.Eligible || got.CoverageRatio != 0.78 {
		t.Fatalf("response = %+v", got)
	}
	if c.filter.Granularity != "hour" || c.filter.Timezone != "Asia/Seoul" {
		t.Errorf("bucketing not forwarded: %+v", c.filter)
	}
	if c.calls != 1 || c.filter.LoginEmail != "A@x.test" ||
		c.filter.Since.Format("2006-01-02") != "2026-08-24" || c.filter.Until.Format("2006-01-02") != "2026-08-31" {
		t.Errorf("store called %d times with %+v", c.calls, c.filter)
	}
}

// Quota is account-level with no session, project, model or profile attribution.
// A usage subset minus whole-account burn is not a residual, so every filter the
// trend chart can apply makes the answer ineligible -- as a 200, because this
// polls beside two other queries and a 4xx would surface a normal state as an
// error.
func TestCoverageGap_refusesFilteredScopes(t *testing.T) {
	cases := map[string]string{
		"project":    "project_hash=abc",
		"projects":   "project_hashes=abc,def",
		"user":       "user_id=u1",
		"profile":    "profile_email=p@x.test",
		"team":       "user_team=t1",
		"model":      "model=opus",
		"agent":      "agent=codex",
		"compatible": "model_category=compatible",
	}
	for name, q := range cases {
		t.Run(name, func(t *testing.T) {
			c := &coverageCaptureStore{}
			got := coverageGet(t, c, coverageRange+"&"+q)
			if got.Eligible || got.Reason == "" {
				t.Errorf("eligible = %v, reason = %q", got.Eligible, got.Reason)
			}
			if c.calls != 0 {
				t.Errorf("store was called for an ineligible request")
			}
		})
	}
}

func TestCoverageGap_requiresARange(t *testing.T) {
	c := &coverageCaptureStore{}
	got := coverageGet(t, c, "granularity=day")
	if got.Eligible || c.calls != 0 {
		t.Errorf("eligible = %v, calls = %d", got.Eligible, c.calls)
	}
}

// A three-hour window moves the integer weekly meter by a point or two, which
// is no signal for a range total. The bucket overlay is exempt: the reader
// asked for it on the range they are looking at.
func TestCoverageGap_refusesARangeShorterThanADay(t *testing.T) {
	c := &coverageCaptureStore{}
	got := coverageGet(t, c, "since=2026-08-24T00:00:00Z&until=2026-08-24T03:00:00Z")
	if got.Eligible || got.Reason == "" || c.calls != 0 {
		t.Errorf("eligible = %v, reason = %q, calls = %d", got.Eligible, got.Reason, c.calls)
	}

	c = &coverageCaptureStore{}
	coverageGet(t, c, "since=2026-08-24T00:00:00Z&until=2026-08-24T03:00:00Z&granularity=minute")
	if c.calls != 1 || c.filter.Granularity != "minute" {
		t.Errorf("bucket request was refused: calls = %d, filter = %+v", c.calls, c.filter)
	}
}
