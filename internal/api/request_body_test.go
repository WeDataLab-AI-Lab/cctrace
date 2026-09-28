package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"cctrace/internal/syncer"

	"cctrace/internal/store"
)

// Embed the shared mock and intercept every write used by the ingest routes.
type bodyLimitStore struct {
	mockStore
	calls int
}

func (m *bodyLimitStore) InsertSessionRecords(context.Context, []*store.SessionRecord) error {
	m.calls++
	return nil
}
func (m *bodyLimitStore) UpsertQuotaSnapshot(context.Context, *store.QuotaSnapshot) error {
	m.calls++
	return nil
}
func (m *bodyLimitStore) InsertQuotaSamples(context.Context, []*store.QuotaSample) (int, error) {
	m.calls++
	return 0, nil
}
func (m *bodyLimitStore) IngestProjectRules(context.Context, *store.ProjectRuleIngestRequest) (*store.ProjectRuleIngestResponse, error) {
	m.calls++
	return &store.ProjectRuleIngestResponse{}, nil
}

func TestRequestBodyLimit(t *testing.T) {
	for _, route := range []struct {
		path, body string
		limit      int
	}{
		{"/api/sync", `{"records":[{"session_id":"body-limit"}]}`, maxSyncRequestBodyBytes},
		{"/api/quota", `{"profile_email":"synthetic@example.invalid"}`, maxQuotaRequestBodyBytes},
		{"/api/quota-samples", `{"samples":[]}`, maxQuotaSamplesRequestBodyBytes},
		{"/api/project-rules", `{}`, maxProjectRulesRequestBodyBytes},
	} {
		for _, framing := range []string{"known", "unknown", "chunked"} {
			for _, extra := range []int{0, 1} {
				name := route.path + "/" + framing
				if extra == 0 {
					name += "/at-limit"
				} else {
					name += "/over-limit"
				}
				t.Run(name, func(t *testing.T) {
					m := &bodyLimitStore{}
					body := route.body + strings.Repeat(" ", route.limit+extra-len(route.body))
					req := httptest.NewRequest(http.MethodPost, route.path, strings.NewReader(body))
					if framing != "known" {
						req.ContentLength = -1
					}
					if framing == "chunked" {
						req.TransferEncoding = []string{"chunked"}
					}
					rr := httptest.NewRecorder()
					newTestServer(m, nil).Handler().ServeHTTP(rr, req)
					if extra == 0 {
						if rr.Code != http.StatusOK || m.calls != 1 {
							t.Fatalf("at limit: status=%d store calls=%d body=%s", rr.Code, m.calls, rr.Body.String())
						}
					} else {
						if rr.Code != http.StatusRequestEntityTooLarge || m.calls != 0 {
							t.Fatalf("over limit: status=%d store calls=%d body=%s", rr.Code, m.calls, rr.Body.String())
						}
						if !strings.Contains(rr.Body.String(), "request body too large") {
							t.Fatalf("unclear error: %s", rr.Body.String())
						}
					}
				})
			}
		}
	}
}

// Use the existing transport_identity_test hello record and quota sample fixture
// at their client batch counts. These measure representative batches, not a
// maximum byte size: Raw and metadata fields have no per-request byte budget.
func TestRequestBodyNormalBatches(t *testing.T) {
	ts := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	records := make([]*store.SessionRecord, 200) // syncer.batchSize
	for i := range records {
		records[i] = &store.SessionRecord{Ts: ts, SessionID: "claude-transport-session", RecordType: "user", Raw: json.RawMessage(`{"type":"user","timestamp":"2026-08-26T00:00:00Z","sessionId":"claude-transport-session","cwd":"/tmp/repo","message":{"role":"user","content":"hello"}}`)}
	}
	for _, tc := range []struct {
		path    string
		payload any
	}{
		{"/api/sync", syncer.SyncPayload{ProfileEmail: "profile@example.com", UserID: "claude-user", ProjectHash: "-tmp-repo", Records: records}},
		{"/api/quota", syncer.QuotaPayload{ProfileEmail: strings.Repeat("a", 64) + "@" + strings.Repeat("b", 185) + ".com", UserID: "synthetic-user", FiveHourPct: 100, FiveHourResetsAt: "2026-09-09T00:00:00Z", SevenDayPct: 100, SevenDayResetsAt: "2026-09-09T00:00:00Z", SevenDaySonnetPct: 100, SevenDaySonnetResetsAt: "2026-09-09T00:00:00Z"}},
		{"/api/project-rules", store.ProjectRuleIngestRequest{ProfileEmail: "profile@example.invalid", UserID: "u1", Agent: "claude", Rules: []*store.ProjectRuleSnapshot{{RulePath: "CLAUDE.md", RuleKind: "claude", Status: "active", Content: strings.Repeat("\x01", 1<<20)}}}},

		{"/api/quota-samples", func() quotaSamplesRequest {
			samples := make([]*store.QuotaSample, 2000) // cmd/cctrace backfillChunk
			for i := range samples {
				samples[i] = &store.QuotaSample{BillingProvider: "anthropic", AccountID: "acct-1", WindowKey: "session", SampledAt: ts, UsedPct: 42}
			}
			return quotaSamplesRequest{Samples: samples}
		}()},
		{"/api/quota-samples", func() quotaSamplesRequest {
			samples := make([]*store.QuotaSample, maxQuotaSampleBatch)
			for i := range samples {
				samples[i] = &store.QuotaSample{BillingProvider: "anthropic", AccountID: "acct-1", WindowKey: "session", SampledAt: ts, UsedPct: 42}
			}
			return quotaSamplesRequest{Samples: samples}
		}()},
	} {
		t.Run(tc.path, func(t *testing.T) {
			body, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("serialized batch bytes=%d", len(body))
			m := &bodyLimitStore{}
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(body))
			req.Header.Set("Origin", "http://"+req.Host)
			req.Header.Set("X-Requested-With", "XMLHttpRequest")
			newTestServer(m, nil).Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusOK || m.calls != 1 {
				t.Fatalf("status=%d calls=%d body=%s", rr.Code, m.calls, rr.Body.String())
			}
		})
	}
}

