package codexappserver

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cctrace/internal/airuntime"
)

func TestPrepareHomeWritesModelEffortAndToolLimits(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ai-codex-home")
	if err := PrepareHome(RuntimeConfig{Home: home, Model: "gpt-6-astra", ReasoningEffort: "low"}); err != nil {
		t.Fatalf("PrepareHome: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	want := "model = \"gpt-6-astra\"\nmodel_reasoning_effort = \"low\"\n" + toolLimitsConfig
	if string(got) != want {
		t.Fatalf("config.toml = %q, want %q", got, want)
	}
	info, err := os.Stat(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("config.toml mode = %v, want no group/other access", perm)
	}
}

// Rewriting at boot replaces the previous file, including settings someone
// added by hand (otel exporters, hooks, MCP servers) that must not leak in.
func TestPrepareHomeReplacesExistingConfig(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("[otel]\nmetrics_exporter = \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PrepareHome(RuntimeConfig{Home: home, Model: "m1"}); err != nil {
		t.Fatalf("PrepareHome: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "model = \"m1\"\n"+toolLimitsConfig {
		t.Fatalf("config.toml = %q", got)
	}
}

// The tool limits that no command-line flag reaches: the model catalog turns on
// sub-agents and code mode's nested skills and clock tools regardless of
// features (experiment §6.3).
func TestPrepareHomeToolLimits(t *testing.T) {
	want := "\n[features.code_mode]\nexcluded_tool_namespaces = [\"skills\", \"clock\"]\n\n[agents]\nenabled = false\n"
	if toolLimitsConfig != want {
		t.Fatalf("toolLimitsConfig = %q, want %q", toolLimitsConfig, want)
	}
}

func readAuth(t *testing.T, home string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		t.Fatalf("read auth.json: %v", err)
	}
	var auth map[string]string
	if err := json.Unmarshal(raw, &auth); err != nil {
		t.Fatalf("auth.json is not a JSON object of strings: %v", err)
	}
	return auth
}

// app-server ignores CODEX_API_KEY (it loads auth with the env var disabled),
// so the key has to be in the home the way `codex login --with-api-key` writes it.
func TestPrepareHomeWritesAPIKeyLogin(t *testing.T) {
	home := t.TempDir()
	if err := PrepareHome(RuntimeConfig{Home: home, APIKey: "sk-one"}); err != nil {
		t.Fatalf("PrepareHome: %v", err)
	}
	if got := readAuth(t, home); got["auth_mode"] != "apikey" || got["OPENAI_API_KEY"] != "sk-one" || len(got) != 2 {
		t.Fatalf("auth.json = %v", got)
	}
	info, err := os.Stat(filepath.Join(home, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("auth.json mode = %v, want no group/other access", perm)
	}

	// A rotated key replaces the one written before.
	if err := PrepareHome(RuntimeConfig{Home: home, APIKey: "sk-two"}); err != nil {
		t.Fatalf("PrepareHome with a new key: %v", err)
	}
	if got := readAuth(t, home); got["OPENAI_API_KEY"] != "sk-two" {
		t.Fatalf("auth.json after rotation = %v", got)
	}
}

// A ChatGPT login in the home is someone's sign-in, not ours to replace.
func TestPrepareHomeKeepsChatGPTLoginWhenKeyIsSet(t *testing.T) {
	home := t.TempDir()
	login := []byte(`{"auth_mode":"chatgpt","tokens":{"id_token":"x"}}`)
	if err := os.WriteFile(filepath.Join(home, "auth.json"), login, 0o600); err != nil {
		t.Fatal(err)
	}
	err := PrepareHome(RuntimeConfig{Home: home, APIKey: "sk-one"})
	if err == nil {
		t.Fatal("PrepareHome replaced a ChatGPT login with an API key")
	}
	if strings.Contains(err.Error(), "sk-one") {
		t.Fatalf("error leaks the key: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(home, "auth.json"))
	if string(got) != string(login) {
		t.Fatalf("auth.json changed to %q", got)
	}
}

// Unsetting the key must stop runs from using the one written earlier; a
// ChatGPT login stays.
func TestPrepareHomeRemovesAPIKeyLoginWhenKeyIsUnset(t *testing.T) {
	home := t.TempDir()
	if err := PrepareHome(RuntimeConfig{Home: home, APIKey: "sk-one"}); err != nil {
		t.Fatal(err)
	}
	if err := PrepareHome(RuntimeConfig{Home: home}); err != nil {
		t.Fatalf("PrepareHome without a key: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("API key auth.json still present (stat err %v)", err)
	}

	login := []byte(`{"auth_mode":"chatgpt"}`)
	if err := os.WriteFile(filepath.Join(home, "auth.json"), login, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PrepareHome(RuntimeConfig{Home: home}); err != nil {
		t.Fatalf("PrepareHome without a key: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(home, "auth.json")); string(got) != string(login) {
		t.Fatalf("ChatGPT auth.json changed to %q", got)
	}
}

// A key an admin registered from the screen is not the environment's to remove:
// booting without CODEX_API_KEY must leave it, or every restart logs out.
func TestPrepareHomeKeepsAPIKeyLoginNotWrittenFromEnv(t *testing.T) {
	home := t.TempDir()
	login := []byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-screen"}`)
	if err := os.WriteFile(filepath.Join(home, "auth.json"), login, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PrepareHome(RuntimeConfig{Home: home}); err != nil {
		t.Fatalf("PrepareHome without a key: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(home, "auth.json")); string(got) != string(login) {
		t.Fatalf("screen-registered auth.json changed to %q", got)
	}
}

// The environment's key is still removed after the screen replaced nothing, and
// a later boot without the key does not trip over the leftover marker.
func TestPrepareHomeRemovesEnvMarkerWithKey(t *testing.T) {
	home := t.TempDir()
	if err := PrepareHome(RuntimeConfig{Home: home, APIKey: "sk-one"}); err != nil {
		t.Fatal(err)
	}
	if err := PrepareHome(RuntimeConfig{Home: home}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, envKeyMarker)); !os.IsNotExist(err) {
		t.Fatalf("marker still present (stat err %v)", err)
	}
}

func TestPrepareHomeRejectsBadInput(t *testing.T) {
	for name, cfg := range map[string]RuntimeConfig{
		"empty home":     {},
		"quote in model": {Home: t.TempDir(), Model: "m\"\n[otel]"},
		"newline effort": {Home: t.TempDir(), ReasoningEffort: "low\nx=1"},
	} {
		if err := PrepareHome(cfg); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestAuthMode(t *testing.T) {
	withAuth := t.TempDir()
	if err := os.WriteFile(filepath.Join(withAuth, "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := t.TempDir()
	keyFile := t.TempDir()
	if err := os.WriteFile(filepath.Join(keyFile, "auth.json"), []byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, home, key, want string
	}{
		// A key registered from the screen is an API key login, not a person's
		// ChatGPT sign-in, even with no CODEX_API_KEY.
		{"api key file", keyFile, "", airuntime.AuthModeAPIKey},
		{"api key wins", withAuth, "sk-test", airuntime.AuthModeAPIKey},
		{"api key without auth.json", empty, "sk-test", airuntime.AuthModeAPIKey},
		{"chatgpt login", withAuth, "", airuntime.AuthModeChatGPT},
		{"nothing", empty, "", airuntime.AuthModeNone},
	}
	for _, tc := range cases {
		if got := AuthMode(tc.home, tc.key); got != tc.want {
			t.Errorf("%s: AuthMode = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A linked auth.json is someone's own login file: a key boot must not write
// through it (nor leave a marker claiming it), and a keyless boot must not
// delete it.
func TestPrepareHomeRefusesLinkedAuthFile(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(t.TempDir(), "auth.json")
	const linked = `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-theirs"}`
	if err := os.WriteFile(target, []byte(linked), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, "auth.json")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := PrepareHome(RuntimeConfig{Home: home, APIKey: "sk-env"}); err == nil {
		t.Fatal("key boot wrote through a linked auth.json")
	}
	if _, err := os.Stat(filepath.Join(home, envKeyMarker)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("marker written for a linked auth.json: %v", err)
	}

	if err := os.WriteFile(filepath.Join(home, envKeyMarker), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PrepareHome(RuntimeConfig{Home: home}); err != nil {
		t.Fatalf("keyless boot: %v", err)
	}
	if info, err := os.Lstat(filepath.Join(home, "auth.json")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("linked auth.json removed: %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != linked {
		t.Fatalf("linked login changed: %s", b)
	}
}
