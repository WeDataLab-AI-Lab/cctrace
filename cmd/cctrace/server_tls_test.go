package main

import (
	"bufio"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cctrace/internal/profile"
)

// writeServerCAFile stores a TLS test server's certificate as a PEM file, the
// file a person would hand to server.ca_cert_file for a private CA.
func writeServerCAFile(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "root.crt")
	block := &pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Without a CA the client keeps Go's default transport untouched, so an install
// that never sets the key behaves exactly as before.
func TestServerTransportWithoutCAIsDefault(t *testing.T) {
	rt, err := serverTransport("")
	if err != nil || rt != nil {
		t.Fatalf("serverTransport(\"\") = %v, %v; want nil, nil", rt, err)
	}
}

// The CA is added to the system roots, not substituted for them, and the clone
// keeps what the default transport does: proxy from the environment (the socks
// route some installs depend on) and HTTP/2.
func TestServerTransportTrustsCAFile(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	if _, err := (&http.Client{}).Get(srv.URL); err == nil {
		t.Fatal("the default client trusted the test server; this test is not exercising TLS trust")
	}

	rt, err := serverTransport(writeServerCAFile(t, srv))
	if err != nil {
		t.Fatalf("serverTransport: %v", err)
	}
	resp, err := (&http.Client{Transport: rt}).Get(srv.URL)
	if err != nil {
		t.Fatalf("GET with the CA file: %v", err)
	}
	resp.Body.Close()

	tr, ok := rt.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", rt)
	}
	if tr.Proxy == nil || !tr.ForceAttemptHTTP2 {
		t.Fatalf("Proxy set = %v, ForceAttemptHTTP2 = %v; want both kept from the default transport", tr.Proxy != nil, tr.ForceAttemptHTTP2)
	}
	if tr == http.DefaultTransport {
		t.Fatal("serverTransport returned the shared default transport itself")
	}
}

// A CA file that cannot be used is an error, never a quiet fall back to the
// system roots: that fallback fails later as an unexplained TLS error, or, worse,
// succeeds against a server the person did not mean to trust.
func TestServerTransportRefusesUnusableCAFile(t *testing.T) {
	dir := t.TempDir()
	notPEM := filepath.Join(dir, "not.pem")
	if err := os.WriteFile(notPEM, []byte("not a certificate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(dir, "missing.crt"), notPEM} {
		rt, err := serverTransport(path)
		if err == nil {
			t.Fatalf("serverTransport(%q) = %v, nil; want an error", path, rt)
		}
		// The file was accepted once and has since moved or changed; the error
		// is the only place the person learns how to stop using it.
		if !strings.Contains(err.Error(), `cctrace config set server.ca_cert_file ""`) {
			t.Fatalf("serverTransport(%q) error %q does not say how to clear the setting", path, err)
		}
	}
}

// Every sync path builds its client here, so the CA is installed here.
func TestNewSyncClientTrustsProfileCA(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"v9.9.9"}`))
	}))
	defer srv.Close()

	p := profile.NewDefault()
	p.Server.CACertFile = writeServerCAFile(t, srv)
	client, err := newSyncClient(p, srv.URL, "test", "")
	if err != nil {
		t.Fatalf("newSyncClient: %v", err)
	}
	if got, err := client.CheckVersion(t.Context()); err != nil || got != "v9.9.9" {
		t.Fatalf("CheckVersion = %q, %v", got, err)
	}

	p.Server.CACertFile = filepath.Join(t.TempDir(), "gone.crt")
	if _, err := newSyncClient(p, srv.URL, "test", ""); err == nil {
		t.Fatal("newSyncClient accepted a CA file that does not exist")
	}
}

// The commands that read the server outside the sync client: login during init,
// read-token creation, the Open API reads, and the status probe.
func TestServerCommandsTrustProfileCA(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/cli/auth":
			_, _ = w.Write([]byte(`{"name":"Alice","email":"alice@example.test"}`))
		case "/api/cli/read-token":
			_, _ = w.Write([]byte(`{"api_token":"cct_read","name":"CLI read token"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	ca := writeServerCAFile(t, srv)

	client, err := serverClient(ca, 0)
	if err != nil {
		t.Fatalf("serverClient: %v", err)
	}
	if _, err := authenticateUser(client, srv.URL, "alice", "pw", ""); err != nil {
		t.Fatalf("authenticateUser: %v", err)
	}
	if _, err := fetchJSON(client, srv.URL+"/api/open/v1/projects", "tok"); err != nil {
		t.Fatalf("fetchJSON: %v", err)
	}

	p := profile.NewDefault()
	p.Server.SyncEndpoint = srv.URL
	p.Server.CACertFile = ca
	p.User.ID = "alice"
	if _, err := issueReadToken(p, "pw", "laptop"); err != nil {
		t.Fatalf("issueReadToken: %v", err)
	}

	if got := checkHTTPHealth(srv.URL, ca); !strings.HasPrefix(got, "[OK]") {
		t.Fatalf("checkHTTPHealth = %q, want [OK]", got)
	}
	if got := checkHTTPHealth(srv.URL, ""); strings.HasPrefix(got, "[OK]") {
		t.Fatalf("checkHTTPHealth without the CA = %q; the probe is not exercising TLS trust", got)
	}
}

// The update download reaches the same server, so it trusts the same CA, and an
// unusable CA fails the attempt before any request is sent.
func TestDownloadAndApplyUpdateRefusesUnusableCAFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone.crt")
	err := downloadAndApplyUpdate(t.Context(), missing, "https://cctrace.example.test", "v9.9.9")
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("downloadAndApplyUpdate = %v, want an error naming %s", err, missing)
	}
}

