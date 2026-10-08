package syncer

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A server behind a private CA is reachable only through the transport the
// caller installs. The client's own ceiling must survive the swap: it is the
// last guard against an upload that escapes its per-request deadline.
func TestSetTransportReachesServerBehindPrivateCA(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"v1.2.3"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "test")
	if _, err := c.CheckVersion(t.Context()); err == nil {
		t.Fatal("CheckVersion trusted the test server without its CA; this test is not exercising TLS trust")
	}

	c.SetTransport(srv.Client().Transport)
	got, err := c.CheckVersion(t.Context())
	if err != nil {
		t.Fatalf("CheckVersion with the server's CA: %v", err)
	}
	if got != "v1.2.3" {
		t.Fatalf("CheckVersion = %q, want v1.2.3", got)
	}
	if c.http.Timeout != maxTransferDeadline {
		t.Fatalf("Timeout = %v after SetTransport, want %v", c.http.Timeout, maxTransferDeadline)
	}
}
