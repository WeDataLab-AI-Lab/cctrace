package codexconfig

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEnsureOtelBlockProducesParsableTomlForPathologicalOtelShapes(t *testing.T) {
	tests := []struct {
		name     string
		existing string
	}{
		{
			name: "spaced table header with trailing comment",
			existing: `model = "gpt-5"

[ otel ] # user note
endpoint = "http://old:4317"

[profiles.default]
model = "gpt-5-codex"
`,
		},
		{
			name: "array table named otel",
			existing: `model = "gpt-5"

[[otel]]
endpoint = "http://old:4317"

[projects."/tmp/work"]
trust_level = "trusted"
`,
		},
		{
			name: "root dotted otel keys",
			existing: `model = "gpt-5"
otel.metrics_exporter.otlp-http.endpoint = "http://old:4318/v1/metrics"
otel.metrics_exporter.otlp-http.protocol = "binary"
otel.metrics_exporter.otlp-http.headers.Authorization = "Bearer old"

[notify]
enabled = true
`,
		},
		{
			name: "root inline otel table",
			existing: `model = "gpt-5"
otel = { metrics_exporter = { otlp-http = { endpoint = "http://old:4318/v1/metrics" } } }

[tools]
enabled = true
`,
		},
		{
			name: "comment contains multiline string delimiter before otel",
			existing: `model = "gpt-5"
# User note mentioning """ should not hide the real table below.

[otel]
endpoint = "http://old:4317"

[tools]
enabled = true
`,
		},
		{
			name: "quoted otel table key",
			existing: `model = "gpt-5"

["otel"."metrics_exporter".otlp-http]
endpoint = "http://old:4318/v1/metrics"
protocol = "binary"

[tools]
enabled = true
`,
		},
		{
			name: "escaped quoted otel table key",
			existing: `model = "gpt-5"

["ot\u0065l"]
endpoint = "http://old:4317"

[tools]
enabled = true
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(tt.existing), 0644); err != nil {
				t.Fatalf("seed config: %v", err)
			}

			changed, err := EnsureOtelBlock(dir, "http://new:14317", `tok"with\chars`)
			if err != nil {
				t.Fatalf("EnsureOtelBlock: %v", err)
			}
			if !changed {
				t.Fatal("pathological otel form must be rewritten")
			}
			got := readConfigToml(t, dir)
			requirePythonTomlParse(t, got)
			if strings.Count(got, "metrics_exporter") != 1 {
				t.Fatalf("metrics_exporter count = %d, want 1\n%s", strings.Count(got, "metrics_exporter"), got)
			}
			if strings.Contains(got, "old") {
				t.Fatalf("old otel value survived:\n%s", got)
			}
			if !strings.Contains(got, `Authorization = "Bearer tok\"with\\chars"`) {
				t.Fatalf("auth token was not TOML-escaped:\n%s", got)
			}
		})
	}
}

func TestEnsureOtelBlockPreservesNonRootOtelKeys(t *testing.T) {
	dir := t.TempDir()
	existing := `model = "gpt-5"

[projects."/tmp/work"]
otel = "project-local metadata, not the root otel config"
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(existing), 0644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	if _, err := EnsureOtelBlock(dir, "http://new:14317", "tok"); err != nil {
		t.Fatalf("EnsureOtelBlock: %v", err)
	}
	got := readConfigToml(t, dir)
	requirePythonTomlParse(t, got)
	if !strings.Contains(got, `otel = "project-local metadata, not the root otel config"`) {
		t.Fatalf("non-root otel key was removed:\n%s", got)
	}
	if gotCount := countOtelTableHeaders(got); gotCount != 1 {
		t.Fatalf("otel table header count = %d, want 1\n%s", gotCount, got)
	}
}

func TestEnsureOtelBlockPreservesLiteralDottedOtelTable(t *testing.T) {
	dir := t.TempDir()
	existing := `model = "gpt-5"

["otel.metrics_exporter"]
note = "literal dotted table, not cctrace otel config"
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(existing), 0644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	if _, err := EnsureOtelBlock(dir, "http://new:14317", "tok"); err != nil {
		t.Fatalf("EnsureOtelBlock: %v", err)
	}
	got := readConfigToml(t, dir)
	requirePythonTomlParse(t, got)
	if !strings.Contains(got, `["otel.metrics_exporter"]`) || !strings.Contains(got, "literal dotted table") {
		t.Fatalf("literal dotted table was removed:\n%s", got)
	}
	if gotCount := countOtelTableHeaders(got); gotCount != 1 {
		t.Fatalf("managed otel table header count = %d, want 1\n%s", gotCount, got)
	}
}