// The CA question is asked only when an endpoint is https. Plain http has no
// certificate to trust, and asking would only invite a wrong answer. A CA kept
// from an earlier https setup is dropped: nothing reads it over http, and left
// in place it would keep pointing Claude Code and Codex at a file that may no
// longer exist.
func TestPromptCACertFileClearsCAForPlainHTTP(t *testing.T) {
	p := profile.NewDefault()
	p.Server.SyncEndpoint = "http://192.168.0.10:18080"
	p.Server.Endpoint = "http://10.0.0.5:8080"
	p.Server.CACertFile = writeTestCAFile(t, t.TempDir())
	ir := &inputReader{reader: bufio.NewReader(strings.NewReader("/should/not/be/read\n"))}
	if err := promptCACertFile(ir, p); err != nil {
		t.Fatalf("promptCACertFile: %v", err)
	}
	if p.Server.CACertFile != "" {
		t.Fatalf("CACertFile = %q, want it cleared for plain http", p.Server.CACertFile)
	}
}

// Re-running init offers the stored CA as the default, so Enter keeps it. A
// stored CA that is wrong or gone has to be removable from init too, which an
// empty answer cannot do; "none" and "-" do.
func TestPromptCACertFileExplicitClear(t *testing.T) {
	for _, answer := range []string{"none", "-"} {
		p := profile.NewDefault()
		p.Server.SyncEndpoint = "https://cctrace.example.test:8443"
		p.Server.CACertFile = filepath.Join(t.TempDir(), "moved-away.crt")
		ir := &inputReader{reader: bufio.NewReader(strings.NewReader(answer + "\n"))}
		if err := promptCACertFile(ir, p); err != nil {
			t.Fatalf("promptCACertFile(%q): %v", answer, err)
		}
		if p.Server.CACertFile != "" {
			t.Fatalf("answer %q: CACertFile = %q, want cleared", answer, p.Server.CACertFile)
		}
	}
}

// A stored CA that is gone, and no answer that fixes it: init stops, and the
// error says how to drop the setting outside init.
func TestPromptCACertFileUnreadableStoredCANamesTheFix(t *testing.T) {
	p := profile.NewDefault()
	p.Server.SyncEndpoint = "https://cctrace.example.test:8443"
	p.Server.CACertFile = filepath.Join(t.TempDir(), "moved-away.crt")
	ir := &inputReader{reader: bufio.NewReader(strings.NewReader("\n\n\n"))}
	err := promptCACertFile(ir, p)
	if err == nil || !strings.Contains(err.Error(), `cctrace config set server.ca_cert_file ""`) {
		t.Fatalf("promptCACertFile = %v, want an error naming the config set fix", err)
	}
}

