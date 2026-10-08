package codexconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testCAFile = "/etc/cctrace/root.crt"

// Codex's OTLP exporter takes a private CA from its own tls table. Measured on
// Codex 0.160.0: with this table an https collector behind a test CA received
// the metrics, and without it nothing arrived and nothing was logged.
func TestEnsureOtelBlockWritesCATLSTable(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureOtelBlock(dir, "https://cctrace.example.test:4317", "tok", testCAFile); err != nil {
		t.Fatalf("EnsureOtelBlock: %v", err)
	}
	content := readConfigFile(t, dir)
	want := `headers = { Authorization = "Bearer tok" }, tls = { ca-certificate = "/etc/cctrace/root.crt" } } }`
	if !strings.Contains(content, want) {
		t.Fatalf("config.toml lacks the tls table inside otlp-http:\n%s", content)
	}

	// Already current: the self-heal that runs every sync must not rewrite it.
	if changed, err := EnsureOtelBlock(dir, "https://cctrace.example.test:4317", "tok", testCAFile); err != nil || changed {
		t.Fatalf("second EnsureOtelBlock changed=%v err=%v, want unchanged", changed, err)
	}
}

// No CA, no tls table: a profile that never set one writes the same block as
// before, so existing installs are not rewritten by this change.
func TestEnsureOtelBlockOmitsTLSWithoutCA(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureOtelBlock(dir, "http://localhost:4317", "tok", ""); err != nil {
		t.Fatalf("EnsureOtelBlock: %v", err)
	}
	if content := readConfigFile(t, dir); strings.Contains(content, "tls") {
		t.Fatalf("tls written without a CA:\n%s", content)
	}
}

// An extra home cctrace owns follows the profile's CA both ways: it gains the
// table when the CA is set and loses it when the CA is cleared.
func TestHealExistingOtelBlockFollowsCA(t *testing.T) {
	dir := t.TempDir()
	const endpoint = "https://cctrace.example.test:4317"
	if _, err := EnsureOtelBlock(dir, endpoint, "tok", ""); err != nil {
		t.Fatal(err)
	}

	if got, err := HealExistingOtelBlock(dir, endpoint, "tok", testCAFile, true); err != nil || got != HealWritten {
		t.Fatalf("heal with CA = %v, %v; want HealWritten", got, err)
	}
	if content := readConfigFile(t, dir); !strings.Contains(content, `ca-certificate = "/etc/cctrace/root.crt"`) {
		t.Fatalf("CA missing after heal:\n%s", content)
	}

	if got, err := HealExistingOtelBlock(dir, endpoint, "tok", "", true); err != nil || got != HealWritten {
		t.Fatalf("heal after clearing CA = %v, %v; want HealWritten", got, err)
	}
	if content := readConfigFile(t, dir); strings.Contains(content, "tls") {
		t.Fatalf("tls left behind after the CA was cleared:\n%s", content)
	}
}

func readConfigFile(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
