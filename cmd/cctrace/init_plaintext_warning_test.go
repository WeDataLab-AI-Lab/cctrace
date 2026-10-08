package main

import "testing"

// The sync channel carries conversation transcripts. Unlike the update channel,
// no signature makes plain HTTP acceptable there -- a signed manifest protects
// integrity, and what is at stake here is confidentiality (#533).
//
// It warns rather than refuses: plain HTTP inside a company network is still a
// deployment someone may choose. But it is no longer silent there. A private
// address is a network other machines share, and exempting it meant the
// production server ran plain HTTP with nobody told. Only this machine itself
// (loopback, "localhost") is exempt. The update channel keeps its wider
// exemption, which rests on the signature rather than on where the server is.
func TestPlaintextWarningBoundary(t *testing.T) {
	quiet := []struct{ endpoint, why string }{
		{"https://cctrace.example.com", "HTTPS is the point of the warning, not a target of it"},
		{"https://cctrace.example.com:8080", "a port does not change the scheme"},
		{"http://localhost:8080", "loopback by name"},
		{"http://127.0.0.1:8080", "loopback literal"},
		{"http://[::1]:8080", "loopback literal, v6"},
	}
	for _, c := range quiet {
		if plaintextLeavesTheLocalNetwork(c.endpoint) {
			t.Errorf("%s warned; it should not -- %s", c.endpoint, c.why)
		}
	}

	loud := []struct{ endpoint, why string }{
		{"http://cctrace.example.com", "a hostname is not exempt: the client cannot reason about what DNS resolves it to"},
		{"http://203.0.113.10:18080", "a public address literal"},
		{"http://8.8.8.8:8080", "a public address literal"},
		{"http://192.168.0.10:18080", "private range -- other machines on the network can read it"},
		{"http://10.0.0.5:8080", "private range"},
		{"http://172.16.0.5:8080", "private range"},
		{"http://169.254.10.1:8080", "link-local"},
	}
	for _, c := range loud {
		if !plaintextLeavesTheLocalNetwork(c.endpoint) {
			t.Errorf("%s stayed quiet; it should warn -- %s", c.endpoint, c.why)
		}
	}
}

// A hostname that happens to resolve privately still warns. Exempting it would
// put the decision in DNS, which the install cannot see and the operator may not
// control.
func TestPrivateLookingHostnamesStillWarn(t *testing.T) {
	for _, endpoint := range []string{
		"http://cctrace.internal",
		"http://cctrace.local",
		"http://intranet",
	} {
		if !plaintextLeavesTheLocalNetwork(endpoint) {
			t.Errorf("%s stayed quiet; only loopback literals and \"localhost\" are exempt", endpoint)
		}
	}
}

// A string that is not a URL must not crash the install, and must not be
// reported as a plaintext leak either -- it never becomes a request.
func TestUnparseableEndpointDoesNotWarn(t *testing.T) {
	for _, endpoint := range []string{"", "://", "ht tp://x", "not a url at all"} {
		if plaintextLeavesTheLocalNetwork(endpoint) {
			t.Errorf("%q warned; it is not an http endpoint", endpoint)
		}
	}
}
