package syncer

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"cctrace/internal/store"
)

func TestTransferDiagnosticsPersistAttempts(t *testing.T) {
	t.Parallel()
	var sizes []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sizes = append(sizes, len(body))
		var payload SyncPayload
		_ = json.Unmarshal(body, &payload)
		if len(payload.Records) > 1 {
			w.WriteHeader(413)
			return
		}
		_, _ = w.Write([]byte(`{"inserted":1}`))
	}))
	defer server.Close()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	path := filepath.Join(dir, "transfer-diagnostics.json")
	c := NewClient(server.URL, "secret", "1.2.3")
	if err := c.SetTransferDiagnostics(path); err != nil {
		t.Fatal(err)
	}
	_, err := c.Send(context.Background(), "codex", "private@example.com", "private-user", "private-project", "private-name", ProjectIdentity{}, []*store.SessionRecord{{}, {}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	t.Log(string(raw))
	d, err := ReadTransferDiagnostics(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Events) != 3 {
		t.Fatalf("events=%+v", d.Events)
	}
	for i, e := range d.Events {
		if e.RequestBytes != sizes[i] || e.Provider != "codex" || e.RecordCount < 1 || e.ClientVersion != "1.2.3" {
			t.Fatalf("event=%+v", e)
		}
	}
	if d.Events[0].Outcome != "body_too_large" || d.Events[1].SplitDepth != 1 {
		t.Fatalf("split=%+v", d.Events)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	c2 := NewClient(server.URL, "secret", "1.2.3")
	if err := c2.SetTransferDiagnostics(path); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 35; i++ {
		_, err = c2.Send(context.Background(), "codex", "", "", "", "", ProjectIdentity{}, []*store.SessionRecord{{}})
		if err != nil {
			t.Fatal(err)
		}
	}
	d, err = ReadTransferDiagnostics(path)
	if err != nil || len(d.Events) != 32 || d.LastFailure == nil || d.LastFailure.HTTPStatus != 413 {
		t.Fatalf("ring=%d err=%v", len(d.Events), err)
	}
}

func TestTransferDiagnosticsRejectUnsafeHistory(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{"schema_version":1,"events":[],"secret":"private"}`, `{"schema_version":1,"schema_version":1,"events":[]}`, `{"schema_version":1,"events":[{"provider":"private@example.com"}]}`} {
		dir, _ := filepath.EvalSymlinks(t.TempDir())
		path := filepath.Join(dir, "diagnostics.json")
		_ = os.WriteFile(path, []byte(body), 0600)
		if _, err := ReadTransferDiagnostics(path); err == nil {
			t.Fatalf("accepted unsafe %s", body)
		}
	}
}

func TestTransferDiagnosticsCorruptionAndRateLimit(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Retry-After", "7"); w.WriteHeader(429) }))
	defer server.Close()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	path := filepath.Join(dir, "transfer-diagnostics.json")
	_ = os.WriteFile(path, []byte(`{"secret":"do-not-export"}`), 0600)
	c := NewClient(server.URL, "secret", "dev")
	if err := c.SetTransferDiagnostics(path); err != nil {
		t.Fatal(err)
	}
	_, err := c.Send(context.Background(), "claude", "private@example.com", "private-account", "private-project", "private-name", ProjectIdentity{}, []*store.SessionRecord{{}})
	if err == nil {
		t.Fatal("expected rate limit")
	}
	d, err := ReadTransferDiagnostics(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Events) != 1 || d.Events[0].HTTPStatus != 429 || d.Events[0].RetryAfterMS != 7000 {
		t.Fatalf("%+v", d)
	}
	raw, _ := os.ReadFile(path)
	for _, secret := range []string{"secret", "private@", "private-account", "private-project", "private-name", "http://"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("leaked %s", secret)
		}
	}
	target := filepath.Join(dir, "target")
	_ = os.WriteFile(target, []byte("owned"), 0600)
	link := filepath.Join(dir, "link")
	_ = os.Symlink(target, link)
	if err := c.SetTransferDiagnostics(link); err != nil {
		t.Fatal(err)
	}
	_, _ = c.Send(context.Background(), "claude", "", "", "", "", ProjectIdentity{}, []*store.SessionRecord{{}})
	body, _ := os.ReadFile(target)
	if string(body) != "owned" {
		t.Fatal("symlink overwritten")
	}
}

func TestTransferDiagnosticsNetworkFailure(t *testing.T) {
	t.Parallel()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	path := filepath.Join(dir, "transfer-diagnostics.json")
	c := NewClient("http://invalid.invalid", "secret", "dev")
	c.SetTransport(diagnosticErrorTransport{})
	_ = c.SetTransferDiagnostics(path)
	_, err := c.Send(context.Background(), "private-provider", "private", "", "", "", ProjectIdentity{}, []*store.SessionRecord{{}})
	if err == nil {
		t.Fatal("expected failure")
	}
	d, err := ReadTransferDiagnostics(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Events) != 1 || d.Events[0].Failure != "timeout" || d.Events[0].Provider != "unknown" || d.Events[0].HTTPStatus != 0 {
		t.Fatalf("%+v", d)
	}
}

type diagnosticErrorTransport struct{}

func (diagnosticErrorTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, context.DeadlineExceeded
}

func TestTransferDiagnosticsTLSFailure(t *testing.T) {
	t.Parallel()
	for _, failure := range []error{x509.UnknownAuthorityError{}, x509.CertificateInvalidError{}, x509.HostnameError{}, tls.RecordHeaderError{}} {
		dir, _ := filepath.EvalSymlinks(t.TempDir())
		path := filepath.Join(dir, "transfer-diagnostics.json")
		c := NewClient("https://invalid.invalid", "secret", "dev")
		c.SetTransport(diagnosticTLSFailureTransport{failure})
		_ = c.SetTransferDiagnostics(path)
		_, err := c.Send(context.Background(), "claude", "", "", "", "", ProjectIdentity{}, []*store.SessionRecord{{}})
		if err == nil {
			t.Fatal("expected failure")
		}
		d, err := ReadTransferDiagnostics(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(d.Events) != 1 || d.Events[0].Failure != "tls" {
			t.Fatalf("type=%T event=%+v", failure, d.Events)
		}
	}
}

type diagnosticTLSFailureTransport struct{ failure error }

func (tr diagnosticTLSFailureTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, tr.failure
}
