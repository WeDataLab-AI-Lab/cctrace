package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateTrustIPCRequiresExplicitNegotiationAndResets(t *testing.T) {
	t.Parallel()
	s, err := New(Config{AppSupportRoot: t.TempDir(), ProfileRoot: filepath.Join(t.TempDir(), "registry")})
	if err != nil {
		t.Fatal(err)
	}
	input := strings.Join([]string{
		`{"version":1,"kind":"request","id":"before","op":"update.trust.read","payload":{"origin":"https://updates.invalid:8443"}}`,
		`{"version":1,"kind":"request","id":"hello","op":"hello","payload":{"update_trust":true}}`,
		`{"version":1,"kind":"request","id":"independent","op":"hello","payload":{"profile_registry":true}}`,
		`{"version":1,"kind":"request","id":"after","op":"update.trust.read","payload":{"origin":"https://updates.invalid:8443"}}`,
		`{"version":1,"kind":"request","id":"reset","op":"hello","payload":{}}`,
		`{"version":1,"kind":"request","id":"revoked","op":"update.trust.read","payload":{"origin":"https://updates.invalid:8443"}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := Serve(context.Background(), s, strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 6 {
		t.Fatalf("responses: %s", output.String())
	}
	for index, code := range map[int]string{0: "capability_not_negotiated", 3: "trust_not_configured", 5: "capability_not_negotiated"} {
		var response Response
		if err := json.Unmarshal([]byte(lines[index]), &response); err != nil || response.Error == nil || response.Error.Code != code {
			t.Fatalf("response %d: %s; want %s", index, lines[index], code)
		}
	}
	if !strings.Contains(lines[1], `"update_trust":true`) || strings.Contains(lines[4], `"update_trust"`) {
		t.Fatalf("capability leaked or missing: %s", output.String())
	}
}

func TestUpdateTrustIPCLegacyHelloAndMissingRegistry(t *testing.T) {
	t.Parallel()
	s, err := New(Config{AppSupportRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	legacy := call(t, s, "legacy", "hello", map[string]any{})
	if legacy.Error != nil || bytes.Contains(legacy.Result, []byte("update_trust")) {
		t.Fatalf("legacy hello changed: %+v", legacy)
	}
	response := call(t, s, "opt-in", "hello", map[string]any{"update_trust": true})
	if response.Error != nil || bytes.Contains(response.Result, []byte(`"update_trust":true`)) {
		t.Fatalf("capability advertised without registry: %+v", response)
	}
}
