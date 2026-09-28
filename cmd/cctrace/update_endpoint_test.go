package main

import "testing"

// The endpoint policy shipped in v0.7.0 rejected the deployment it was meant to
// protect: clients read the update endpoint from their profile's sync endpoint,
// which on an internal network is a plain-HTTP private address. Every client that
// reached v0.7.0 stopped being able to self-update, and no test noticed because
// none of them used an address anyone actually deploys to.
func TestValidateUpdateEndpoint(t *testing.T) {
	allowed := map[string]string{
		"https://trace.example.com": "TLS is always fine",
		"http://localhost:8080":     "loopback by name",
		"http://127.0.0.1:8080":     "loopback literal",
		"http://[::1]:8080":         "IPv6 loopback",
		"http://10.20.30.40:18080":  "a deployed production endpoint",
		"http://10.20.30.40:28080":  "a deployed dev endpoint",
		"http://10.0.0.5:8080":      "RFC1918 10/8",
		"http://172.16.0.5:8080":    "RFC1918 172.16/12",
		"http://[fd00::1]:8080":     "IPv6 unique local",
	}
	for endpoint, why := range allowed {
		if err := validateUpdateEndpoint(endpoint); err != nil {
			t.Errorf("%s (%s) must be allowed: %v", endpoint, why, err)
		}
	}

	// Widening the exemption must not reach the public internet, and must not
	// follow a hostname whose resolution the client cannot reason about.
	rejected := map[string]string{
		"http://trace.example.com":  "public hostname over plain HTTP",
		"http://8.8.8.8:8080":       "public address over plain HTTP",
		"http://trace.internal:808": "private-looking hostname still resolves via DNS",
		"ftp://10.20.30.40":         "unsupported scheme",
		"10.20.30.40:18080":         "no scheme",
	}
	for endpoint, why := range rejected {
		if err := validateUpdateEndpoint(endpoint); err == nil {
			t.Errorf("%s (%s) must be rejected", endpoint, why)
		}
	}
}

// Guards the specific regression: whatever the policy becomes, the endpoints this
// project actually deploys to have to satisfy it.
func TestDeployedEndpointsSatisfyPolicy(t *testing.T) {
	for _, ep := range []string{
		"http://10.20.30.40:18080", // production, profile.json sync_endpoint
		"http://10.20.30.40:28080", // dev
	} {
		if err := validateUpdateEndpoint(ep); err != nil {
			t.Fatalf("clients configured with %s cannot self-update: %v", ep, err)
		}
	}
}
