package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cctrace/internal/aireport"
	"cctrace/internal/airuntime"
	"cctrace/internal/auth"
	"cctrace/internal/sessionrecord"
	"cctrace/internal/store"
)

type judgementBucketCountStore struct {
	store.Store
	read func(context.Context, store.SessionOverviewFilter) (map[string]int, error)
}

func (m *judgementBucketCountStore) SessionOverviewJudgementBucketCounts(ctx context.Context, f store.SessionOverviewFilter) (map[string]int, error) {
	return m.read(ctx, f)
}

type p5APIMockStore struct {
	*mockStore
	settings             store.SessionAIRecordSettings
	granted              bool
	grantedAt            *time.Time
	search               []store.SessionRecordSearchResult
	searchIncludeRecords bool
	deleted              int64
	deleteCalled         bool
	status               store.SessionRecordRangeStatus
	rangeCalled          bool
	regenerateCalled     bool
	regenerateErr        error
}

func (m *p5APIMockStore) GetSessionAIRecordSettings(context.Context) (*store.SessionAIRecordSettings, error) {
	return &m.settings, nil
}
func (m *p5APIMockStore) SessionRecordConsent(context.Context, int64, string, string) (bool, *time.Time, error) {
	return m.granted, m.grantedAt, nil
}
func (m *p5APIMockStore) GrantSessionRecordConsent(context.Context, int64, string, string) error {
	m.granted = true
	return nil
}
func (m *p5APIMockStore) RevokeSessionRecordConsent(context.Context, int64, string) error {
	m.granted = false
	return nil
}
func (m *p5APIMockStore) SearchMySessionRecords(_ context.Context, q, owner, email string, useUserID, includeRecords bool, limit int) ([]store.SessionRecordSearchResult, error) {
	if q != "api" || owner != "record-owner" || email != "" || !useUserID || limit != 7 {
		return nil, context.Canceled
	}
	m.searchIncludeRecords = includeRecords
	if !includeRecords {
		return []store.SessionRecordSearchResult{}, nil
	}
	return m.search, nil
}
func (m *p5APIMockStore) DeleteOwnSessionAIRecords(_ context.Context, owner, email string, useUserID bool) (int64, error) {
	if owner != "record-owner" || email != "" || !useUserID {
		return 0, context.Canceled
	}
	m.deleteCalled = true
	return m.deleted, nil
}

func (m *p5APIMockStore) SessionRecordRangeStatus(_ context.Context, id int64, owner, email string, useUserID bool, key, version string, from, to, now time.Time) (store.SessionRecordRangeStatus, error) {
	if (id != 41 && id != 42) || !useUserID || owner != "record-owner" || email != "" || key != "claude-api:api_key" || version != "2026-10-07" || !from.Before(to) {
		return store.SessionRecordRangeStatus{}, context.Canceled
	}
	return m.status, nil
}

func (m *p5APIMockStore) RequestSessionRecordRange(_ context.Context, id int64, owner, email string, useUserID bool, key, version string, from, to, now time.Time) error {
	if (id != 41 && id != 42) || !useUserID || owner != "record-owner" || email != "" || key != "claude-api:api_key" || version != "2026-10-07" || !from.Before(to) {
		return context.Canceled
	}
	m.rangeCalled = true
	return nil
}

func (m *p5APIMockStore) RequestSessionRecord(_ context.Context, sessionID, owner, email string, useUserID bool, id int64, key, version string, force bool, now time.Time) error {
	if sessionID != "session-1" || (id != 41 && id != 42) || !useUserID || owner != "record-owner" || email != "" || key != "claude-api:api_key" || version != "2026-10-07" || !force {
		return context.Canceled
	}
	m.regenerateCalled = true
	return m.regenerateErr
}

