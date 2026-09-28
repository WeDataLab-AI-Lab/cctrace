package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/aireport"
	"cctrace/internal/airuntime"
)

func clearAIEnv(t *testing.T) {
	for _, k := range []string{"CCTRACE_AI_ENABLED", "CCTRACE_AI_RUNTIME", "CCTRACE_AI_CODEX_HOME", "CCTRACE_AI_MODEL", "CCTRACE_AI_REASONING_EFFORT", "CODEX_API_KEY",
		"CCTRACE_AI_OPENAI_MODEL", "CCTRACE_AI_CLAUDE_MODEL", "CCTRACE_AI_OPENAI_API_KEY", "CCTRACE_AI_ANTHROPIC_API_KEY",
		"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "CCTRACE_SECRETS_KEY", "JWT_SECRET", "CCTRACE_AI_MAX_CONCURRENT", "CCTRACE_AI_WALL_CLOCK"} {
		t.Setenv(k, "")
	}
}

func TestAIEnvDefaults(t *testing.T) {
	clearAIEnv(t)
	t.Setenv("JWT_SECRET", "jwt")
	env := aiEnvFromEnv("/data")
	want := aiEnv{CodexHome: filepath.Join("/data", "ai-codex-home"), SecretsKey: "jwt", SecretsSource: aireport.KeySourceJWTSecret, MaxConcurrent: 2, WallClock: 8 * time.Minute}
	if env != want {
		t.Fatalf("env = %+v, want %+v", env, want)
	}
}

func TestAIEnvParsesValues(t *testing.T) {
	clearAIEnv(t)
	t.Setenv("CCTRACE_AI_RUNTIME", "claude-api")
	t.Setenv("CCTRACE_AI_CODEX_HOME", "/srv/codex")
	t.Setenv("CCTRACE_AI_MODEL", "gpt-5.3-codex")
	t.Setenv("CCTRACE_AI_REASONING_EFFORT", "low")
	t.Setenv("CODEX_API_KEY", "sk-test")
	t.Setenv("CCTRACE_AI_OPENAI_MODEL", "gpt-openai")
	t.Setenv("CCTRACE_AI_CLAUDE_MODEL", "claude-opus-5")
	t.Setenv("CCTRACE_AI_OPENAI_API_KEY", "sk-openai")
	t.Setenv("CCTRACE_AI_ANTHROPIC_API_KEY", "sk-ant")
	t.Setenv("JWT_SECRET", "jwt")
	t.Setenv("CCTRACE_SECRETS_KEY", "dedicated")
	t.Setenv("CCTRACE_AI_MAX_CONCURRENT", "4")
	t.Setenv("CCTRACE_AI_WALL_CLOCK", "3m")
	env := aiEnvFromEnv("/data")
	want := aiEnv{Runtime: "claude-api", CodexHome: "/srv/codex", Model: "gpt-5.3-codex", ReasoningEffort: "low", APIKey: "sk-test",
		OpenAIModel: "gpt-openai", ClaudeModel: "claude-opus-5", OpenAIAPIKey: "sk-openai", AnthropicAPIKey: "sk-ant",
		SecretsKey: "dedicated", SecretsSource: aireport.KeySourceSecretsKey, MaxConcurrent: 4, WallClock: 3 * time.Minute}
	if env != want {
		t.Fatalf("env = %+v, want %+v", env, want)
	}

	t.Setenv("CCTRACE_AI_MAX_CONCURRENT", "0")
	t.Setenv("CCTRACE_AI_WALL_CLOCK", "soon")
	env = aiEnvFromEnv("/data")
	if env.MaxConcurrent != 2 || env.WallClock != 8*time.Minute {
		t.Fatalf("unusable values must keep defaults: %+v", env)
	}
}

// CCTRACE_AI_ENABLED is unset, true or false; any other value is off, so a typo
// never turns reports on.
func TestAIEnvEnabled(t *testing.T) {
	clearAIEnv(t)
	if env := aiEnvFromEnv("/data"); env.Enabled != nil {
		t.Fatalf("unset = %v", *env.Enabled)
	}
	for raw, want := range map[string]bool{"true": true, "1": true, " TRUE ": true, "false": false, "0": false, "yes-please": false} {
		t.Setenv("CCTRACE_AI_ENABLED", raw)
		if env := aiEnvFromEnv("/data"); env.Enabled == nil || *env.Enabled != want {
			t.Errorf("%q = %v", raw, env.Enabled)
		}
	}
}

