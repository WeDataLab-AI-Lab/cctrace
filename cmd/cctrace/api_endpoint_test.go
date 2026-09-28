package main

import (
	"testing"

	"cctrace/internal/profile"
)

func TestProfileHTTPAPIEndpointPrefersSyncEndpoint(t *testing.T) {
	p := &profile.Profile{}
	p.Server.Endpoint = "http://trace.example.com:14317"
	p.Server.SyncEndpoint = "http://trace.example.com:18080"

	got := profileHTTPAPIEndpoint(p)
	if got != "http://trace.example.com:18080" {
		t.Fatalf("endpoint = %q, want sync endpoint", got)
	}
}

func TestProfileHTTPAPIEndpointFallsBackToEndpoint(t *testing.T) {
	p := &profile.Profile{}
	p.Server.Endpoint = "http://trace.example.com:8080/"

	got := profileHTTPAPIEndpoint(p)
	if got != "http://trace.example.com:8080" {
		t.Fatalf("endpoint = %q, want trimmed fallback endpoint", got)
	}
}

func TestProfileHTTPAPIEndpointNilProfile(t *testing.T) {
	if got := profileHTTPAPIEndpoint(nil); got != "" {
		t.Fatalf("endpoint = %q, want empty", got)
	}
}
