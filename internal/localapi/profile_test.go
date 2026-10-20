package localapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalProfileCatalogAndSelectionUseProtectedCAS(t *testing.T) {
	token := base64.RawURLEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
	selectionPath := filepath.Join(t.TempDir(), "next-profile.json")
	s, err := New(Options{
		Address: "127.0.0.1:12345", Token: token, AllowedOrigins: []string{"http://127.0.0.1:60895"},
		SelectionPath: selectionPath,
		Profiles:      []ProfileIdentity{{Agent: "codex", ProfileName: "work", BillingProvider: "openai", AccountID: "acct-local"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRequest("GET", "http://127.0.0.1:12345/v1/profiles", nil)
	get.Header.Set("Authorization", "Bearer "+token)
	get.Header.Set("Origin", "http://127.0.0.1:60895")
	getRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(getRec, get)
	if getRec.Code != 200 {
		t.Fatalf("catalog status=%d body=%s", getRec.Code, getRec.Body.String())
	}
	if strings.Contains(getRec.Body.String(), "/Users/") || strings.Contains(getRec.Body.String(), "auth.json") || strings.Contains(getRec.Body.String(), token) {
		t.Fatalf("catalog leaked local path or credential: %s", getRec.Body.String())
	}
	var catalog ProfileCatalogResponse
	if err := json.Unmarshal(getRec.Body.Bytes(), &catalog); err != nil || len(catalog.Profiles) != 1 || catalog.Profiles[0].AccountID != "acct-local" {
		t.Fatalf("catalog=%+v err=%v", catalog, err)
	}
	preflight := httptest.NewRequest("OPTIONS", "http://127.0.0.1:12345/v1/profiles/select", nil)
	preflight.Header.Set("Origin", "http://127.0.0.1:60895")
	preflight.Header.Set("Access-Control-Request-Method", "POST")
	preflight.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
	preflightRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(preflightRec, preflight)
	if preflightRec.Code != 204 || preflightRec.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Fatalf("profile preflight status=%d headers=%v", preflightRec.Code, preflightRec.Header())
	}

	body := `{"agent":"codex","profile_name":"work","billing_provider":"openai","account_id":"acct-local","expected_revision":0}`
	post := func(origin, requestBody string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "http://127.0.0.1:12345/v1/profiles/select", strings.NewReader(requestBody))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	if got := post("", body).Code; got != 403 {
		t.Fatalf("missing Origin status=%d, want 403", got)
	}
	if got := post("http://evil.test", body).Code; got != 403 {
		t.Fatalf("wrong Origin status=%d, want 403", got)
	}
	if got := post("http://127.0.0.1:60895", strings.Replace(body, "acct-local", "acct-other", 1)).Code; got != 403 {
		t.Fatalf("account mismatch status=%d, want 403", got)
	}
	selected := post("http://127.0.0.1:60895", body)
	if selected.Code != 200 || !strings.Contains(selected.Body.String(), `"revision":1`) {
		t.Fatalf("selection status=%d body=%s", selected.Code, selected.Body.String())
	}
	stale := post("http://127.0.0.1:60895", body)
	if stale.Code != 409 {
		t.Fatalf("stale selection status=%d body=%s, want 409", stale.Code, stale.Body.String())
	}
	data, err := os.ReadFile(selectionPath)
	if err != nil || !strings.Contains(string(data), `"codex": "work"`) {
		t.Fatalf("selection file=%s err=%v", data, err)
	}
}
