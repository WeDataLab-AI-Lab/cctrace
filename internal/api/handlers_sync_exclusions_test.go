package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cctrace/internal/ingestblock"
)

func postExclusions(t *testing.T, srv *Server, body string) *http.Response {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	resp, err := http.Post(ts.URL+"/api/sync/exclusions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// #715 layer 3: a client asks whether the accounts it is about to send are
// excluded, and hears back only about those. The rest of the exclusion list --
// other people's accounts -- never leaves the server.
func TestSyncExclusions_answersOnlyForTheAskedAccounts(t *testing.T) {
	invoked := false
	authMW := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			invoked = true
			next.ServeHTTP(w, r)
		})
	}
	bl := ingestblock.New()
	bl.AddExcludedAccount("openai", "acct-personal")
	bl.AddExcludedAccount("openai", "acct-someone-else")
	srv := newTestServer(&mockStore{}, nil, authMW).WithIngestBlocklist(bl)

	resp := postExclusions(t, srv, `{"accounts":[
		{"billing_provider":"openai","account_id":"acct-personal"},
		{"billing_provider":"openai","account_id":"acct-team"},
		{"billing_provider":"anthropic","account_id":"acct-personal"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !invoked {
		t.Error("the exclusion query bypassed the upload-token auth")
	}
	var got struct {
		Accounts []struct {
			BillingProvider string `json:"billing_provider"`
			AccountID       string `json:"account_id"`
		} `json:"accounts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Accounts) != 1 || got.Accounts[0].BillingProvider != "openai" || got.Accounts[0].AccountID != "acct-personal" {
		t.Errorf("accounts = %+v, want only openai/acct-personal", got.Accounts)
	}
}

// A query is about one client's own accounts, which is a handful. A request
// naming hundreds is not that, and answering it would turn the route into a
// bulk lookup over guessed ids. Refused as too large, not as malformed: a
// client can split a batch, and it cannot fix a request that is simply wrong.
func TestSyncExclusions_refusesOversizedQueries(t *testing.T) {
	srv := newTestServer(&mockStore{}, nil).WithIngestBlocklist(ingestblock.New())
	accounts := make([]string, 0, maxExclusionQueryAccounts+1)
	for i := 0; i <= maxExclusionQueryAccounts; i++ {
		accounts = append(accounts, fmt.Sprintf(`{"billing_provider":"openai","account_id":"acct-%d"}`, i))
	}
	resp := postExclusions(t, srv, `{"accounts":[`+strings.Join(accounts, ",")+`]}`)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
}
