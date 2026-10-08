package codexconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Legacy table-form [otel.metrics_exporter.otlp-http] must be replaced in
// place, not duplicated by appending a second inline [otel] (which produced a
// duplicate metrics_exporter key that broke Codex's TOML parser).
func TestWriteOtelBlock_LegacyTableForm_NoDuplicate(t *testing.T) {
	dir := t.TempDir()
	existing := `model = "gpt-5"

[otel.metrics_exporter.otlp-http]
endpoint = "http://old:14318/v1/metrics"
protocol = "binary"

[otel.metrics_exporter.otlp-http.headers]
Authorization = "Bearer old-token"

[plugins.foo]
bar = 1
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(existing), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := WriteOtelBlock(dir, "http://new:14317", "new-token", ""); err != nil {
		t.Fatalf("write: %v", err)
	}
	s := readConfigToml(t, dir)

	if n := strings.Count(s, "metrics_exporter"); n != 1 {
		t.Fatalf("metrics_exporter count = %d, want 1\n%s", n, s)
	}
	if strings.Contains(s, "[otel.metrics_exporter") {
		t.Fatalf("legacy table form not removed:\n%s", s)
	}
	if !strings.Contains(s, "new:14318") || !strings.Contains(s, "new-token") {
		t.Fatalf("new values not written:\n%s", s)
	}
	if !strings.Contains(s, "[plugins.foo]") || !strings.Contains(s, `model = "gpt-5"`) {
		t.Fatalf("non-otel config lost:\n%s", s)
	}
}

// An already-broken config (table form AND inline both present) must collapse
// to a single otel section.
func TestWriteOtelBlock_DuplicateOtel_Collapses(t *testing.T) {
	dir := t.TempDir()
	existing := `[otel.metrics_exporter.otlp-http]
endpoint = "http://a:14318/v1/metrics"

[otel]
metrics_exporter = { otlp-http = { endpoint = "http://b:14318/v1/metrics" } }
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(existing), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := WriteOtelBlock(dir, "http://c:14317", "tok", ""); err != nil {
		t.Fatalf("write: %v", err)
	}
	s := readConfigToml(t, dir)
	if n := strings.Count(s, "metrics_exporter"); n != 1 {
		t.Fatalf("metrics_exporter count = %d, want 1\n%s", n, s)
	}
}

func TestReadConfig_TableForm(t *testing.T) {
	dir := t.TempDir()
	existing := `[otel.metrics_exporter.otlp-http]
endpoint = "http://x:14318/v1/metrics"
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(existing), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cfg, err := ReadConfig(dir)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !cfg.HasOtel {
		t.Fatal("table-form [otel.x] not detected as otel")
	}
	if cfg.OtelEndpoint != "http://x:14318/v1/metrics" {
		t.Fatalf("endpoint = %q, want http://x:14318/v1/metrics", cfg.OtelEndpoint)
	}
}

// EnsureOtelBlock must be a no-op when the config already matches.
func TestEnsureOtelBlock_NoopWhenCurrent(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureOtelBlock(dir, "http://x:14317", "tok", ""); err != nil {
		t.Fatalf("first: %v", err)
	}
	changed, err := EnsureOtelBlock(dir, "http://x:14317", "tok", "")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if changed {
		t.Fatal("already-current config must be a no-op (changed=false)")
	}
}

// EnsureOtelBlock heals a legacy table-form config in place (self-heal path for
// already-enabled users).
func TestEnsureOtelBlock_HealsLegacyTableForm(t *testing.T) {
	dir := t.TempDir()
	existing := `[otel.metrics_exporter.otlp-http]
endpoint = "http://old:14318/v1/metrics"
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(existing), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	changed, err := EnsureOtelBlock(dir, "http://x:14317", "tok", "")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !changed {
		t.Fatal("legacy table form must trigger a rewrite (changed=true)")
	}
	s := readConfigToml(t, dir)
	if strings.Count(s, "metrics_exporter") != 1 || strings.Contains(s, "[otel.metrics_exporter") {
		t.Fatalf("not healed:\n%s", s)
	}
}

func readConfigToml(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	return string(data)
}
