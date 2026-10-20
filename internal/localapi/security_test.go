package localapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestHostOriginPreflightAndMethod(t *testing.T) {
	s, token, _ := fixtureServer(t)
	checks := []struct {
		method, path, host, origin, requestMethod, headers string
		want                                               int
	}{
		{"GET", "/v1/health", "evil.test:12345", "", "", "", 403},
		{"GET", "/v1/health", "127.0.0.1:12345", "null", "", "", 403},
		{"POST", "/v1/health", "127.0.0.1:12345", "", "", "", 405},
		{"OPTIONS", "/v1/health", "127.0.0.1:12345", "http://127.0.0.1:60895", "GET", "authorization,content-type", 204},
		{"OPTIONS", "/v1/health", "127.0.0.1:12345", "http://127.0.0.1:60895", "POST", "authorization", 403},
		{"OPTIONS", "/v1/health", "127.0.0.1:12345", "http://127.0.0.1:60895", "GET", "x-evil", 403},
	}
	for _, tc := range checks {
		req := httptest.NewRequest(tc.method, "http://127.0.0.1:12345"+tc.path, nil)
		req.Host = tc.host
		req.Header.Set("Origin", tc.origin)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Access-Control-Request-Method", tc.requestMethod)
		req.Header.Set("Access-Control-Request-Headers", tc.headers)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Errorf("%s %s: got %d want %d", tc.method, tc.origin, w.Code, tc.want)
		}
		if tc.origin == "null" && w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Error("unauthorized origin received CORS")
		}
	}
}
func TestOutsideSymlinkAndHomeIsolation(t *testing.T) {
	s, token, _ := fixtureServer(t)
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(outside, []byte(`{"type":"user"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	root := s.roots[0].path
	link := filepath.Join(root, "projects", "-sample", "escaped.jsonl")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	code, body := request(t, s, token, "/v1/sessions")
	if code != 200 {
		t.Fatalf("list %d %s", code, body)
	}
	var listed ListResponse
	_ = json.Unmarshal(body, &listed)
	if len(listed.Sessions) != 1 {
		t.Fatalf("symlink escaped root: %d", len(listed.Sessions))
	}
	other := t.TempDir()
	dir := filepath.Join(other, "projects", "-sample")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte(`{"type":"user"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := New(Options{Address: "127.0.0.1:12345", Token: token, ClaudeDirs: []string{other}, AllowedOrigins: []string{"http://127.0.0.1:60895"}})
	if err != nil {
		t.Fatal(err)
	}
	code, body = request(t, second, token, "/v1/sessions")
	if code != 200 {
		t.Fatalf("second %d %s", code, body)
	}
	var out ListResponse
	_ = json.Unmarshal(body, &out)
	if out.Sessions[0].LocalID == listed.Sessions[0].LocalID {
		t.Fatal("same native id collides across roots")
	}
	code, _ = request(t, s, token, "/v1/sessions/../../outside/records")
	if code != 404 {
		t.Fatalf("traversal status %d", code)
	}
}
func TestOriginsAndShortTokenRejected(t *testing.T) {
	valid := base64.RawURLEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
	for _, origin := range []string{"https://example.com", "http://example.com"} {
		if _, err := New(Options{Address: "127.0.0.1:12345", Token: valid, AllowedOrigins: []string{origin}}); err != nil {
			t.Errorf("rejected default-port origin %q: %v", origin, err)
		}
	}
	for _, origin := range []string{"*", "null", "http://localhost/path", "http://user@localhost", "http://localhost?x=1", "http://*.example.com"} {
		if _, err := New(Options{Address: "127.0.0.1:12345", Token: valid, AllowedOrigins: []string{origin}}); err == nil {
			t.Errorf("accepted origin %q", origin)
		}
	}
	if _, err := New(Options{Address: "127.0.0.1:12345", Token: "short", AllowedOrigins: []string{"http://localhost"}}); err == nil {
		t.Error("accepted short token")
	}
}
