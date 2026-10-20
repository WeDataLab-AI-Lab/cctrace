package syncer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cctrace/internal/store"
)

func TestSendQuotaOwnerSamplesUsesPersonalRouteAndIdentityFreeRows(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/api/me/quota-usage/samples" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer cct_owner-token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		var body struct {
			Samples []map[string]any `json:"samples"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Samples) != 1 {
			t.Fatalf("decode owner samples: %v %#v", err, body)
		}
		for _, forbidden := range []string{"login_email", "profile_email", "source_session_id", "access_token", "auth_token"} {
			if _, ok := body.Samples[0][forbidden]; ok {
				t.Fatalf("owner sample contains %q: %#v", forbidden, body.Samples[0])
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	sample := &store.QuotaOwnerSample{
		BillingProvider: "anthropic", AccountID: "acct-1", WindowKey: "session",
		SampledAt: time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC), UsedPct: 42,
		Attribution: store.AttributionObserved,
	}
	if err := NewClient(srv.URL, "cct_owner-token", "").SendQuotaOwnerSamples(context.Background(), []*store.QuotaOwnerSample{sample}); err != nil {
		t.Fatalf("SendQuotaOwnerSamples: %v", err)
	}
	if !called {
		t.Fatal("personal quota upload route was not called")
	}
}

func TestSendQuotaOwnerSamplesReturnsApprovalOrTokenFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()
	err := NewClient(srv.URL, "cct_pending", "").SendQuotaOwnerSamples(context.Background(), []*store.QuotaOwnerSample{{
		BillingProvider: "openai", AccountID: "acct-2", WindowKey: "300", UsedPct: 1, Attribution: store.AttributionObserved,
	}})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("SendQuotaOwnerSamples error = %v, want HTTP 403", err)
	}
}
