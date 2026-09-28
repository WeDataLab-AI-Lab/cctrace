package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cctrace/internal/store"
)

// cctrace exists to answer what the agents cost. The Open API shipped without that
// number: UsageAggregates counts tokens and never touches cost_usd, so /usage could
// tell you 4.2M tokens and nothing about the money.
//
// The internal API already answers it -- CostByUser/CostByTeam/CostByModel in
// internal/store/postgres_cost.go, all returning cost alongside tokens. These tests
// pin the open surface onto those.

// newUsageTestServer wires a non-admin caller whose store returns two model rows for
// one user -- the shape CostByUser actually produces, since it groups by model as
// well as by user.
func newUsageTestServer(t *testing.T) *Server {
	t.Helper()
	since := mustTime(t, "2026-08-01T00:00:00Z")
	m := &mockStore{
		getDashboardUserByAPITokenFn: func(ctx context.Context, token string) (*store.DashboardUser, error) {
			return &store.DashboardUser{ID: 1, Email: "caller", Role: "user", IsActive: true}, nil
		},
		costByUserFn: func(ctx context.Context, s, u time.Time, profileEmail, loginEmail, userID string) ([]*store.CostSummary, error) {
			return []*store.CostSummary{
				{ProfileEmail: "caller", UserTeam: "platform", Model: "sonnet", TotalCost: 1, TotalInput: 100, TotalOutput: 10, RequestCount: 10},
				{ProfileEmail: "caller", UserTeam: "platform", Model: "opus", TotalCost: 2, TotalInput: 200, TotalOutput: 20, RequestCount: 20},
			}, nil
		},
		costByTeamFn: func(ctx context.Context, s, u time.Time, profileEmail, userID string) ([]*store.CostSummary, error) {
			return []*store.CostSummary{
				{UserTeam: "platform", Model: "sonnet", TotalCost: 3, TotalInput: 300, TotalOutput: 30, RequestCount: 30},
			}, nil
		},
		costByModelFn: func(ctx context.Context, s, u time.Time, profileEmail, loginEmail, userID string) ([]*store.ModelStat, error) {
			return []*store.ModelStat{
				{Model: "sonnet", TotalCost: 1, InputTokens: 100, OutputTokens: 10, RequestCount: 10},
				{Model: "opus", TotalCost: 2, InputTokens: 200, OutputTokens: 20, RequestCount: 20},
			}, nil
		},
		usageAggregatesFn: func(ctx context.Context, f store.SessionOverviewFilter) (*store.UsageAggregate, error) {
			return &store.UsageAggregate{SessionCount: 2, InputTokens: 300, OutputTokens: 30, WorkTimeSeconds: 60}, nil
		},
	}
	_ = since
	return newTestServer(m, nil)
}

func usageRequest(t *testing.T, srv *Server, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/open/v1/usage"+query, nil)
	req.Header.Set("Authorization", "Bearer open-api-token")
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	return rec
}

func TestOpenAPIUsageReportsCost(t *testing.T) {
	srv := newUsageTestServer(t)

	rec := usageRequest(t, srv, "?since=2026-08-01T00:00:00Z&until=2026-08-31T00:00:00Z")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := got["cost_usd"]; !ok {
		t.Fatal("usage response has no cost_usd -- the one number this product exists for")
	}
}

