package localapi

import (
	"encoding/base64"
	"net/http/httptest"
	"testing"
)

func TestHealthRequiresBearerAndExactOrigin(t *testing.T) {
	token := base64.RawURLEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
	s, err := New(Options{Address: "127.0.0.1:12345", Token: token, AllowedOrigins: []string{"http://127.0.0.1:60895"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		token, origin string
		status        int
	}{
		{"", "", 401}, {"Bearer abc", "", 401}, {"Bearer " + token, "http://evil.test", 403}, {"Bearer " + token, "http://127.0.0.1:60895", 200},
	} {
		req := httptest.NewRequest("GET", "http://127.0.0.1:12345/v1/health", nil)
		if tc.token != "" {
			req.Header.Set("Authorization", tc.token)
		}
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Errorf("token=%q origin=%q: got %d want %d", tc.token, tc.origin, w.Code, tc.status)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Error("missing no-store")
		}
	}
}
