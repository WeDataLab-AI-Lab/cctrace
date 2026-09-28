package api

import (
	"net/http/httptest"
	"testing"
	"time"
)

// TestParseTimeRange guards the plugins/skills "All" period filter (issue #41).
// The dashboard "All" filter sends since=epoch (1970, with millis) and no until;
// until must default to now so the range is [1970, now] (full history). If until
// defaulted to the zero time, "All" would query ts < 0001-01-01 and return nothing.
func TestParseTimeRange(t *testing.T) {
	t.Run("epoch since with missing until spans full history", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/plugins?since=1970-01-01T00:00:00.000Z", nil)
		before := time.Now()
		since, until := parseTimeRange(req)
		after := time.Now()

		if !since.Equal(time.Unix(0, 0)) {
			t.Fatalf("since = %v, want epoch (1970-01-01T00:00:00Z)", since)
		}
		if until.Before(before) || until.After(after) {
			t.Fatalf("until = %v, want ~now within [%v, %v]", until, before, after)
		}
	})

	t.Run("missing since defaults to 7 days ago", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/plugins", nil)
		since, _ := parseTimeRange(req)

		want := time.Now().AddDate(0, 0, -7)
		if diff := since.Sub(want); diff > time.Minute || diff < -time.Minute {
			t.Fatalf("since = %v, want ~%v (now-7d)", since, want)
		}
	})

	t.Run("explicit since and until are honored", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/plugins?since=2026-01-01T00:00:00Z&until=2026-02-01T00:00:00Z", nil)
		since, until := parseTimeRange(req)

		if got := since.UTC().Format(time.RFC3339); got != "2026-01-01T00:00:00Z" {
			t.Fatalf("since = %v, want 2026-01-01T00:00:00Z", got)
		}
		if got := until.UTC().Format(time.RFC3339); got != "2026-02-01T00:00:00Z" {
			t.Fatalf("until = %v, want 2026-02-01T00:00:00Z", got)
		}
	})
}

func TestLenientQueryTimeRange(t *testing.T) {
	t.Run("returns parsed optional bounds", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/?since=2026-01-01T00:00:00Z&until=2026-02-01T00:00:00Z", nil)
		since, until := lenientQueryTimeRange(req)
		if since == nil || since.Format(time.RFC3339) != "2026-01-01T00:00:00Z" {
			t.Fatalf("since = %v", since)
		}
		if until == nil || until.Format(time.RFC3339) != "2026-02-01T00:00:00Z" {
			t.Fatalf("until = %v", until)
		}
	})

	t.Run("ignores missing and malformed bounds", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/?since=not-a-time", nil)
		since, until := lenientQueryTimeRange(req)
		if since != nil || until != nil {
			t.Fatalf("range = (%v, %v), want nil bounds", since, until)
		}
	})
}
