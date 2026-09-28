package codexconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAuth(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readConfigText(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Codex metrics carry no session and no billing account, so the server cannot
// apply a billing-account exclusion to them (#715). The exporter header names the
// account, read from the same auth.json the JSONL syncer stamps records with.
func TestEnsureOtelBlockSendsCodexAccountHeader(t *testing.T) {
	dir := t.TempDir()
	writeAuth(t, dir, `{"tokens":{"account_id":"acct-1","access_token":"secret-access","refresh_token":"secret-refresh"}}`)

	if _, err := EnsureOtelBlock(dir, "http://localhost:4317", "tok"); err != nil {
		t.Fatalf("EnsureOtelBlock: %v", err)
	}
	content := readConfigText(t, dir)
	if !strings.Contains(content, `X-Cctrace-Codex-Account = "acct-1"`) {
		t.Fatalf("account header missing:\n%s", content)
	}
	if strings.Contains(content, "secret-") {
		t.Fatalf("a token value reached the config:\n%s", content)
	}

	// An account switch rewrites the block on the next sync.
	writeAuth(t, dir, `{"tokens":{"account_id":"acct-2"}}`)
	changed, err := EnsureOtelBlock(dir, "http://localhost:4317", "tok")
	if err != nil {
		t.Fatalf("EnsureOtelBlock: %v", err)
	}
	if !changed || !strings.Contains(readConfigText(t, dir), `X-Cctrace-Codex-Account = "acct-2"`) {
		t.Fatalf("account switch not written (changed=%v):\n%s", changed, readConfigText(t, dir))
	}
	if changed, _ := EnsureOtelBlock(dir, "http://localhost:4317", "tok"); changed {
		t.Fatal("unchanged account rewrote the config")
	}
}

// An unknown account is left out rather than sent empty: an empty value would
// read as "this account" to anything that compares it.
func TestEnsureOtelBlockOmitsAccountHeaderWhenUnknown(t *testing.T) {
	for name, auth := range map[string]string{"no auth.json": "", "api key login": `{"OPENAI_API_KEY":"sk"}`} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if auth != "" {
				writeAuth(t, dir, auth)
			}
			if _, err := EnsureOtelBlock(dir, "http://localhost:4317", "tok"); err != nil {
				t.Fatalf("EnsureOtelBlock: %v", err)
			}
			if content := readConfigText(t, dir); strings.Contains(content, "X-Cctrace-Codex-Account") {
				t.Fatalf("header written without an account:\n%s", content)
			}
		})
	}
}

// An extra Codex home is healed only when it already has an otel section (#753):
// cctrace fixes what a person pointed at it, but never opts a home in on its own.
func TestHealExistingOtelBlockLeavesHomesWithoutOtelAlone(t *testing.T) {
	for name, config := range map[string]string{
		"no config.toml":  "",
		"no otel section": "model = \"gpt-5\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if config != "" {
				if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := HealExistingOtelBlock(dir, ownEndpoint, "tok", true)
			if err != nil || got != HealNoOtel {
				t.Fatalf("HealExistingOtelBlock = %v, %v; want HealNoOtel", got, err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "config.toml"))
			if config == "" {
				if !os.IsNotExist(err) {
					t.Fatalf("config.toml created in a home without one (err=%v)", err)
				}
				return
			}
			if string(data) != config {
				t.Fatalf("config rewritten:\n%s", data)
			}
		})
	}
}

// ownEndpoint is the cctrace OTEL endpoint the heal tests run as. Its metrics
// endpoint is cctrace.example.com:4318, which is what "owned" is matched against.
const ownEndpoint = "http://cctrace.example.com:4317"

// The legacy `endpoint = "..."` form sends no metrics at all; healing moves it to
// the current metrics_exporter form with that home's account header.
func TestHealExistingOtelBlockMigratesLegacyEndpointForm(t *testing.T) {
	for name, legacy := range map[string]string{
		"table":     "model = \"gpt-5\"\n\n[otel]\nendpoint = \"http://cctrace.example.com:4317\"\n",
		"no scheme": "model = \"gpt-5\"\n\n[otel]\nendpoint = \"cctrace.example.com:4317\"\n",
		"root key":  "otel.endpoint = \"http://cctrace.example.com:4318\"\nmodel = \"gpt-5\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeAuth(t, dir, `{"tokens":{"account_id":"acct-2"}}`)
			if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(legacy), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := HealExistingOtelBlock(dir, ownEndpoint, "tok", true)
			if err != nil || got != HealWritten {
				t.Fatalf("HealExistingOtelBlock = %v, %v; want HealWritten", got, err)
			}
			content := readConfigText(t, dir)
			if strings.Contains(content, "\nendpoint =") || strings.Contains(content, "otel.endpoint") {
				t.Fatalf("legacy endpoint kept:\n%s", content)
			}
			for _, want := range []string{`model = "gpt-5"`, "http://cctrace.example.com:4318/v1/metrics", `X-Cctrace-Codex-Account = "acct-2"`} {
				if !strings.Contains(content, want) {
					t.Fatalf("missing %q:\n%s", want, content)
				}
			}

			// Every sync runs this; the second run must find nothing to do.
			if got, err := HealExistingOtelBlock(dir, ownEndpoint, "tok", true); err != nil || got != HealUnchanged {
				t.Fatalf("second run = %v, %v; want HealUnchanged", got, err)
			}
			if again := readConfigText(t, dir); again != content {
				t.Fatalf("second run changed the config:\n%s", again)
			}
		})
	}
}