func TestSessionRecordConsentAndSearchContractsAreOwnerScoped(t *testing.T) {
	user := &auth.DashboardUser{ID: 41, Email: "record-owner@example.test", Role: "user", CctraceUserID: "record-owner"}
	settings := store.SessionAIRecordSettings{Enabled: true, Runtime: "claude-api"}
	now := time.Now().UTC()
	m := &p5APIMockStore{mockStore: &mockStore{}, settings: settings, granted: true, grantedAt: &now,
		search: []store.SessionRecordSearchResult{{SessionID: "mine", Matches: []store.SessionRecordSearchMatch{{Field: "headline", Text: "Synthetic API"}}}}}
	srv := newTestServer(m, nil).WithSessionRecordsEnabled(true)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, personalAPIRequest(http.MethodGet, "/api/session-records/consent", "", user, false))
	if rec.Code != http.StatusOK {
		t.Fatalf("consent status=%d body=%s", rec.Code, rec.Body.String())
	}
	var consent map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &consent); err != nil {
		t.Fatal(err)
	}
	if consent["runtime_key"] != "claude-api:api_key" || consent["disclosure_version"] != "2026-10-07" || consent["granted"] != true || consent["disclosure"] == "" {
		t.Fatalf("consent contract=%v", consent)
	}
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, personalAPIRequest(http.MethodGet, "/api/session-search?q=api&limit=7", "", user, false))
	if rec.Code != http.StatusOK || !m.searchIncludeRecords {
		t.Fatalf("enabled search status=%d includeRecords=%v body=%s", rec.Code, m.searchIncludeRecords, rec.Body.String())
	}
	srv.WithSessionRecordsEnabled(false)
	m.searchIncludeRecords = true
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, personalAPIRequest(http.MethodGet, "/api/session-search?q=api&limit=7", "", user, false))
	if rec.Code != http.StatusOK || m.searchIncludeRecords {
		t.Fatalf("disabled search status=%d includeRecords=%v", rec.Code, m.searchIncludeRecords)
	}
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, personalAPIRequest(http.MethodDelete, "/api/session-records?scope=mine", "", user, true))
	if rec.Code != http.StatusOK || !m.deleteCalled {
		t.Fatalf("own deletion status=%d called=%v body=%s", rec.Code, m.deleteCalled, rec.Body.String())
	}
}

func TestSessionRecordConsentRequiresCurrentRuntimeAndDisclosure(t *testing.T) {
	user := &auth.DashboardUser{ID: 42, Email: "owner@example.test", Role: "user", CctraceUserID: "record-owner"}
	m := &p5APIMockStore{mockStore: &mockStore{}, settings: store.SessionAIRecordSettings{Enabled: true, Runtime: "claude-api"}}
	runtime := &airuntime.FakeRuntime{InfoValue: airuntime.Info{Key: "claude-api", Model: "synthetic-model", AuthMode: airuntime.AuthModeAPIKey},
		StatusValue: airuntime.Status{Configured: true, Available: true}}
	ai := aireport.NewService(aireport.NewMemStore(), []airuntime.Runtime{runtime}, aireport.Config{EnvRuntime: "claude-api"})
	srv := newTestServer(m, nil).WithAIReports(ai).WithSessionRecordsEnabled(true)
	body := `{"runtime_key":"claude-api:api_key","disclosure_version":"2026-10-07"}`
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, personalAPIRequest(http.MethodPost, "/api/session-records/consent", body, user, true))
	if rec.Code != http.StatusNoContent || !m.granted {
		t.Fatalf("current consent status=%d granted=%v body=%s", rec.Code, m.granted, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, personalAPIRequest(http.MethodPost, "/api/session-records/consent", `{"runtime_key":"claude-api:api_key","disclosure_version":"old"}`, user, true))
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale disclosure status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, personalAPIRequest(http.MethodDelete, "/api/session-records/consent", "", user, true))
	if rec.Code != http.StatusNoContent || m.granted {
		t.Fatalf("consent withdrawal status=%d granted=%v body=%s", rec.Code, m.granted, rec.Body.String())
	}
	if sessionrecord.RecordDisclosureVersion != "2026-10-07" {
		t.Fatalf("consent test is out of date: version %s", sessionrecord.RecordDisclosureVersion)
	}
}

