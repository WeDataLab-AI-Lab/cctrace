package codexconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteOtelBlock_NewFile(t *testing.T) {
	dir := t.TempDir()
	if err := WriteOtelBlock(dir, "http://localhost:4317", "tok-abc", ""); err != nil {
		t.Fatalf("WriteOtelBlock: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "[otel]") {
		t.Error("expected [otel] section")
	}
	if !strings.Contains(content, "metrics_exporter") {
		t.Error("expected metrics_exporter")
	}
	if !strings.Contains(content, "http://localhost:4318/v1/metrics") {
		t.Error("expected metrics endpoint URL")
	}
	if !strings.Contains(content, "tok-abc") {
		t.Error("expected auth token")
	}
}

func TestWriteOtelBlock_ExistingFile_NoOtel(t *testing.T) {
	dir := t.TempDir()
	existing := `model = "gpt-5"
personality = "pragmatic"
`
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte(existing), 0644)

	if err := WriteOtelBlock(dir, "http://server:4317", "tok-xyz", ""); err != nil {
		t.Fatalf("WriteOtelBlock: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	content := string(data)
	if !strings.Contains(content, "model = \"gpt-5\"") {
		t.Error("existing config should be preserved")
	}
	if !strings.Contains(content, "[otel]") {
		t.Error("expected [otel] section appended")
	}
	if !strings.Contains(content, "http://server:4318/v1/metrics") {
		t.Error("expected metrics endpoint")
	}
}

func TestWriteOtelBlock_ExistingFile_WithOtel(t *testing.T) {
	dir := t.TempDir()
	existing := `model = "gpt-5"

[otel]
endpoint = "http://old:4317"
headers = {Authorization = "Bearer old-token"}
`
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte(existing), 0644)

	if err := WriteOtelBlock(dir, "http://new:4317", "new-token", ""); err != nil {
		t.Fatalf("WriteOtelBlock: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	content := string(data)
	if strings.Contains(content, "old-token") {
		t.Error("old token should be replaced")
	}
	if !strings.Contains(content, "http://new:4318/v1/metrics") {
		t.Error("expected new metrics endpoint")
	}
	if !strings.Contains(content, "new-token") {
		t.Error("expected new token")
	}
	// Should not duplicate [otel] section
	if strings.Count(content, "[otel]") != 1 {
		t.Errorf("expected exactly 1 [otel] section, got %d", strings.Count(content, "[otel]"))
	}
}

func TestWriteOtelBlock_NoToken(t *testing.T) {
	dir := t.TempDir()
	if err := WriteOtelBlock(dir, "http://localhost:4317", "", ""); err != nil {
		t.Fatalf("WriteOtelBlock: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	content := string(data)
	if !strings.Contains(content, "[otel]") {
		t.Error("expected [otel] section even without token")
	}
}

func TestWriteOtelBlock_EscapesAuthTokenAsTomlString(t *testing.T) {
	dir := t.TempDir()
	token := `tok"with\special`
	if err := WriteOtelBlock(dir, "http://localhost:4317", token, ""); err != nil {
		t.Fatalf("WriteOtelBlock: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	want := "Authorization = " + fmt.Sprintf("%q", "Bearer "+token)
	if !strings.Contains(content, want) {
		t.Fatalf("auth token should be TOML-escaped as %q, got:\n%s", want, content)
	}
}

func TestEnsureOtelBlockReportsUnreadableConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "config.toml"), 0755); err != nil {
		t.Fatalf("mkdir config.toml as directory: %v", err)
	}

	changed, err := EnsureOtelBlock(dir, "http://localhost:4317", "tok", "")
	if err == nil {
		t.Fatal("expected unreadable config error")
	}
	if changed {
		t.Fatal("unreadable config must not be reported as changed")
	}
}

func TestReadConfig_NoFile(t *testing.T) {
	dir := t.TempDir()
	cfg, err := ReadConfig(dir)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if cfg.OtelEndpoint != "" {
		t.Errorf("expected empty endpoint, got %q", cfg.OtelEndpoint)
	}
}

func TestReadConfig_WithOtel(t *testing.T) {
	dir := t.TempDir()
	content := `model = "gpt-5"

[otel]
endpoint = "http://localhost:4317"
headers = {Authorization = "Bearer mytoken"}
`
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0644)

	cfg, err := ReadConfig(dir)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if cfg.OtelEndpoint != "http://localhost:4317" {
		t.Errorf("OtelEndpoint = %q, want http://localhost:4317", cfg.OtelEndpoint)
	}
	if !cfg.HasOtel {
		t.Error("expected HasOtel = true")
	}
}

func TestReadConfig_WithMetricsExporter(t *testing.T) {
	dir := t.TempDir()
	content := `model = "gpt-5"

[otel]
metrics_exporter = { otlp-http = { endpoint = "http://localhost:4318/v1/metrics", protocol = "binary" } }
`
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0644)

	cfg, err := ReadConfig(dir)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if !cfg.HasOtel {
		t.Error("expected HasOtel true")
	}
	if cfg.OtelEndpoint != "http://localhost:4318/v1/metrics" {
		t.Errorf("expected metrics endpoint, got %q", cfg.OtelEndpoint)
	}
}

func TestCodexMetricsEndpoint(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"grpc default", "http://localhost:4317", "http://localhost:4318/v1/metrics"},
		{"deployed grpc", "http://trace.example.com:14317", "http://trace.example.com:14318/v1/metrics"},
		// The TLS overlay (deploy/docker-compose.tls.yml) serves OTLP gRPC on 5317
		// and OTLP HTTP on 5318, so an https gRPC endpoint maps like the others.
		{"TLS overlay grpc", "https://trace.example.com:5317", "https://trace.example.com:5318/v1/metrics"},
		{"http already", "http://trace.example.com:14318/v1/metrics", "http://trace.example.com:14318/v1/metrics"},
		{"logs path", "http://trace.example.com:14318/v1/logs", "http://trace.example.com:14318/v1/metrics"},
		{"custom http port", "https://trace.example.com:443", "https://trace.example.com:443/v1/metrics"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexMetricsEndpoint(tt.in); got != tt.want {
				t.Fatalf("codexMetricsEndpoint(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// A root-level otel key sits among other root keys. Writing the [otel] table in
// its place would pull every root key after it into the table -- `model` would
// become `otel.model`. The table goes where tables may go instead.
func TestReplaceRootOtelKeyKeepsLaterRootKeysAtRoot(t *testing.T) {
	existing := "otel.endpoint = \"http://localhost:4318\"\nmodel = \"gpt-5\"\n\n[profiles.fast]\nmodel = \"gpt-5-mini\"\n"
	got := replaceOrAppendOtelSection(existing, "[otel]\nNEW = 1\n")
	want := "model = \"gpt-5\"\n\n[profiles.fast]\nmodel = \"gpt-5-mini\"\n\n[otel]\nNEW = 1\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if again := replaceOrAppendOtelSection(got, "[otel]\nNEW = 1\n"); again != got {
		t.Fatalf("not idempotent:\n%s", again)
	}
}

// A root otel key whose value spans lines (a multi-line array, inline table or
// string) cannot be removed line by line without leaving its continuation lines
// behind as broken TOML. Such a config is left untouched and the caller is told.
func TestEnsureOtelBlockLeavesAMultilineRootOtelKeyAlone(t *testing.T) {
	for name, config := range map[string]string{
		"array":        "otel.endpoint = \"http://localhost:4317\"\notel.tags = [\n  \"a\",\n  \"b\",\n]\nmodel = \"gpt-5\"\n",
		"string":       "otel.note = \"\"\"\nfirst\n[not-a-table]\n\"\"\"\nmodel = \"gpt-5\"\n",
		"inline table": "otel.exporter = { otlp-http = {\n  endpoint = \"http://localhost:4318\" } }\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			changed, err := EnsureOtelBlock(dir, "http://localhost:4317", "tok", "")
			if !errors.Is(err, ErrMultilineRootOtelKey) || changed {
				t.Fatalf("EnsureOtelBlock = %v, %v; want ErrMultilineRootOtelKey", changed, err)
			}
			if data, _ := os.ReadFile(filepath.Join(dir, "config.toml")); string(data) != config {
				t.Fatalf("config rewritten:\n%s", data)
			}
		})
	}

	// A single-line root otel key is still replaced.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("otel.tags = [\"a\", \"b\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if changed, err := EnsureOtelBlock(dir, "http://localhost:4317", "tok", ""); err != nil || !changed {
		t.Fatalf("single-line root key: EnsureOtelBlock = %v, %v; want rewritten", changed, err)
	}
}

// Comment lines between the otel body and the next header introduce that next
// section, not the otel one. Replacing the otel block must keep them.
func TestReplaceOtelSectionKeepsCommentsOfTheNextSection(t *testing.T) {
	existing := `model = "gpt-5"

[otel]
endpoint = "http://localhost:4317"
# inside otel, goes with it
environment = "dev"

# --- profiles ---
# second line
[profiles.fast]
model = "gpt-5-mini"
`
	got := replaceOrAppendOtelSection(existing, "[otel]\nNEW = 1\n")
	want := `model = "gpt-5"

[otel]
NEW = 1

# --- profiles ---
# second line
[profiles.fast]
model = "gpt-5-mini"
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if again := replaceOrAppendOtelSection(got, "[otel]\nNEW = 1\n"); again != got {
		t.Fatalf("not idempotent:\n%s", again)
	}
}

// OTLPHTTPEndpoint is the port half of codexMetricsEndpoint: the gRPC port
// becomes the OTLP/HTTP one and everything else is kept, path included.
func TestOTLPHTTPEndpoint(t *testing.T) {
	for in, want := range map[string]string{
		"http://localhost:4317":                 "http://localhost:4318",
		"https://trace.example.com:14317":       "https://trace.example.com:14318",
		"https://trace.example.com:443":         "https://trace.example.com:443",
		"https://trace.example.com:4317/prefix": "https://trace.example.com:4318/prefix",
		"not a url":                             "not a url",
	} {
		if got := OTLPHTTPEndpoint(in); got != want {
			t.Errorf("OTLPHTTPEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}