func TestRequestBodyLimitBeforeDecode(t *testing.T) {
	// An invalid first byte would fail decoding immediately. The whole body must
	// be size-checked first, including unknown Content-Length requests.
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/auth/login"}, {http.MethodPut, "/api/admin/deletion-policy"}, {http.MethodPatch, "/api/admin/retention"}, {http.MethodDelete, "/api/projects"},
	} {
		t.Run(route.method, func(t *testing.T) {
			req := httptest.NewRequest(route.method, route.path, strings.NewReader("!"+strings.Repeat(" ", maxRequestBodyBytes)))
			req.ContentLength = -1
			// CSRF runs before the body limit, so the request must look same-origin
			// or it is rejected with 403 before the size check is reached.
			req.Header.Set("Origin", "http://"+req.Host)
			req.Header.Set("X-Requested-With", "XMLHttpRequest")
			rr := httptest.NewRecorder()
			newTestServer(&mockStore{}, nil).Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

type bodyReadProbe struct{ reads int }

func (p *bodyReadProbe) Read([]byte) (int, error) { p.reads++; return 0, io.EOF }
func (p *bodyReadProbe) Close() error             { return nil }

func TestRequestBodyLimitAfterAuthentication(t *testing.T) {
	deny := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	}
	srv := newServer(&mockStore{}, nil, deny, deny)
	for _, path := range []string{"/api/sync", "/api/quota", "/api/quota-samples", "/api/project-rules", "/api/admin/users"} {
		t.Run(path, func(t *testing.T) {
			probe := &bodyReadProbe{}
			req := httptest.NewRequest(http.MethodPost, path, probe)
			rr := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusUnauthorized || probe.reads != 0 {
				t.Fatalf("status=%d body reads=%d", rr.Code, probe.reads)
			}
		})
	}
}

// TotalAlloc is a conservative cumulative allocation measurement, not peak RSS.
func TestRequestBodyAllocationMeasurement(t *testing.T) {
	for _, route := range []struct {
		path, prefix, suffix string
		limit                int
	}{
		{"/api/sync", `{"records":[{"session_id":"`, `"}]}`, maxSyncRequestBodyBytes},
		{"/api/quota", `{"profile_email":"`, `"}`, maxQuotaRequestBodyBytes},
		{"/api/quota-samples", `{"samples":[{"account_id":"`, `"}]}`, maxQuotaSamplesRequestBodyBytes},
		{"/api/project-rules", `{"rules":[{"content":"`, `"}]}`, maxProjectRulesRequestBodyBytes},
	} {
		t.Run(route.path, func(t *testing.T) {
			body := route.prefix + strings.Repeat("a", route.limit-len(route.prefix)-len(route.suffix)) + route.suffix
			srv := newTestServer(&bodyLimitStore{}, nil)
			req := httptest.NewRequest(http.MethodPost, route.path, strings.NewReader(body))
			rr := httptest.NewRecorder()
			runtime.GC()
			var before, afterRead, after runtime.MemStats
			var handler http.HandlerFunc
			switch route.path {
			case "/api/sync":
				handler = srv.handleSync
			case "/api/quota":
				handler = srv.handleQuotaWrite
			case "/api/quota-samples":
				handler = srv.handleQuotaSamplesWrite
			case "/api/project-rules":
				handler = srv.handleProjectRulesIngest
			}
			measured := withRequestBodyLimit(int64(route.limit), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				runtime.ReadMemStats(&afterRead)
				handler(w, r)
			}))
			runtime.ReadMemStats(&before)
			measured.ServeHTTP(rr, req)
			runtime.ReadMemStats(&after)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			t.Logf("wire=%d read_alloc=%d decode_response_alloc=%d total_alloc_delta=%d allocation_ratio=%.3f", len(body), afterRead.TotalAlloc-before.TotalAlloc, after.TotalAlloc-afterRead.TotalAlloc, after.TotalAlloc-before.TotalAlloc, float64(after.TotalAlloc-before.TotalAlloc)/float64(len(body)))
		})
	}
}