func TestSessionRecordRefreshStatusAndRegenerationContracts(t *testing.T) {
	user := &auth.DashboardUser{ID: 41, Email: "owner@example.test", Role: "user", CctraceUserID: "record-owner"}
	m := &p5APIMockStore{mockStore: &mockStore{}, settings: store.SessionAIRecordSettings{Enabled: true, Runtime: "claude-api"}, granted: true,
		status: store.SessionRecordRangeStatus{State: "writing", Total: 2, Writing: 1, Blocked: map[string]int{}}}
	runtime := &airuntime.FakeRuntime{InfoValue: airuntime.Info{Key: "claude-api", Model: "synthetic-model", AuthMode: airuntime.AuthModeAPIKey}, StatusValue: airuntime.Status{Configured: true, Available: true}}
	ai := aireport.NewService(aireport.NewMemStore(), []airuntime.Runtime{runtime}, aireport.Config{EnvRuntime: "claude-api"})
	srv := newTestServer(m, nil).WithAIReports(ai).WithSessionRecordsEnabled(true)
	from, to := "2026-10-01T00:00:00Z", "2026-10-02T00:00:00Z"
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, personalAPIRequest(http.MethodGet, "/api/session-records/status?from="+from+"&to="+to, "", user, false))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"writing"`) {
		t.Fatalf("status response=%d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, personalAPIRequest(http.MethodPost, "/api/session-records/refresh", `{"from":"`+from+`","to":"`+to+`"}`, user, true))
	if rec.Code != http.StatusAccepted || !m.rangeCalled {
		t.Fatalf("refresh status=%d called=%v body=%s", rec.Code, m.rangeCalled, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, personalAPIRequest(http.MethodPost, "/api/sessions/session-1/record/regenerate", "", user, true))
	if rec.Code != http.StatusAccepted || !m.regenerateCalled {
		t.Fatalf("regenerate status=%d called=%v body=%s", rec.Code, m.regenerateCalled, rec.Body.String())
	}
}

func TestSessionRecordManualRegenerationReportsBudgetWaitAndKeepsRateLimit(t *testing.T) {
	user := &auth.DashboardUser{ID: 41, Email: "record-owner@example.test", Role: "user", CctraceUserID: "record-owner"}
	runtime := &airuntime.FakeRuntime{InfoValue: airuntime.Info{Key: "claude-api", Model: "synthetic-model", AuthMode: airuntime.AuthModeAPIKey}, StatusValue: airuntime.Status{Configured: true, Available: true}}
	ai := aireport.NewService(aireport.NewMemStore(), []airuntime.Runtime{runtime}, aireport.Config{EnvRuntime: "claude-api"})
	retryAt := time.Now().UTC().Add(12 * time.Hour).Truncate(time.Second)
	m := &p5APIMockStore{mockStore: &mockStore{}, settings: store.SessionAIRecordSettings{Enabled: true, Runtime: "claude-api"}, granted: true}
	srv := newTestServer(m, nil).WithAIReports(ai).WithSessionRecordsEnabled(true)
	srv.sessionRecordWorkerStatus = func() sessionrecord.WorkerStatus {
		return sessionrecord.WorkerStatus{State: "paused", PausedReason: "daily_cap", PausedUntil: &retryAt}
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, personalAPIRequest(http.MethodPost, "/api/sessions/session-1/record/regenerate", "", user, true))
	if rec.Code != http.StatusAccepted || !m.regenerateCalled {
		t.Fatalf("budget-wait status=%d called=%v body=%s", rec.Code, m.regenerateCalled, rec.Body.String())
	}
	var body struct {
		State   string     `json:"state"`
		Reason  string     `json:"reason"`
		RetryAt *time.Time `json:"retry_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.State != "waiting_budget" || body.Reason != "waiting_budget" || body.RetryAt == nil || !body.RetryAt.Equal(retryAt) {
		t.Fatalf("budget-wait response=%+v body=%s", body, rec.Body.String())
	}
	if len(runtime.Requests()) != 0 {
		t.Fatalf("regeneration handler called provider runtime %d times", len(runtime.Requests()))
	}

	limited := &p5APIMockStore{mockStore: &mockStore{}, settings: store.SessionAIRecordSettings{Enabled: true, Runtime: "claude-api"}, granted: true, regenerateErr: store.ErrSessionRecordRateLimited}
	limitedServer := newTestServer(limited, nil).WithAIReports(ai).WithSessionRecordsEnabled(true)
	limitedRec := httptest.NewRecorder()
	limitedServer.Handler().ServeHTTP(limitedRec, personalAPIRequest(http.MethodPost, "/api/sessions/session-1/record/regenerate", "", user, true))
	if limitedRec.Code != http.StatusTooManyRequests || !strings.Contains(limitedRec.Body.String(), `"error":"rate_limited"`) {
		t.Fatalf("ordinary rate limit status=%d body=%s", limitedRec.Code, limitedRec.Body.String())
	}
}

