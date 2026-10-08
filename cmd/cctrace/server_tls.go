package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"time"

	"cctrace/internal/profile"
)

// serverTransport returns the transport for talking to the cctrace server when
// server.ca_cert_file names a private CA, and nil when it is empty so the client
// keeps Go's default transport exactly as before.
//
// It clones http.DefaultTransport rather than building a fresh one: the clone
// keeps ProxyFromEnvironment, which some installs rely on to reach the server
// through a socks proxy, and keeps HTTP/2, which a bare &http.Transport{} turns
// off once TLSClientConfig is set. The CA is added to a copy of the system
// roots, so a server with a public certificate is still trusted.
//
// A CA file that cannot be read or holds no certificate is an error. Quietly
// falling back to the system roots would turn a typo into a TLS failure with no
// mention of the file that caused it. The file was valid when it was stored, so
// the error also says how to stop using it.
func serverTransport(caFile string) (http.RoundTripper, error) {
	if caFile == "" {
		return nil, nil
	}
	const clearHint = `fix the file, or clear the setting with: cctrace config set server.ca_cert_file ""`
	raw, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("server.ca_cert_file: %w; %s", err, clearHint)
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("server.ca_cert_file: system roots: %w", err)
	}
	if !pool.AppendCertsFromPEM(raw) {
		return nil, fmt.Errorf("server.ca_cert_file %s holds no PEM certificate; %s", caFile, clearHint)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool}
	return tr, nil
}

// checkStoredCACertFile validates the profile's server.ca_cert_file the way
// `config set` and the init prompt do. Paths that reuse a stored profile
// without asking -- the init re-run menu's Codex patch, `env apply` -- call it
// before writing anything, so a CA file that has since moved or changed stops
// them with the way out instead of being copied into Claude Code's and Codex's
// settings as if it were valid.
func checkStoredCACertFile(p *profile.Profile) error {
	if p.Server.CACertFile == "" {
		return nil
	}
	if _, err := normalizeCACertFile(p.Server.CACertFile); err != nil {
		return fmt.Errorf("%w; fix the file, answer 'none' to the CA question of a full 'cctrace init', or run: cctrace config set server.ca_cert_file \"\"", err)
	}
	return nil
}

// serverClient is an *http.Client for the cctrace server with the profile's CA.
// timeout 0 means none, the same as http.DefaultClient it replaces.
func serverClient(caFile string, timeout time.Duration) (*http.Client, error) {
	tr, err := serverTransport(caFile)
	if err != nil {
		return nil, err
	}
	return &http.Client{Timeout: timeout, Transport: tr}, nil
}
