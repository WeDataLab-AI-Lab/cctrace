package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestServerPolicyRouteBypassesAPIAuthentication(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, path string
		want       int
	}{{"disabled", "", 204}, {"missing configured file", filepath.Join(t.TempDir(), "missing.json"), 503}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mux := http.NewServeMux()
			mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
			registerServerPolicyRoute(mux, tc.path)
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest("GET", "/api/client-config-policy?installation_id=test", nil))
			if recorder.Code != tc.want {
				t.Fatalf("policy status = %d, want %d", recorder.Code, tc.want)
			}
			recorder = httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest("GET", "/api/other", nil))
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("API auth status = %d", recorder.Code)
			}
		})
	}
}