// The service is on only when the environment says so: CCTRACE_AI_ENABLED, or
// a set CCTRACE_AI_RUNTIME as before the switch existed.
func TestNewAIReportServiceEnablement(t *testing.T) {
	stubCodex(t, false, nil)
	on, off := true, false
	for _, c := range []struct {
		name string
		env  aiEnv
		want bool
	}{
		{"nothing set", aiEnv{}, false},
		{"runtime set", aiEnv{Runtime: aireport.RuntimeClaude}, true},
		{"runtime set, switched off", aiEnv{Runtime: aireport.RuntimeClaude, Enabled: &off}, false},
		{"switched on", aiEnv{Enabled: &on}, true},
	} {
		e, err := newAIReportService(aireport.NewMemStore(), c.env).Enablement(t.Context())
		if err != nil || e.Enabled != c.want {
			t.Errorf("%s = %+v, %v", c.name, e, err)
		}
	}
}

// A key a deployment carries for something else is never the report's key.
func TestAIEnvIgnoresStandardProviderKeys(t *testing.T) {
	clearAIEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-other")
	t.Setenv("ANTHROPIC_API_KEY", "sk-other")
	if env := aiEnvFromEnv("/data"); env.OpenAIAPIKey != "" || env.AnthropicAPIKey != "" {
		t.Fatalf("env = %+v", env)
	}
}

func stubCodex(t *testing.T, found bool, rt airuntime.Runtime) {
	oldFactory, oldLook := newCodexRuntime, lookCodexCLI
	t.Cleanup(func() { newCodexRuntime, lookCodexCLI = oldFactory, oldLook })
	newCodexRuntime = func(aiEnv) airuntime.Runtime { return rt }
	lookCodexCLI = func(string) (string, error) {
		if found {
			return "/usr/bin/codex", nil
		}
		return "", errors.New("not found")
	}
}

func runtimeKeys(rts []airuntime.Runtime) []string {
	var keys []string
	for _, rt := range rts {
		keys = append(keys, rt.Info().Key)
	}
	return keys
}

// Every runtime this server can run is built at boot, Codex first, whatever
// CCTRACE_AI_RUNTIME says; one that cannot be built says why.
func TestAIRuntimesFromEnv(t *testing.T) {
	kr := aireport.NewKeyring(aireport.NewMemStore(), "", nil)
	fake := &airuntime.FakeRuntime{InfoValue: airuntime.Info{Key: aireport.DefaultRuntimeKey}}

	// NVIDIA's endpoint is fixed. LiteLLM is self-hosted and its address is the
	// one setting an admin types in, so it is built whatever the environment
	// says: the address can arrive later through the screen, and Status reports
	// the missing half until one does.
	stubCodex(t, true, fake)
	rts, missing := aiRuntimesFromEnv(aiEnv{}, kr, nil)
	if got := runtimeKeys(rts); len(got) != 5 || got[0] != "codex-app-server" || got[1] != "openai-api" ||
		got[2] != "claude-api" || got[3] != "nvidia-api" || got[4] != "litellm-api" || len(missing) != 0 {
		t.Fatalf("runtimes = %v missing = %v", got, missing)
	}

	// An address in the environment changes nothing about which runtimes exist.
	rts, missing = aiRuntimesFromEnv(aiEnv{LiteLLMBaseURL: "https://litellm.example.test"}, kr, nil)
	if got := runtimeKeys(rts); len(got) != 5 || got[4] != "litellm-api" || len(missing) != 0 {
		t.Fatalf("with LiteLLM address: runtimes = %v missing = %v", got, missing)
	}

	stubCodex(t, false, fake)
	rts, missing = aiRuntimesFromEnv(aiEnv{}, kr, nil)
	if got := runtimeKeys(rts); len(got) != 4 || got[0] != "openai-api" || missing["codex-app-server"] == "" {
		t.Fatalf("no CLI: runtimes = %v missing = %v", got, missing)
	}

	stubCodex(t, true, nil)
	if rts, missing = aiRuntimesFromEnv(aiEnv{}, kr, nil); len(rts) != 4 || missing["codex-app-server"] == "" {
		t.Fatalf("home not prepared: runtimes = %v missing = %v", runtimeKeys(rts), missing)
	}

	newCodexRuntime = nil
	if rts, missing = aiRuntimesFromEnv(aiEnv{}, kr, nil); len(rts) != 4 || missing["codex-app-server"] == "" {
		t.Fatalf("no driver: runtimes = %v missing = %v", runtimeKeys(rts), missing)
	}
}

