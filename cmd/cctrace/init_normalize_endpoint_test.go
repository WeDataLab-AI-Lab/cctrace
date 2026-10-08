package main

import "testing"

// normalizeEndpoint's scheme-less default stays http:// by decision (#533,
// 2026-09-21): the TLS overlay (deploy/docker-compose.tls.yml) is opt-in, TLS
// ports are the plaintext port +1000 so a scheme switch alone would not reach
// them, and Codex sends nothing to an https OTLP endpoint behind a private CA
// unless server.ca_cert_file puts that CA in its block (#644). This pins the
// current behavior so a future change to any of the three is deliberate.
func TestNormalizeEndpoint(t *testing.T) {
	cases := []struct {
		in, want, why string
	}{
		{"host:8080", "http://host:8080", "scheme-less input gets http:// prepended"},
		{"  host:8080  ", "http://host:8080", "surrounding whitespace is trimmed before the scheme is added"},
		{"http://host:8080", "http://host:8080", "an explicit http scheme is left alone"},
		{"https://host:8080", "https://host:8080", "an explicit https scheme is kept, not overridden"},
		{"host:8080/", "http://host:8080/", "a trailing slash is preserved, not stripped"},
		{"https://host:8080/", "https://host:8080/", "a trailing slash is preserved on an https input too"},
		{"", "", "empty input stays empty"},
		{"   ", "", "whitespace-only input trims to empty"},
	}
	for _, c := range cases {
		if got := normalizeEndpoint(c.in); got != c.want {
			t.Errorf("normalizeEndpoint(%q) = %q, want %q -- %s", c.in, got, c.want, c.why)
		}
	}
}