// An extra home whose otel section sends to some other collector is not cctrace's
// to rewrite: healing it would redirect that person's metrics to cctrace, hand the
// cctrace token to their config and drop their headers. Legacy and current forms
// alike, and a cctrace header alone does not make another server's block ours.
func TestHealExistingOtelBlockLeavesAForeignCollectorAlone(t *testing.T) {
	for name, config := range map[string]string{
		"current":        "[otel]\nmetrics_exporter = { otlp-http = { endpoint = \"https://collector.example.com:4318/v1/metrics\", protocol = \"binary\", headers = { x-api-key = \"theirs\" } } }\n# keep me\n",
		"legacy":         "[otel]\nendpoint = \"http://collector.example.com:4317\"\n",
		"cctrace header": "[otel]\nmetrics_exporter = { otlp-http = { endpoint = \"http://other.example.com:4318/v1/metrics\", protocol = \"binary\", headers = { X-Cctrace-Codex-Account = \"acct-2\" } } }\n",
		"no endpoint":    "[otel]\nenvironment = \"dev\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := HealExistingOtelBlock(dir, ownEndpoint, "tok", true)
			if err != nil || got != HealForeign {
				t.Fatalf("HealExistingOtelBlock = %v, %v; want HealForeign", got, err)
			}
			if content := readConfigText(t, dir); content != config {
				t.Fatalf("foreign config rewritten:\n%s", content)
			}
		})
	}
}

// Named profiles on the same server share the process CODEX_HOME, so each sync
// would rewrite the other's token into an extra home (#753 review). The home
// keeps the token it has; only the form, endpoint and account are healed.
func TestHealExistingOtelBlockKeepsTheHomesOwnToken(t *testing.T) {
	dir := t.TempDir()
	writeAuth(t, dir, `{"tokens":{"account_id":"acct-2"}}`)
	current := buildOtelBlock(codexMetricsEndpoint(ownEndpoint), "their-tok", "acct-2")
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(current), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := HealExistingOtelBlock(dir, ownEndpoint, "my-tok", true); err != nil || got != HealUnchanged {
		t.Fatalf("token-only difference: HealExistingOtelBlock = %v, %v; want HealUnchanged", got, err)
	}

	writeAuth(t, dir, `{"tokens":{"account_id":"acct-3"}}`)
	if got, err := HealExistingOtelBlock(dir, ownEndpoint, "my-tok", true); err != nil || got != HealWritten {
		t.Fatalf("account switch: HealExistingOtelBlock = %v, %v; want HealWritten", got, err)
	}
	content := readConfigText(t, dir)
	if !strings.Contains(content, "Bearer their-tok") || strings.Contains(content, "my-tok") {
		t.Fatalf("home's token not kept across the rewrite:\n%s", content)
	}
}

// A kept token goes stale when cctrace reissues it, and Codex then gets 401s in
// silence. The caller can see the home's token to say so, and an explicit
// `cctrace init` heals with keepHomeToken=false to replace it.
func TestHealExistingOtelBlockOverwritesTheTokenWhenAskedTo(t *testing.T) {
	dir := t.TempDir()
	current := buildOtelBlock(codexMetricsEndpoint(ownEndpoint), "old-tok", "")
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(current), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := OtelBearerToken(dir); got != "old-tok" {
		t.Fatalf("OtelBearerToken = %q, want old-tok", got)
	}
	if got, err := HealExistingOtelBlock(dir, ownEndpoint, "new-tok", false); err != nil || got != HealWritten {
		t.Fatalf("HealExistingOtelBlock = %v, %v; want HealWritten", got, err)
	}
	if got := OtelBearerToken(dir); got != "new-tok" {
		t.Fatalf("token after explicit heal = %q, want new-tok", got)
	}
}

// A commented-out line is not configuration. An old cctrace endpoint left in a
// comment must not make another collector's block look like cctrace's, and an
// old token in a comment must not be adopted as the home's own.
func TestHealExistingOtelBlockIgnoresCommentedOutLines(t *testing.T) {
	foreign := "[otel]\n# endpoint = \"http://cctrace.example.com:4317\"\nendpoint = \"http://collector.example.com:4317\" # was endpoint = \"http://cctrace.example.com:4317\"\n"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := HealExistingOtelBlock(dir, ownEndpoint, "tok", true); err != nil || got != HealForeign {
		t.Fatalf("commented cctrace endpoint: HealExistingOtelBlock = %v, %v; want HealForeign", got, err)
	}

	owned := "[otel]\nendpoint = \"http://cctrace.example.com:4317\"\n# headers = { Authorization = \"Bearer old-tok\" }\n"
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(owned), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := HealExistingOtelBlock(dir, ownEndpoint, "tok", true); err != nil || got != HealWritten {
		t.Fatalf("owned legacy block: HealExistingOtelBlock = %v, %v; want HealWritten", got, err)
	}
	if content := readConfigText(t, dir); strings.Contains(content, "old-tok") || !strings.Contains(content, "Bearer tok") {
		t.Fatalf("commented-out token adopted:\n%s", content)
	}
}