func TestOpenAPIUsageUngroupedPreservesCacheOnlyNormalizedTokens(t *testing.T) {
	const cacheCost = 100.0 * 0.5 / 1_000_000.0
	srv := newTestServer(&mockStore{
		getDashboardUserByOpenAPITokenFn: func(context.Context, string) (*store.DashboardUser, error) {
			return &store.DashboardUser{ID: 1, Email: "admin", Role: "admin", IsActive: true}, nil
		},
		usageAggregatesFn: func(context.Context, store.SessionOverviewFilter) (*store.UsageAggregate, error) {
			return &store.UsageAggregate{
				InputTokens:  0,
				OutputTokens: 0,
				ByModel:      []*store.ModelUsageAggregate{{Model: "gpt-5.5", InputTokens: 0, OutputTokens: 0}},
			}, nil
		},
		costByModelFn: func(context.Context, time.Time, time.Time, string, string, string) ([]*store.ModelStat, error) {
			return []*store.ModelStat{{Model: "gpt-5.5", TotalCost: cacheCost, InputTokens: 0, OutputTokens: 0, RequestCount: 1}}, nil
		},
	}, nil)

	query := "?since=2026-08-15T00:00:00Z&until=2026-08-16T00:00:00Z"
	rec := usageRequest(t, srv, query)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		CostUSD      float64 `json:"cost_usd"`
		InputTokens  int64   `json:"input_tokens"`
		OutputTokens int64   `json:"output_tokens"`
		TotalTokens  int64   `json:"total_tokens"`
		ByModel      []struct {
			Model        string `json:"model"`
			InputTokens  int64  `json:"input_tokens"`
			OutputTokens int64  `json:"output_tokens"`
			TotalTokens  int64  `json:"total_tokens"`
		} `json:"by_model"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.InputTokens != 0 || got.OutputTokens != 0 || got.TotalTokens != 0 {
		t.Fatalf("ungrouped tokens = %d/%d/%d, want normalized 0/0/0", got.InputTokens, got.OutputTokens, got.TotalTokens)
	}
	if got.CostUSD < cacheCost-1e-12 || got.CostUSD > cacheCost+1e-12 {
		t.Fatalf("cost = %.10f, want cache cost %.10f", got.CostUSD, cacheCost)
	}
	if len(got.ByModel) != 1 || got.ByModel[0].Model != "gpt-5.5" || got.ByModel[0].InputTokens != 0 || got.ByModel[0].OutputTokens != 0 || got.ByModel[0].TotalTokens != 0 {
		t.Fatalf("by_model = %+v, want cache-only normalized gpt-5.5 row", got.ByModel)
	}

	groupedRec := usageRequest(t, srv, "?group_by=model&"+query[1:])
	if groupedRec.Code != http.StatusOK {
		t.Fatalf("grouped status = %d, want 200: %s", groupedRec.Code, groupedRec.Body.String())
	}
	var grouped struct {
		Items []struct {
			Key          string  `json:"key"`
			CostUSD      float64 `json:"cost_usd"`
			InputTokens  int64   `json:"input_tokens"`
			OutputTokens int64   `json:"output_tokens"`
			TotalTokens  int64   `json:"total_tokens"`
		} `json:"items"`
	}
	if err := json.NewDecoder(groupedRec.Body).Decode(&grouped); err != nil {
		t.Fatalf("decode grouped: %v", err)
	}
	if len(grouped.Items) != 1 || grouped.Items[0].Key != "gpt-5.5" || grouped.Items[0].InputTokens != got.ByModel[0].InputTokens || grouped.Items[0].OutputTokens != got.ByModel[0].OutputTokens || grouped.Items[0].TotalTokens != got.ByModel[0].TotalTokens || grouped.Items[0].CostUSD < cacheCost-1e-12 || grouped.Items[0].CostUSD > cacheCost+1e-12 {
		t.Fatalf("grouped items = %+v, want same normalized tokens and cache cost", grouped.Items)
	}
}

// Grouping is what makes `cctrace report` expressible through this API. The store
// returns rows split by model as well as by the requested axis, so the handler has
// to fold them: a user who used three models is one row here, not three.
func TestOpenAPIUsageGroupsByUserAndFoldsModels(t *testing.T) {
	srv := newUsageTestServer(t)

	rec := usageRequest(t, srv, "?group_by=user&since=2026-08-01T00:00:00Z&until=2026-08-31T00:00:00Z")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Items []struct {
			Key      string  `json:"key"`
			CostUSD  float64 `json:"cost_usd"`
			Requests int64   `json:"request_count"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("items = %d, want 1 -- the two model rows for one user must fold into one", len(got.Items))
	}
	if got.Items[0].CostUSD < 2.9999 || got.Items[0].CostUSD > 3.0001 {
		t.Fatalf("cost = %v, want 3 (1 + 2 summed across the user's models)", got.Items[0].CostUSD)
	}
	if got.Items[0].Requests != 30 {
		t.Fatalf("request_count = %d, want 30", got.Items[0].Requests)
	}
	if got.Total != len(got.Items) {
		t.Fatalf("total = %d, items = %d", got.Total, len(got.Items))
	}
}

// An axis nobody defined must be refused rather than silently ignored -- a caller
// that mistypes group_by should not get whole-fleet totals believing they are
// grouped.
func TestOpenAPIUsageRejectsUnknownGroupBy(t *testing.T) {
	srv := newUsageTestServer(t)

	rec := usageRequest(t, srv, "?group_by=nonsense&since=2026-08-01T00:00:00Z&until=2026-08-31T00:00:00Z")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unknown group_by", rec.Code)
	}
}

// Grouping must not become a way around the read scope: a non-admin asking to group
// by user still sees only itself.
func TestOpenAPIUsageGroupingRespectsScope(t *testing.T) {
	srv := newUsageTestServer(t)

	rec := usageRequest(t, srv, "?group_by=user&profile_email=someone-else&since=2026-08-01T00:00:00Z&until=2026-08-31T00:00:00Z")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Items []struct {
			Key string `json:"key"`
		} `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, item := range got.Items {
		if item.Key == "someone-else" {
			t.Fatal("a non-admin reached another account's usage by passing profile_email")
		}
	}
}

func mustTime(t *testing.T, v string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, v)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}