// An https endpoint asks; a wrong path is asked again rather than stored, and an
// empty answer leaves the system roots in charge.
func TestPromptCACertFileForHTTPS(t *testing.T) {
	ca := writeTestCAFile(t, t.TempDir())

	p := profile.NewDefault()
	p.Server.SyncEndpoint = "https://cctrace.example.test:8443"
	ir := &inputReader{reader: bufio.NewReader(strings.NewReader("/no/such/root.crt\n" + ca + "\n"))}
	if err := promptCACertFile(ir, p); err != nil {
		t.Fatalf("promptCACertFile: %v", err)
	}
	if p.Server.CACertFile != ca {
		t.Fatalf("CACertFile = %q, want %q", p.Server.CACertFile, ca)
	}

	p = profile.NewDefault()
	p.Server.Endpoint = "https://cctrace.example.test:5317"
	ir = &inputReader{reader: bufio.NewReader(strings.NewReader("\n"))}
	if err := promptCACertFile(ir, p); err != nil {
		t.Fatalf("promptCACertFile(empty): %v", err)
	}
	if p.Server.CACertFile != "" {
		t.Fatalf("CACertFile = %q, want empty", p.Server.CACertFile)
	}
}

// The init re-run menu's Codex patch and env re-apply skip the CA prompt, so
// they check the stored CA themselves. A CA file that was valid when stored and
// has since gone must stop them before anything is written, with the way out,
// rather than writing the dead path into Codex's and Claude Code's settings and
// printing [OK].
func TestStoredCAIsCheckedWhereThePromptIsSkipped(t *testing.T) {
	home := setupTestHome(t)
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_CONFIG_DIR", codexDir)
	t.Setenv("CODEX_HOME", "")
	p, err := profile.Load()
	if err != nil {
		t.Fatal(err)
	}
	p.Server.CACertFile = filepath.Join(t.TempDir(), "deleted.crt")
	if err := profile.Save(p); err != nil {
		t.Fatal(err)
	}

	err = runCodexPatch(p, false, "")
	if err == nil || !strings.Contains(err.Error(), `cctrace config set server.ca_cert_file ""`) {
		t.Fatalf("runCodexPatch = %v, want an error naming the config set fix", err)
	}
	if _, statErr := os.Stat(filepath.Join(codexDir, "config.toml")); !os.IsNotExist(statErr) {
		t.Fatalf("Codex config.toml was written despite the missing CA (stat err %v)", statErr)
	}

	err = applyDefaultProfile()
	if err == nil || !strings.Contains(err.Error(), `cctrace config set server.ca_cert_file ""`) {
		t.Fatalf("applyDefaultProfile = %v, want an error naming the config set fix", err)
	}
}

// server.protocol stays as stored, but with a private CA on an https endpoint
// Claude Code is sent over http/protobuf on the OTLP/HTTP port. init and
// config set say so once, so the stored grpc does not read as what is used.
func TestNoteClaudeProtocolOverride(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint, protocol, ca string
		note                         bool
	}{
		{"grpc, CA, https", "https://cctrace.example.test:5317", "grpc", "/ca.crt", true},
		{"default protocol, CA, https", "https://cctrace.example.test:5317", "", "/ca.crt", true},
		{"no CA", "https://cctrace.example.test:5317", "grpc", "", false},
		{"plain http", "http://10.0.0.5:8080", "grpc", "/ca.crt", false},
		{"already http/protobuf", "https://cctrace.example.test:5318", "http/protobuf", "/ca.crt", false},
	} {
		p := profile.NewDefault()
		p.Server.Endpoint = tc.endpoint
		p.Server.Protocol = tc.protocol
		p.Server.CACertFile = tc.ca
		var out strings.Builder
		noteClaudeProtocolOverride(&out, p)
		got := out.String()
		if (got != "") != tc.note {
			t.Errorf("%s: note = %q, want note %v", tc.name, got, tc.note)
		}
		if tc.note && (!strings.Contains(got, "http/protobuf") || !strings.Contains(got, "https://cctrace.example.test:5318") || !strings.Contains(got, "2.1.291")) {
			t.Errorf("%s: note %q does not name the protocol, the mapped endpoint and the measured version", tc.name, got)
		}
	}
}
