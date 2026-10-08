package envgen

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cctrace/internal/profile"
)

const envNodeExtraCACertsName = "NODE_EXTRA_CA_CERTS"

// caTestProfile is testProfile with an https OTLP gRPC endpoint, the case a
// private CA applies to.
func caTestProfile(claudeDir string) *profile.Profile {
	p := testProfile(claudeDir)
	p.Server.Endpoint = "https://trace.example.com:4317"
	return p
}

// caTestSettings creates a Claude home whose settings.json holds env, and
// returns its directory, a reader for its env section, and the warnings an
// apply prints.
func caTestSettings(t *testing.T, env map[string]interface{}) (string, func() map[string]interface{}, *bytes.Buffer) {
	t.Helper()
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")
	writeJSON(t, settingsPath, map[string]interface{}{"env": env})
	var warned bytes.Buffer
	prev := warnOut
	warnOut = &warned
	t.Cleanup(func() { warnOut = prev })
	readEnv := func() map[string]interface{} {
		e, _ := readJSON(t, settingsPath)["env"].(map[string]interface{})
		return e
	}
	return claudeDir, readEnv, &warned
}

// Measured on Claude Code 2.1.291 against a private CA: the grpc exporter
// aborted the TLS handshake with OTEL_EXPORTER_OTLP_CERTIFICATE and with
// NODE_EXTRA_CA_CERTS, and only http/protobuf with NODE_EXTRA_CA_CERTS reached
// the server. So a profile with a CA and an https endpoint sends Claude Code
// over http/protobuf to the OTLP/HTTP port beside the gRPC one, whatever
// server.protocol says.
func TestBuildEnvMapPrivateCAOverHTTPS(t *testing.T) {
	for in, want := range map[string]string{
		"https://trace.example.com:4317":  "https://trace.example.com:4318",
		"https://trace.example.com:14317": "https://trace.example.com:14318",
		"https://trace.example.com:5317":  "https://trace.example.com:5318",
	} {
		p := fullProfile()
		p.Server.Endpoint = in
		p.Server.CACertFile = "/etc/cctrace/root.crt"
		env := BuildEnvMap(p)
		if env["OTEL_EXPORTER_OTLP_PROTOCOL"] != "http/protobuf" {
			t.Errorf("%s: protocol = %q, want http/protobuf", in, env["OTEL_EXPORTER_OTLP_PROTOCOL"])
		}
		if env["OTEL_EXPORTER_OTLP_ENDPOINT"] != want {
			t.Errorf("%s: endpoint = %q, want %q", in, env["OTEL_EXPORTER_OTLP_ENDPOINT"], want)
		}
		if _, has := env["OTEL_EXPORTER_OTLP_CERTIFICATE"]; has {
			t.Errorf("%s: OTEL_EXPORTER_OTLP_CERTIFICATE written; it did nothing for Claude Code in measurement", in)
		}
	}
}

