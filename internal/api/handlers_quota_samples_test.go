package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cctrace/internal/auth"
	"cctrace/internal/emailalias"
	"cctrace/internal/store"
)

func quotaSamplesServer(t *testing.T) (*Server, *mockStore) {
	t.Helper()
	m := &mockStore{}
	return newTestServer(m, nil), m
}

func quotaSamplesReadRequest(target string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	return req.WithContext(auth.WithUser(req.Context(), &auth.DashboardUser{ID: 1, Role: "admin"}))
}

// Accepted and inserted are reported separately because they routinely differ:
// the five-minute response cache re-offers the same reading, several profiles
// report the same account, and the backfill may re-walk collected files. A
// re-run inserting nothing is a normal outcome, and collapsing the two numbers
// would make it look like work was done.
func TestQuotaSamplesWrite_reportsAcceptedAndInserted(t *testing.T) {
	s, m := quotaSamplesServer(t)

	body := `{"samples":[
	  {"billing_provider":"anthropic","account_id":"acct-1","window_key":"session",
	   "sampled_at":"2026-08-24T09:00:00Z","used_pct":42}
	]}`
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/quota-samples", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got quotaSamplesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Accepted != 1 || got.Inserted != 1 {
		t.Errorf("accepted/inserted = %d/%d, want 1/1", got.Accepted, got.Inserted)
	}
	if len(m.quotaSamples) != 1 || m.quotaSamples[0].AccountID != "acct-1" {
		t.Errorf("stored = %+v", m.quotaSamples)
	}
}

// An unbounded batch is not a feature the backfill needs — it chunks — so the
// ceiling costs nothing and keeps a malformed or hostile body finite.
func TestQuotaSamplesWrite_rejectsOversizedBatch(t *testing.T) {
	s, _ := quotaSamplesServer(t)

	var b strings.Builder
	b.WriteString(`{"samples":[`)
	for i := 0; i <= maxQuotaSampleBatch; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"billing_provider":"anthropic","account_id":"a","window_key":"session","sampled_at":"2026-08-24T09:00:00Z"}`)
	}
	b.WriteString(`]}`)

	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/quota-samples", strings.NewReader(b.String())))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestQuotaSamplesRead_passesFilters(t *testing.T) {
	s, m := quotaSamplesServer(t)

	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, quotaSamplesReadRequest(
		"/api/quota-samples?from=2026-08-24T00:00:00Z&to=2026-08-25T00:00:00Z&billing_provider=openai&window_minutes=10080"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	f := m.quotaSampleFilter
	if f.BillingProvider != "openai" {
		t.Errorf("provider = %q", f.BillingProvider)
	}
	if f.WindowMinutes != 10080 {
		t.Errorf("window_minutes = %d, want 10080", f.WindowMinutes)
	}
	if !f.From.Equal(time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("from = %v", f.From)
	}
}

// A malformed range must not be silently treated as "no range". That would
// answer a narrow question with the whole table and look like data.
func TestQuotaSamplesRead_rejectsBadTimestamps(t *testing.T) {
	s, _ := quotaSamplesServer(t)
	for _, q := range []string{"?from=yesterday", "?to=soon", "?window_minutes=5h"} {
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, quotaSamplesReadRequest("/api/quota-samples"+q))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, rec.Code)
		}
	}
}

// An empty history is [] rather than null, so the chart's "no data yet" path is
// an empty list and not a decode failure.
func TestQuotaSamplesRead_emptyIsAnEmptyList(t *testing.T) {
	s, _ := quotaSamplesServer(t)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, quotaSamplesReadRequest("/api/quota-samples"))

	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Errorf("body = %q, want []", body)
	}
}

func TestQuotaSamplesRead_allowsDashboardUser(t *testing.T) {
	s, _ := quotaSamplesServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/quota-samples", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.DashboardUser{ID: 7, Role: "user"}))
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// The snapshot route is deliberately left alone by this change: it is what
// older clients speak, and breaking it would stop their collection entirely.
func TestQuotaSnapshotRouteStillServed(t *testing.T) {
	s, _ := quotaSamplesServer(t)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/quota",
		strings.NewReader(`{"profile_email":"one@example.test","five_hour_pct":42}`)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// The snapshot is keyed by profile_email like every other row the profile
// writes, so a profile merged into another reports under the target's address as
// sync and the other ingest paths do. Without that, each report after a merge
// puts a row back under the address the merge just removed.
func TestQuotaSnapshotWrite_resolvesAliases(t *testing.T) {
	m := &mockStore{}
	s := newTestServer(m, nil).WithAliases(emailalias.Aliases{"old@example.test": "new@example.test"})

	for _, email := range []string{"Old@Example.test", "other@Example.test"} {
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/quota",
			strings.NewReader(`{"profile_email":"`+email+`","five_hour_pct":42}`)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body = %s", email, rec.Code, rec.Body.String())
		}
	}

	if len(m.quotaSnapshots) != 2 {
		t.Fatalf("stored %d snapshots, want 2", len(m.quotaSnapshots))
	}
	if got := m.quotaSnapshots[0].ProfileEmail; got != "new@example.test" {
		t.Errorf("aliased profile stored as %q, want new@example.test", got)
	}
	if got := m.quotaSnapshots[1].ProfileEmail; got != "other@Example.test" {
		t.Errorf("unaliased profile stored as %q, want it as received", got)
	}
}

var _ = store.QuotaSample{}
