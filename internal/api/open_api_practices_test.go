package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"cctrace/internal/store"
)

type practicesMockStore struct {
	*mockStore
	fn func(ctx context.Context, since, until time.Time, selfUserID string) (*store.Practices, error)
}

func (m *practicesMockStore) Practices(ctx context.Context, since, until time.Time, selfUserID string) (*store.Practices, error) {
	return m.fn(ctx, since, until, selfUserID)
}

func practicesTokenLookup(_ context.Context, token string) (*store.DashboardUser, error) {
	switch token {
	case "admin-token":
		return &store.DashboardUser{ID: 1, Email: "admin@example.com", Role: "admin", IsActive: true, CctraceUserID: "admin-uid"}, nil
	case "read-token":
		return &store.DashboardUser{ID: 2, Email: "alice@example.com", Role: "user", IsActive: true, CctraceUserID: "alice"}, nil
	case "ingest-token":
		return nil, store.ErrTokenNotOpenAPI
	case "admin-read-token":
		return nil, store.ErrCLIReadTokenAdmin
	}
	return nil, context.Canceled
}

// Admins and regular users read the same shared aggregate; only self follows the
// caller, and the window is fixed at the last 30 days whatever the query says.
func TestOpenAPIPracticesSharesAggregateAndScopesSelf(t *testing.T) {
	var calls []string
	srv := newTestServer(&practicesMockStore{
		mockStore: &mockStore{getDashboardUserByOpenAPITokenFn: practicesTokenLookup},
		fn: func(_ context.Context, since, until time.Time, selfUserID string) (*store.Practices, error) {
			calls = append(calls, selfUserID)
			if d := time.Since(until); d < 0 || d > time.Minute {
				t.Fatalf("until = %v, want now", until)
			}
			if !since.Equal(until.AddDate(0, 0, -30)) {
				t.Fatalf("window = %v..%v, want 30 days ending now", since, until)
			}
			manual := 12.5
			return &store.Practices{
				WindowStart: since, WindowEnd: until,
				Rows:    []store.PracticeRow{{Kind: "plugin", Name: "alpha", UsersWith: 1}},
				Self:    store.PracticeRates{ManualApprovalsPer1000: &manual},
				Caveats: []string{"correlation only"},
			}, nil
		},
	}, nil)

	for _, token := range []string{"read-token", "admin-token"} {
		rec := openAPIRequest(t, srv, http.MethodGet,
			"/api/open/v1/practices?since=2020-01-01T00:00:00Z&until=2020-01-02T00:00:00Z&user_id=bob", "Bearer "+token)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d; body=%s", token, rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		assertJSONKeysExactly(t, body, "window_start", "window_end", "rows", "self", "caveats")
		row := body["rows"].([]any)[0].(map[string]any)
		assertJSONKeysExactly(t, row, "kind", "name", "users_with")
		assertJSONKeysExactly(t, body["self"].(map[string]any), "manual_approvals_per_1000", "human_rejections_per_1000")
	}
	if len(calls) != 2 || calls[0] != "alice" || calls[1] != "admin-uid" {
		t.Fatalf("self user ids = %v, want the caller's own (alice, then admin-uid)", calls)
	}
}

func TestOpenAPIPracticesUsesOpenAPITokenRules(t *testing.T) {
	srv := newTestServer(&practicesMockStore{
		mockStore: &mockStore{getDashboardUserByOpenAPITokenFn: practicesTokenLookup},
		fn: func(context.Context, time.Time, time.Time, string) (*store.Practices, error) {
			return &store.Practices{Rows: []store.PracticeRow{}}, nil
		},
	}, nil)
	for _, tt := range []struct {
		header string
		status int
		code   string
	}{
		{header: "", status: http.StatusUnauthorized},
		{header: "Bearer unknown", status: http.StatusUnauthorized},
		{header: "Bearer ingest-token", status: http.StatusUnauthorized, code: "ingestion_token"},
		{header: "Bearer admin-read-token", status: http.StatusForbidden, code: "admin_read_token_forbidden"},
		{header: "Bearer read-token", status: http.StatusOK},
	} {
		rec := openAPIRequest(t, srv, http.MethodGet, "/api/open/v1/practices", tt.header)
		if rec.Code != tt.status {
			t.Fatalf("%q status = %d, want %d; body=%s", tt.header, rec.Code, tt.status, rec.Body.String())
		}
		if tt.code != "" {
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["code"] != tt.code {
				t.Fatalf("%q body = %s, want code %s", tt.header, rec.Body.String(), tt.code)
			}
		}
	}
}

// The route sits behind the same per-caller limiter as its neighbours.
func TestOpenAPIPracticesIsRateLimitedPerCaller(t *testing.T) {
	srv := newTestServer(&practicesMockStore{
		mockStore: &mockStore{getDashboardUserByOpenAPITokenFn: practicesTokenLookup},
		fn: func(context.Context, time.Time, time.Time, string) (*store.Practices, error) {
			return &store.Practices{Rows: []store.PracticeRow{}}, nil
		},
	}, nil)
	for i := 0; i < 2*openAPIUserBurst; i++ {
		if rec := openAPIRequest(t, srv, http.MethodGet, "/api/open/v1/practices", "Bearer read-token"); rec.Code == http.StatusTooManyRequests {
			return
		}
	}
	t.Fatalf("no 429 after %d requests from one caller", 2*openAPIUserBurst)
}