// Without a CA, or with a plain http endpoint, the Claude Code environment is
// exactly what it was before the CA existed -- same map, same self-heal hash --
// so no existing install is rewritten.
func TestClaudeEnvUnchangedWithoutCAOrHTTPS(t *testing.T) {
	base := fullProfile()
	withHTTPCA := fullProfile()
	withHTTPCA.Server.CACertFile = "/etc/cctrace/root.crt"
	if !mapsEqual(BuildEnvMap(base), BuildEnvMap(withHTTPCA)) {
		t.Fatalf("a CA on an http endpoint changed the env:\n%v\n%v", BuildEnvMap(base), BuildEnvMap(withHTTPCA))
	}
	if managedSettingsHash(base) != managedSettingsHash(withHTTPCA) {
		t.Fatal("a CA on an http endpoint changed the self-heal hash")
	}
	httpsNoCA := fullProfile()
	httpsNoCA.Server.Endpoint = "https://trace.example.com:4317"
	env := BuildEnvMap(httpsNoCA)
	if env["OTEL_EXPORTER_OTLP_PROTOCOL"] != "grpc" || env["OTEL_EXPORTER_OTLP_ENDPOINT"] != "https://trace.example.com:4317" {
		t.Fatalf("https without a CA changed protocol or endpoint: %v", env)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// NODE_EXTRA_CA_CERTS is commonly set already, for a company proxy. A value
// cctrace did not write belongs to the user: with no profile CA it survives
// every apply (config set of an unrelated key re-applies too) and reset.
func TestUserNodeExtraCACertsSurvivesWithoutProfileCA(t *testing.T) {
	claudeDir, readEnv, _ := caTestSettings(t, map[string]interface{}{envNodeExtraCACertsName: "/corp/proxy.pem"})
	p := caTestProfile(claudeDir)

	if err := ApplyToClaudeSettings(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := readEnv()[envNodeExtraCACertsName]; got != "/corp/proxy.pem" {
		t.Fatalf("%s = %v after apply, want the user's value", envNodeExtraCACertsName, got)
	}
	if err := RemoveFromClaudeSettings(p); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got := readEnv()[envNodeExtraCACertsName]; got != "/corp/proxy.pem" {
		t.Fatalf("%s = %v after reset, want the user's value", envNodeExtraCACertsName, got)
	}
}

// cctrace writes the profile's CA, follows a change of CA, and on clear -- or
// when the endpoint stops being https -- removes only the value it wrote.
// OTEL_EXPORTER_OTLP_CERTIFICATE is never written.
func TestApplyWritesAndRemovesOnlyItsOwnNodeExtraCACerts(t *testing.T) {
	claudeDir, readEnv, _ := caTestSettings(t, map[string]interface{}{})
	p := caTestProfile(claudeDir)

	p.Server.CACertFile = "/etc/cctrace/a.crt"
	if err := ApplyToClaudeSettings(p); err != nil {
		t.Fatalf("apply with CA: %v", err)
	}
	env := readEnv()
	if env[envNodeExtraCACertsName] != "/etc/cctrace/a.crt" {
		t.Fatalf("%s = %v after apply with CA", envNodeExtraCACertsName, env[envNodeExtraCACertsName])
	}
	if _, has := env["OTEL_EXPORTER_OTLP_CERTIFICATE"]; has {
		t.Fatal("OTEL_EXPORTER_OTLP_CERTIFICATE written")
	}

	p.Server.CACertFile = "/etc/cctrace/b.crt"
	if err := ApplyToClaudeSettings(p); err != nil {
		t.Fatalf("apply with a new CA: %v", err)
	}
	if got := readEnv()[envNodeExtraCACertsName]; got != "/etc/cctrace/b.crt" {
		t.Fatalf("%s = %v after changing the CA", envNodeExtraCACertsName, got)
	}

	p.Server.CACertFile = ""
	if err := ApplyToClaudeSettings(p); err != nil {
		t.Fatalf("apply after clearing CA: %v", err)
	}
	if v, has := readEnv()[envNodeExtraCACertsName]; has {
		t.Fatalf("%s = %v still present after the CA was cleared", envNodeExtraCACertsName, v)
	}

	p.Server.CACertFile = "/etc/cctrace/a.crt"
	if err := ApplyToClaudeSettings(p); err != nil {
		t.Fatalf("apply with CA again: %v", err)
	}
	p.Server.Endpoint = "http://trace.company.com:4317"
	if err := ApplyToClaudeSettings(p); err != nil {
		t.Fatalf("apply on plain http: %v", err)
	}
	if v, has := readEnv()[envNodeExtraCACertsName]; has {
		t.Fatalf("%s = %v still present on a plain http endpoint", envNodeExtraCACertsName, v)
	}
}

// Once the user edits the value cctrace wrote, it is theirs again: clearing the
// profile CA, or reset, must not take their edit with it.
func TestClearLeavesHandEditedNodeExtraCACerts(t *testing.T) {
	claudeDir, readEnv, _ := caTestSettings(t, map[string]interface{}{})
	p := caTestProfile(claudeDir)
	p.Server.CACertFile = "/etc/cctrace/a.crt"
	if err := ApplyToClaudeSettings(p); err != nil {
		t.Fatalf("apply with CA: %v", err)
	}

	settingsPath := filepath.Join(claudeDir, "settings.json")
	s := readJSON(t, settingsPath)
	s["env"].(map[string]interface{})[envNodeExtraCACertsName] = "/corp/edited.pem"
	writeJSON(t, settingsPath, s)

	p.Server.CACertFile = ""
	if err := ApplyToClaudeSettings(p); err != nil {
		t.Fatalf("apply after clearing CA: %v", err)
	}
	if got := readEnv()[envNodeExtraCACertsName]; got != "/corp/edited.pem" {
		t.Fatalf("%s = %v, want the user's edit kept", envNodeExtraCACertsName, got)
	}
	if err := RemoveFromClaudeSettings(p); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got := readEnv()[envNodeExtraCACertsName]; got != "/corp/edited.pem" {
		t.Fatalf("%s = %v after reset, want the user's edit kept", envNodeExtraCACertsName, got)
	}
}

// Setting a profile CA while the user's own, different value is in place keeps
// the user's value and says so, naming both. Overwriting would lose a value
// cctrace never recorded -- typically the company proxy's CA.
func TestApplyKeepsUnrecordedUserNodeExtraCACertsAndWarns(t *testing.T) {
	claudeDir, readEnv, warned := caTestSettings(t, map[string]interface{}{envNodeExtraCACertsName: "/corp/proxy.pem"})
	p := caTestProfile(claudeDir)
	p.Server.CACertFile = "/etc/cctrace/a.crt"
	if err := ApplyToClaudeSettings(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := readEnv()[envNodeExtraCACertsName]; got != "/corp/proxy.pem" {
		t.Fatalf("%s = %v, want the user's value kept", envNodeExtraCACertsName, got)
	}
	if !strings.Contains(warned.String(), "/corp/proxy.pem") || !strings.Contains(warned.String(), "/etc/cctrace/a.crt") {
		t.Fatalf("warning does not name both values: %q", warned.String())
	}
}

// reset removes the value cctrace wrote, like every other key it owns.
func TestResetRemovesItsOwnNodeExtraCACerts(t *testing.T) {
	claudeDir, readEnv, _ := caTestSettings(t, map[string]interface{}{})
	p := caTestProfile(claudeDir)
	p.Server.CACertFile = "/etc/cctrace/a.crt"
	if err := ApplyToClaudeSettings(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := RemoveFromClaudeSettings(p); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if v, has := readEnv()[envNodeExtraCACertsName]; has {
		t.Fatalf("%s = %v after reset", envNodeExtraCACertsName, v)
	}
}

// The self-heal stamp follows the CA on an https endpoint, so a profile CA
// changed outside `config set` still reaches settings.json on the next sync.
func TestManagedSettingsHashFollowsCA(t *testing.T) {
	p := caTestProfile("/nonexistent/.claude")
	before := managedSettingsHash(p)
	p.Server.CACertFile = "/etc/cctrace/a.crt"
	if managedSettingsHash(p) == before {
		t.Fatal("managedSettingsHash ignores server.ca_cert_file")
	}
}

// A profile without a CA must hash exactly as it did before the key existed.
// The hash is the self-heal stamp: if it moved, every existing install would
// rewrite settings.json on its next sync for a change that does not apply to it.
// The value is the hash this profile produced before OTEL_EXPORTER_OTLP_CERTIFICATE
// was managed; sync is off so the hook commands (which embed the binary path)
// stay out of it.
//
// Only the first 16 hex digits are pinned. That is 64 bits, plenty to catch any
// change to the hashed content. The full 64-digit value would be a high-entropy
// token the mirror gate cannot approve by rule (scripts/mirror-baseline-lint.go
// keeps 'entropy' unapprovable so prefix-less secrets stay visible).
func TestManagedSettingsHashUnchangedWithoutCA(t *testing.T) {
	p := testProfile("/nonexistent/.claude")
	p.Options.SyncEnabled = false
	const beforePrefix = "49e1939dab511176"
	if got := managedSettingsHash(p); !strings.HasPrefix(got, beforePrefix) {
		t.Fatalf("managedSettingsHash = %s, want prefix %s (unchanged for a profile without a CA)", got, beforePrefix)
	}
}
