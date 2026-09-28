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

// Two surfaces answer "what did this cost": the internal /api/cost/* the dashboard
// and `cctrace report` read, and /api/open/v1/usage that external scripts and
// `cctrace usage` read. Keeping both is a deliberate choice -- the internal one is
// first-party and the open one is a versioned contract.
//
// What must not happen is the two drifting into different answers. Today they cannot,
// because both read the same store method rather than each carrying its own SQL.
// This pins that: give the store one set of rows and both endpoints must total the
// same money.
//
// If someone gives one surface its own query, the numbers stop matching here rather
// than on somebody's invoice.
func TestCostSurfacesAgreeOnTotals(t *testing.T) {
	rows := []*store.CostSummary{
		{ProfileEmail: "someone", UserTeam: "platform", Model: "sonnet", TotalCost: 1.25, TotalInput: 100, TotalOutput: 10, RequestCount: 10},
		{ProfileEmail: "someone", UserTeam: "platform", Model: "opus", TotalCost: 2.50, TotalInput: 200, TotalOutput: 20, RequestCount: 20},
	}
	calls := 0
	m := &mockStore{
		getDashboardUserByOpenAPITokenFn: func(ctx context.Context, token string) (*store.DashboardUser, error) {
			return &store.DashboardUser{ID: 1, Email: "someone", Role: "admin", IsActive: true}, nil
		},
		costByUserFn: func(ctx context.Context, s, u time.Time, profileEmail, loginEmail, userID string) ([]*store.CostSummary, error) {
			calls++
			return rows, nil
		},
	}
	srv := newTestServer(m, nil)
	window := "since=2026-08-01T00:00:00Z&until=2026-08-19T00:00:00Z"

	// Open API: grouped, one row per user.
	openReq := httptest.NewRequest(http.MethodGet, "/api/open/v1/usage?group_by=user&"+window, nil)
	openReq.Header.Set("Authorization", "Bearer web-token")
	openRec := httptest.NewRecorder()
	srv.mux.ServeHTTP(openRec, openReq)
	if openRec.Code != http.StatusOK {
		t.Fatalf("open api status = %d: %s", openRec.Code, openRec.Body.String())
	}
	var openBody struct {
		Items []struct {
			CostUSD float64 `json:"cost_usd"`
		} `json:"items"`
	}
	if err := json.Unmarshal(openRec.Body.Bytes(), &openBody); err != nil {
		t.Fatal(err)
	}
	var openTotal float64
	for _, i := range openBody.Items {
		openTotal += i.CostUSD
	}

	// Internal: one row per user AND model, which `cctrace report` prints unfolded.
	intRec := httptest.NewRecorder()
	intReq := httptest.NewRequest(http.MethodGet, "/api/cost/by-user?"+window, nil)
	srv.mux.ServeHTTP(intRec, intReq)
	if intRec.Code != http.StatusOK {
		t.Fatalf("internal status = %d: %s", intRec.Code, intRec.Body.String())
	}
	var intBody []struct {
		TotalCost float64 `json:"total_cost"`
	}
	if err := json.Unmarshal(intRec.Body.Bytes(), &intBody); err != nil {
		t.Fatal(err)
	}
	var intTotal float64
	for _, r := range intBody {
		intTotal += r.TotalCost
	}

	if openTotal < intTotal-1e-9 || openTotal > intTotal+1e-9 {
		t.Fatalf("the two cost surfaces disagree: open api %.10f, internal %.10f -- "+
			"they no longer share a source", openTotal, intTotal)
	}
	if calls != 2 {
		t.Fatalf("CostByUser called %d times, want 2 -- one surface stopped using it", calls)
	}
}
