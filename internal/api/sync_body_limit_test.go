package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// paddedSyncBody pads the smallest valid /api/sync payload out to n wire bytes.
func paddedSyncBody(n int) string {
	const payload = `{"records":[{"session_id":"sync-body-limit"}]}`
	return payload + strings.Repeat(" ", n-len(payload))
}

func postPaddedSync(t *testing.T, srv *Server, m *bodyLimitStore, body string) (int, int) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sync", strings.NewReader(body))
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr.Code, m.calls
}

// Production batches measured 33.9 MiB against the 8 MiB default, and a client
// cannot split a single oversized record, so on that deployment the default is a
// permanent sync stall rather than a bounded rejection (#588). The ceiling has to
// move per deployment without moving the default everyone else inherits.
func TestSyncBodyLimitOverride(t *testing.T) {
	over := paddedSyncBody(maxSyncRequestBodyBytes + 1)

	t.Run("default rejects over the compiled limit", func(t *testing.T) {
		m := &bodyLimitStore{}
		code, calls := postPaddedSync(t, newTestServer(m, nil), m, over)
		if code != http.StatusRequestEntityTooLarge || calls != 0 {
			t.Fatalf("status=%d calls=%d", code, calls)
		}
	})

	t.Run("raised limit accepts the same body", func(t *testing.T) {
		m := &bodyLimitStore{}
		srv := newTestServer(m, nil).WithSyncBodyLimit(MaxConfigurableSyncBodyBytes)
		code, calls := postPaddedSync(t, srv, m, over)
		if code != http.StatusOK || calls != 1 {
			t.Fatalf("status=%d calls=%d", code, calls)
		}
	})

	// A misconfigured value must fall back to the compiled default -- not to no
	// limit, and not to a limit of zero either, which would reject every sync.
	// Both halves are asserted: rejecting the oversized body alone would also
	// pass if the bad value were used verbatim. A value above the ceiling is
	// unusable for the same reason a negative one is: buffering it whole would
	// exhaust the host long before the route refused anything.
	for name, limit := range map[string]int64{
		"zero":              0,
		"negative":          -1,
		"above the ceiling": MaxConfigurableSyncBodyBytes + 1,
	} {
		t.Run("falls back to the default on "+name, func(t *testing.T) {
			m := &bodyLimitStore{}
			srv := newTestServer(m, nil).WithSyncBodyLimit(limit)
			if code, calls := postPaddedSync(t, srv, m, paddedSyncBody(maxSyncRequestBodyBytes)); code != http.StatusOK || calls != 1 {
				t.Fatalf("at the default limit: status=%d calls=%d", code, calls)
			}
			m.calls = 0
			if code, calls := postPaddedSync(t, srv, m, over); code != http.StatusRequestEntityTooLarge || calls != 0 {
				t.Fatalf("over the default limit: status=%d calls=%d", code, calls)
			}
		})
	}

	t.Run("other routes keep their own limit", func(t *testing.T) {
		srv := newTestServer(&bodyLimitStore{}, nil).WithSyncBodyLimit(maxSyncRequestBodyBytes * 8)
		quota := `{"profile_email":"x@example.invalid"}`
		quota += strings.Repeat(" ", maxQuotaRequestBodyBytes+1-len(quota))
		req := httptest.NewRequest(http.MethodPost, "/api/quota", strings.NewReader(quota))
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	})
}
