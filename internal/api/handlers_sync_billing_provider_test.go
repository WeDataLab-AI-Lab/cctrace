package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cctrace/internal/store"
)

// The billing_provider default is looked up per agent kind, but a value the
// client already sent on the record always wins over the lookup. gjc is the
// case that matters most here: its .message.provider mixes "anthropic",
// "openai-codex" and "amazon-bedrock" within a single session, so unlike
// codex it cannot be pinned to one default - the per-record value is the
// only correct source of truth when present.
func TestSync_BillingProviderDefaultByAgent(t *testing.T) {
	tests := []struct {
		name         string
		agent        string
		wantProvider string
	}{
		{name: "gjc defaults to anthropic", agent: "gjc", wantProvider: "anthropic"},
		{name: "omo defaults to anthropic", agent: "omo", wantProvider: "anthropic"},
		{name: "codex still defaults to openai", agent: "codex", wantProvider: "openai"},
		{name: "claude still defaults to anthropic", agent: "claude", wantProvider: "anthropic"},
		{name: "unknown agent defaults to anthropic and is not rejected", agent: "future-tool", wantProvider: "anthropic"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var inserted []*store.SessionRecord
			m := &mockStore{
				insertSessionRecordsFn: func(ctx context.Context, records []*store.SessionRecord) error {
					inserted = records
					return nil
				},
			}
			srv := newTestServer(m, nil)
			ts := httptest.NewServer(srv.Handler())
			defer ts.Close()

			body := `{"agent":"` + tt.agent + `","project_hash":"proj","records":[{"record_type":"assistant"}]}`
			resp, err := http.Post(ts.URL+"/api/sync", "application/json", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("agent %q: expected 200, got %d", tt.agent, resp.StatusCode)
			}
			if len(inserted) != 1 {
				t.Fatalf("agent %q: inserted %d records, want 1", tt.agent, len(inserted))
			}
			if inserted[0].Agent != tt.agent {
				t.Fatalf("agent %q: record agent = %q, want %q", tt.agent, inserted[0].Agent, tt.agent)
			}
			if inserted[0].BillingProvider != tt.wantProvider {
				t.Fatalf("agent %q: billing_provider = %q, want %q", tt.agent, inserted[0].BillingProvider, tt.wantProvider)
			}
		})
	}
}

// gjc records carry billing_provider per record because a single gjc session
// mixes providers; the envelope-level agent default must never overwrite a
// value the client already set on the record.
func TestSync_BillingProviderPerRecordOverrideWins(t *testing.T) {
	var inserted []*store.SessionRecord
	m := &mockStore{
		insertSessionRecordsFn: func(ctx context.Context, records []*store.SessionRecord) error {
			inserted = records
			return nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"agent":"gjc","project_hash":"proj","records":[` +
		`{"record_type":"assistant","billing_provider":"openai-codex"},` +
		`{"record_type":"assistant"}]}`
	resp, err := http.Post(ts.URL+"/api/sync", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(inserted) != 2 {
		t.Fatalf("inserted %d records, want 2", len(inserted))
	}
	if inserted[0].BillingProvider != "openai-codex" {
		t.Fatalf("per-record billing_provider was overwritten: %+v", inserted[0])
	}
	if inserted[1].BillingProvider != "anthropic" {
		t.Fatalf("record without billing_provider should get gjc's default: %+v", inserted[1])
	}
}

// omo is mixed the same way gjc is (claude-sdk-oauth vs. openai-codex within
// a single session), so an omo record that already carries its own
// billing_provider must keep it rather than being overwritten by the map.
func TestSync_OmoBillingProviderPerRecordOverrideWins(t *testing.T) {
	var inserted []*store.SessionRecord
	m := &mockStore{
		insertSessionRecordsFn: func(ctx context.Context, records []*store.SessionRecord) error {
			inserted = records
			return nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"agent":"omo","project_hash":"proj","records":[` +
		`{"record_type":"assistant","billing_provider":"openai"},` +
		`{"record_type":"assistant"}]}`
	resp, err := http.Post(ts.URL+"/api/sync", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(inserted) != 2 {
		t.Fatalf("inserted %d records, want 2", len(inserted))
	}
	if inserted[0].BillingProvider != "openai" {
		t.Fatalf("per-record billing_provider was overwritten: %+v", inserted[0])
	}
	if inserted[1].BillingProvider != "anthropic" {
		t.Fatalf("record without billing_provider should get omo's default: %+v", inserted[1])
	}
}

// The envelope-level agent still fills in per-record agent when the record
// carries none, and the billing_provider default follows that filled-in
// agent. This guards against regressing existing envelope fallback behavior.
func TestSync_EnvelopeAgentFillsRecordAgentAndBillingProvider(t *testing.T) {
	var inserted []*store.SessionRecord
	m := &mockStore{
		insertSessionRecordsFn: func(ctx context.Context, records []*store.SessionRecord) error {
			inserted = records
			return nil
		},
	}
	srv := newTestServer(m, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"agent":"omo","project_hash":"proj","records":[{"record_type":"assistant"}]}`
	resp, err := http.Post(ts.URL+"/api/sync", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(inserted) != 1 {
		t.Fatalf("inserted %d records, want 1", len(inserted))
	}
	if inserted[0].Agent != "omo" {
		t.Fatalf("record agent should be filled from envelope: %+v", inserted[0])
	}
	if inserted[0].BillingProvider != "anthropic" {
		t.Fatalf("billing_provider should follow filled-in agent: %+v", inserted[0])
	}
}
