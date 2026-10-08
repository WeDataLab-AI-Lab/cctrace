package openinsights

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Same contract as the sync client: the installed transport carries the
// private CA, and the per-request timeout stays what it was.
func TestSetTransportReachesServerBehindPrivateCA(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"project_hash":"h1"}]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "read-token")
	c.wait = func(context.Context, time.Duration) error { return nil }
	if _, err := c.Projects(t.Context()); err == nil {
		t.Fatal("Projects trusted the test server without its CA; this test is not exercising TLS trust")
	}

	c.SetTransport(srv.Client().Transport)
	got, err := c.Projects(t.Context())
	if err != nil {
		t.Fatalf("Projects with the server's CA: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Projects = %+v, want one item", got)
	}
	if c.http.Timeout != requestTimeout {
		t.Fatalf("Timeout = %v after SetTransport, want %v", c.http.Timeout, requestTimeout)
	}
}
