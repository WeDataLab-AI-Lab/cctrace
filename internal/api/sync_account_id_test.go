package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cctrace/internal/store"
)

// account_id is per-record, not per-envelope: a record collected before the
// Codex account was first observed stays empty, so nothing may fill it from a
// sibling record or from the envelope.
func TestSync_AcceptsPerRecordAccountID(t *testing.T) {
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

	body := `{"agent":"codex","project_hash":"proj","records":[` +
		`{"record_type":"assistant","account_id":"acct-a"},` +
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
	if inserted[0].AccountID != "acct-a" {
		t.Fatalf("account_id was not accepted: %+v", inserted[0])
	}
	if inserted[1].AccountID != "" {
		t.Fatalf("account_id should stay empty: %+v", inserted[1])
	}
}