func TestAnalysisHidesSessionRecordWhenFeatureFlagIsOff(t *testing.T) {
	m := &mockStore{sessionAnalysisReadFn: func(string, store.SessionAnalysisAccess) (map[string]interface{}, error) {
		return map[string]interface{}{
			"session_id": "mine", "record": map[string]interface{}{"state": "ready"}, "judgement": map[string]interface{}{"bucket": "done"},
		}, nil
	}}
	srv := newTestServer(m, nil).WithSessionAnalysisEnabled(true)
	user := &auth.DashboardUser{ID: 1, Email: "owner@example.test", Role: "user", CctraceUserID: "owner"}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, personalAPIRequest(http.MethodGet, "/api/sessions/mine/analysis", "", user, false))
	if rec.Code != http.StatusOK {
		t.Fatalf("analysis status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if _, ok := response["record"]; ok {
		t.Fatalf("feature-off analysis leaked record: %v", response)
	}
	if _, ok := response["judgement"]; ok {
		t.Fatalf("feature-off analysis leaked judgement: %v", response)
	}
}

func TestSessionRecordBucketCountsUseTheFullFilteredPopulation(t *testing.T) {
	user := &auth.DashboardUser{ID: 7, Email: "owner@example.test", Role: "user", CctraceUserID: "record-owner"}
	base := &mockStore{sessionOverviewAnalysisCountFn: func(_ context.Context, f store.SessionOverviewFilter, _ bool) (int, map[string]int, int, error) {
		if !f.IncludeSessionRecords || !f.AnalysisOwner {
			t.Errorf("P5 count scope=%+v", f)
		}
		return 3, nil, 0, nil
	}}
	m := &judgementBucketCountStore{Store: base, read: func(_ context.Context, f store.SessionOverviewFilter) (map[string]int, error) {
		if !f.IncludeSessionRecords || !f.AnalysisOwner {
			t.Errorf("P5 judgement count scope=%+v", f)
		}
		return map[string]int{"done": 0, "leftover": 1, "ended_midway": 1, "unknown": 1}, nil
	}}
	srv := newTestServer(m, nil).WithSessionAnalysisEnabled(true).WithSessionRecordsEnabled(true)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, personalAPIRequest(http.MethodGet, "/api/session-overview/count?include=analysis", "", user, false))
	if rec.Code != http.StatusOK {
		t.Fatalf("count status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	counts, ok := response["judgement_bucket_counts"].(map[string]interface{})
	if !ok || counts["unknown"] != float64(1) || counts["leftover"] != float64(1) || counts["ended_midway"] != float64(1) || counts["done"] != float64(0) {
		t.Fatalf("P5 judgement counts=%v", response["judgement_bucket_counts"])
	}
}