func TestEnsureOtelBlockPreservesLiteralDottedOtelRootKey(t *testing.T) {
	dir := t.TempDir()
	existing := `model = "gpt-5"
"otel.metrics_exporter" = "literal dotted key"
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(existing), 0644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	if _, err := EnsureOtelBlock(dir, "http://new:14317", "tok"); err != nil {
		t.Fatalf("EnsureOtelBlock: %v", err)
	}
	got := readConfigToml(t, dir)
	requirePythonTomlParse(t, got)
	if !strings.Contains(got, `"otel.metrics_exporter" = "literal dotted key"`) {
		t.Fatalf("literal dotted root key was removed:\n%s", got)
	}
}

func TestEnsureOtelBlockCreatesConfigWithPrivateMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX permission bits")
	}
	dir := t.TempDir()

	changed, err := EnsureOtelBlock(dir, "http://new:14317", "tok")
	if err != nil {
		t.Fatalf("EnsureOtelBlock: %v", err)
	}
	if !changed {
		t.Fatal("missing config must create managed otel block")
	}
	info, err := os.Stat(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("config mode = %o, want 0600", perm)
	}
}

func TestReadConfigRecognizesSpacedCommentedOtelHeader(t *testing.T) {
	dir := t.TempDir()
	existing := `[ otel ] # generated by another tool
endpoint = "http://localhost:4318/v1/metrics"
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(existing), 0644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	cfg, err := ReadConfig(dir)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if !cfg.HasOtel {
		t.Fatal("spaced/commented otel header was not detected")
	}
	if cfg.OtelEndpoint != "http://localhost:4318/v1/metrics" {
		t.Fatalf("endpoint = %q, want http://localhost:4318/v1/metrics", cfg.OtelEndpoint)
	}
}

func TestEnsureOtelBlockPreservesMultilineStringsContainingOtelHeaderText(t *testing.T) {
	dir := t.TempDir()
	existing := `model = "gpt-5"
notes = """
This is documentation, not a TOML table:
[otel]
endpoint = "do not delete"
"""

[profiles.default]
model = "gpt-5-codex"
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(existing), 0644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	changed, err := EnsureOtelBlock(dir, "http://new:14317", "tok")
	if err != nil {
		t.Fatalf("EnsureOtelBlock: %v", err)
	}
	if !changed {
		t.Fatal("missing real otel section must append a managed block")
	}
	got := readConfigToml(t, dir)
	requirePythonTomlParse(t, got)
	if !strings.Contains(got, "This is documentation, not a TOML table") || !strings.Contains(got, `endpoint = "do not delete"`) {
		t.Fatalf("multiline string content was not preserved:\n%s", got)
	}
	if gotCount := countOtelTableHeaders(got); gotCount != 1 {
		t.Fatalf("otel table header count = %d, want 1\n%s", gotCount, got)
	}
}

func requirePythonTomlParse(t *testing.T, content string) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available for TOML parser smoke test")
	}
	cmd := exec.Command("python3", "-c", "import sys, tomllib; tomllib.loads(sys.stdin.read())")
	cmd.Stdin = strings.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tomllib rejected generated config: %v\n%s\n--- config ---\n%s", err, out, content)
	}
}

func countOtelTableHeaders(content string) int {
	count := 0
	var state tomlStringState
	for _, line := range strings.Split(content, "\n") {
		if state.inMultiline() {
			state.update(line)
			continue
		}
		if name, ok := tomlHeaderName(strings.TrimSpace(line)); ok && isOtelTableName(name) {
			count++
			continue
		}
		state.update(line)
	}
	return count
}