// A key given in the environment has to reach the runtime, and it travels
// through the keyring the daemon builds inside newAIReportService. Constructing
// a keyring in the test instead would pass whether or not that map carries the
// provider -- which is exactly the wiring that was missing: the screen reported
// the variable as managing the key while the runtime saw none.
func TestEnvKeysReachEveryAPIRuntime(t *testing.T) {
	stubCodex(t, false, nil)
	svc := newAIReportService(aireport.NewMemStore(), aiEnv{
		SecretsKey:      strings.Repeat("s", 32),
		OpenAIAPIKey:    "sk-openai",
		AnthropicAPIKey: "sk-anthropic",
		NVIDIAAPIKey:    "sk-nvidia",
		LiteLLMAPIKey:   "sk-litellm",
		LiteLLMBaseURL:  "https://litellm.example.test",
		MaxConcurrent:   1,
		WallClock:       time.Minute,
	})
	if svc == nil {
		t.Fatal("no service")
	}
	defer svc.Shutdown(context.Background())

	views, _, err := svc.RuntimeViews(t.Context())
	if err != nil {
		t.Fatalf("RuntimeViews: %v", err)
	}
	seen := 0
	for _, v := range views {
		if !v.Built || v.Key == aireport.DefaultRuntimeKey {
			continue
		}
		seen++
		if !v.Status.Configured {
			t.Errorf("%s not configured from its environment key: %+v", v.Key, v.Status)
		}
	}
	if seen != 4 {
		t.Errorf("built API runtimes = %d, want 4 (openai, claude, nvidia, litellm)", seen)
	}
}

// The API runtimes read their keys from the keyring on every call.
func TestAPIRuntimesReadKeyring(t *testing.T) {
	stubCodex(t, false, nil)
	kr := aireport.NewKeyring(aireport.NewMemStore(), "", nil)
	rts, _ := aiRuntimesFromEnv(aiEnv{}, kr, nil)
	for _, rt := range rts {
		if st := rt.Status(context.Background()); st.Configured {
			t.Errorf("%s configured without a key: %+v", rt.Info().Key, st)
		}
	}
}

// The Codex driver is linked: the factory builds a runtime, and the dedicated
// home gets its config.toml before any run.
func TestCodexRuntimeFactoryIsLinked(t *testing.T) {
	if newCodexRuntime == nil {
		t.Fatal("newCodexRuntime is nil; the codexappserver driver is not wired")
	}
	home := t.TempDir()
	rt := newCodexRuntime(aiEnv{CodexHome: home, Model: "gpt-5.3-codex"})
	if rt == nil {
		t.Fatal("want a runtime for codex-app-server")
	}
	if key := rt.Info().Key; key != aireport.DefaultRuntimeKey {
		t.Fatalf("runtime key = %q, want %q", key, aireport.DefaultRuntimeKey)
	}
	if _, err := os.Stat(filepath.Join(home, "config.toml")); err != nil {
		t.Fatalf("config.toml not prepared in the dedicated home: %v", err)
	}
}

// A model value that is not a plain identifier must not reach config.toml; the
// Codex runtime is not built instead of starting with a broken home.
func TestCodexRuntimeFactoryRejectsUnsafeConfig(t *testing.T) {
	if rt := newCodexRuntime(aiEnv{CodexHome: t.TempDir(), Model: "x\"\n[mcp_servers]"}); rt != nil {
		t.Fatal("unsafe model must leave the runtime unbuilt")
	}
}

