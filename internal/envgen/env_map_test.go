package envgen

import (
	"strings"
	"testing"
)

// BuildEnvMap writes the keys and otelKeySet decides which keys cctrace owns.
// They must name the same set. A key written but not owned is captured by
// takeSnapshot as user data, overwritten by the apply, and then rejected by
// verify with "env key was modified" — which aborts ApplyToClaudeSettings
// entirely, i.e. `cctrace init` stops working. A key owned but not written is
// harmless but signals the lists have drifted.
func TestBuildEnvMapKeysMatchOtelKeySet(t *testing.T) {
	// A profile with endpoint and token set so the conditional keys appear.
	p := fullProfile()
	written := BuildEnvMap(p)

	for k := range written {
		if !otelKeySet[k] {
			t.Errorf("BuildEnvMap writes %q but otelKeySet does not own it: apply would abort on the settings guard", k)
		}
	}
	for k := range otelKeySet {
		if _, ok := written[k]; !ok {
			t.Errorf("otelKeySet owns %q but BuildEnvMap never writes it", k)
		}
	}
}

func TestBuildEnvMapOmitsOptionalTransportFieldsWhenUnset(t *testing.T) {
	p := fullProfile()
	p.Server.Endpoint = ""
	p.Server.AuthToken = ""

	env := BuildEnvMap(p)

	if _, has := env["OTEL_EXPORTER_OTLP_ENDPOINT"]; has {
		t.Fatal("endpoint must be omitted when profile server endpoint is empty")
	}
	if _, has := env["OTEL_EXPORTER_OTLP_HEADERS"]; has {
		t.Fatal("headers must be omitted when profile auth token is empty")
	}
	if env["OTEL_EXPORTER_OTLP_PROTOCOL"] != "grpc" {
		t.Fatalf("protocol = %q, want grpc", env["OTEL_EXPORTER_OTLP_PROTOCOL"])
	}
	if !strings.Contains(env["OTEL_RESOURCE_ATTRIBUTES"], "user.profile.email=alice@example.com") {
		t.Fatalf("resource attributes missing user email: %q", env["OTEL_RESOURCE_ATTRIBUTES"])
	}
}