func TestNewAIReportServiceNeedsAIStore(t *testing.T) {
	stubCodex(t, false, nil)
	if svc := newAIReportService(struct{}{}, aiEnv{}); svc != nil {
		t.Fatal("a store without AI report methods must not get a service")
	}
	svc := newAIReportService(aireport.NewMemStore(), aiEnv{MaxConcurrent: 1, WallClock: time.Minute})
	if svc == nil {
		t.Fatal("want a service")
	}
	// No CCTRACE_AI_RUNTIME and no admin choice: no runtime is active, although
	// the API runtimes were built.
	if sel, _ := svc.Selection(t.Context()); sel.Key != "" {
		t.Fatalf("selection = %+v", sel)
	}
	if info, status := svc.RuntimeStatus(t.Context()); info.Key != aireport.DefaultRuntimeKey || status.Configured {
		t.Fatalf("status = %+v %+v", info, status)
	}
}

// Each runtime's model variable reaches its own settings, the environment
// layer an admin setting overrides; CCTRACE_AI_RUNTIME is the default choice.
func TestNewAIReportServicePassesEnvSettings(t *testing.T) {
	stubCodex(t, false, nil)
	svc := newAIReportService(aireport.NewMemStore(), aiEnv{
		Runtime: aireport.RuntimeClaude, Model: "gpt-env", ReasoningEffort: "low", OpenAIModel: "gpt-openai", ClaudeModel: "claude-opus-5",
	})
	ctx := t.Context()
	for key, want := range map[string]struct{ model, source, effort string }{
		aireport.DefaultRuntimeKey: {"gpt-env", aireport.SourceEnv, "low"},
		aireport.RuntimeOpenAI:     {"gpt-openai", aireport.SourceEnv, ""},
		aireport.RuntimeClaude:     {"claude-opus-5", aireport.SourceEnv, ""},
	} {
		got, err := svc.SettingsFor(ctx, key)
		if err != nil || got.Model != want.model || got.ModelSource != want.source || got.ReasoningEffort != want.effort {
			t.Errorf("%s = %+v, %v", key, got, err)
		}
	}
	if sel, _ := svc.Selection(ctx); sel.Key != aireport.RuntimeClaude || sel.Source != aireport.SourceEnv {
		t.Fatalf("selection = %+v", sel)
	}
	got, _ := newAIReportService(aireport.NewMemStore(), aiEnv{}).SettingsFor(ctx, aireport.RuntimeClaude)
	if got.Model != "claude-sonnet-5" || got.ModelSource != aireport.SourceDefault {
		t.Fatalf("claude default = %+v", got)
	}
}

// The environment key wins and locks the screen; without any secret nothing
// can be registered.
func TestNewAIReportServiceKeyring(t *testing.T) {
	stubCodex(t, false, nil)
	ctx := t.Context()
	svc := newAIReportService(aireport.NewMemStore(), aiEnv{OpenAIAPIKey: "sk-env-openai-1234", SecretsKey: "test-secret-at-least-32-bytes-long!!"})
	if info, _ := svc.Keyring().Credential(ctx, aireport.ProviderOpenAI); info.Source != aireport.CredentialEnv {
		t.Fatalf("openai = %+v", info)
	}
	if _, err := svc.Keyring().SetKey(ctx, aireport.ProviderAnthropic, "sk-ant-registered-5678", "a"); err != nil {
		t.Fatalf("register with secret: %v", err)
	}
	// A short secret is refused with a boot warning, not used.
	var logs bytes.Buffer
	log.SetOutput(&logs)
	short := newAIReportService(aireport.NewMemStore(), aiEnv{SecretsKey: "jwt"})
	log.SetOutput(os.Stderr)
	if _, err := short.Keyring().SetKey(ctx, aireport.ProviderAnthropic, "sk-ant-registered-5678", "a"); !errors.Is(err, aireport.ErrSecretsUnavailable) {
		t.Fatalf("register with a short secret: %v", err)
	}
	if !strings.Contains(logs.String(), "at least 32 bytes") {
		t.Fatalf("no boot warning: %q", logs.String())
	}
	noSecret := newAIReportService(aireport.NewMemStore(), aiEnv{})
	if _, err := noSecret.Keyring().SetKey(ctx, aireport.ProviderAnthropic, "sk-ant-registered-5678", "a"); !errors.Is(err, aireport.ErrSecretsUnavailable) {
		t.Fatalf("register without secret: %v", err)
	}
}
