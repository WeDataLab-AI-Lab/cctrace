package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/profile"
)

func TestApplyLocalDevEndpointsReadsDotEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("HTTP_PORT=18080\nGRPC_PORT=14317\n"), 0600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Chdir(dir)
	t.Setenv("HTTP_PORT", "")
	t.Setenv("GRPC_PORT", "")
	t.Setenv("CCTRACE_LOCAL_SYNC_ENDPOINT", "")
	t.Setenv("CCTRACE_LOCAL_OTEL_ENDPOINT", "")

	p := profile.NewDefault()
	p.Server.Endpoint = "https://trace.example.com:4317"
	p.Server.SyncEndpoint = "https://trace.example.com:18080"
	endpointOverride := ""

	runtimeEndpoint := applyLocalDevEndpoints(p, &endpointOverride)

	if endpointOverride != "http://localhost:18080" {
		t.Fatalf("endpointOverride = %q, want http://localhost:18080", endpointOverride)
	}
	if runtimeEndpoint != "http://localhost:14317" {
		t.Fatalf("runtimeEndpoint = %q, want http://localhost:14317", runtimeEndpoint)
	}
	if p.Server.Endpoint != "https://trace.example.com:4317" {
		t.Fatalf("Server.Endpoint mutated to %q", p.Server.Endpoint)
	}
}

func TestApplyLocalDevEndpointsKeepsExplicitEndpointOverride(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HTTP_PORT", "")
	t.Setenv("GRPC_PORT", "")
	t.Setenv("CCTRACE_LOCAL_SYNC_ENDPOINT", "")
	t.Setenv("CCTRACE_LOCAL_OTEL_ENDPOINT", "")

	p := profile.NewDefault()
	endpointOverride := "http://localhost:19090"

	runtimeEndpoint := applyLocalDevEndpoints(p, &endpointOverride)

	if endpointOverride != "http://localhost:19090" {
		t.Fatalf("endpointOverride = %q, want explicit override", endpointOverride)
	}
	if runtimeEndpoint != "http://localhost:4317" {
		t.Fatalf("runtimeEndpoint = %q, want http://localhost:4317", runtimeEndpoint)
	}
}

func TestApplyLocalDevEndpointsKeepsLocalProfileSyncEndpoint(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HTTP_PORT", "")
	t.Setenv("GRPC_PORT", "")
	t.Setenv("CCTRACE_LOCAL_SYNC_ENDPOINT", "")
	t.Setenv("CCTRACE_LOCAL_OTEL_ENDPOINT", "")

	p := profile.NewDefault()
	p.Server.Endpoint = "http://127.0.0.1:4317"
	p.Server.SyncEndpoint = "http://localhost:18080"
	endpointOverride := ""

	runtimeEndpoint := applyLocalDevEndpoints(p, &endpointOverride)

	if endpointOverride != "" {
		t.Fatalf("endpointOverride = %q, want unchanged for local profile sync endpoint", endpointOverride)
	}
	if runtimeEndpoint != "http://127.0.0.1:4317" {
		t.Fatalf("runtimeEndpoint = %q, want existing local OTEL endpoint", runtimeEndpoint)
	}
}

func TestLocalDevEndpointNormalizesExplicitEnvAndAllowsNoDefault(t *testing.T) {
	t.Setenv("CCTRACE_LOCAL_SYNC_ENDPOINT", "127.0.0.1:19090")
	got, ok := localDevEndpoint("CCTRACE_LOCAL_SYNC_ENDPOINT", nil, "")
	if !ok || got != "http://127.0.0.1:19090" {
		t.Fatalf("endpoint = %q ok=%v, want normalized explicit env", got, ok)
	}

	t.Setenv("CCTRACE_LOCAL_SYNC_ENDPOINT", "")
	got, ok = localDevEndpoint("CCTRACE_LOCAL_SYNC_ENDPOINT", nil, "")
	if ok || got != "" {
		t.Fatalf("endpoint = %q ok=%v, want no endpoint without default", got, ok)
	}
	if got := normalizeLocalEndpoint("https://localhost:19090"); got != "https://localhost:19090" {
		t.Fatalf("normalize https = %q", got)
	}
}

func TestConfigValuePrefersEnvironmentOverDotEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("HTTP_PORT=18080\n"), 0600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Chdir(dir)
	t.Setenv("HTTP_PORT", "19090")
	if got := configValue("HTTP_PORT"); got != "19090" {
		t.Fatalf("configValue = %q, want env override", got)
	}
}

func TestReadDotEnvSkipsCommentsAndMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := "\n# comment\nMALFORMED\n =empty\nHTTP_PORT='18080'\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	got := readDotEnv(path)
	if got["HTTP_PORT"] != "18080" {
		t.Fatalf("HTTP_PORT = %q, want 18080", got["HTTP_PORT"])
	}
	if _, ok := got["MALFORMED"]; ok {
		t.Fatal("malformed line should be ignored")
	}
	if _, ok := got[""]; ok {
		t.Fatal("empty key should be ignored")
	}
}

func TestRemovePIDFileIfCurrentPreservesNewWatcherPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.pid")
	if err := writePID(path, 222); err != nil {
		t.Fatalf("writePID: %v", err)
	}

	if err := removePIDFileIfCurrent(path, 111); err != nil {
		t.Fatalf("removePIDFileIfCurrent: %v", err)
	}
	got, err := readPID(path)
	if err != nil {
		t.Fatalf("readPID: %v", err)
	}
	if got != 222 {
		t.Fatalf("pid file = %d, want new watcher pid 222", got)
	}

	if err := removePIDFileIfCurrent(path, 222); err != nil {
		t.Fatalf("removePIDFileIfCurrent current: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("pid file should be removed for matching pid, stat err=%v", err)
	}
}

func TestRemovePIDFileIfCurrentMissingFileIsNoop(t *testing.T) {
	if err := removePIDFileIfCurrent(filepath.Join(t.TempDir(), "sync.pid"), 123); err != nil {
		t.Fatalf("removePIDFileIfCurrent missing: %v", err)
	}
}

func TestAutoMigrateCodexSelfHealsEnabledUserConfig(t *testing.T) {
	codexDir := t.TempDir()
	t.Setenv("CODEX_CONFIG_DIR", codexDir)
	legacy := `[otel.metrics_exporter.otlp-http]
endpoint = "http://old:14318/v1/metrics"
protocol = "binary"
`
	if err := os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte(legacy), 0600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}

	p := profile.NewDefault()
	p.Server.AuthToken = "tok"
	p.Options.CodexSyncEnabled = true
	autoMigrateCodex(p, "", "http://localhost:4317")

	data, err := os.ReadFile(filepath.Join(codexDir, "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	content := string(data)
	if strings.Contains(content, "[otel.metrics_exporter") {
		t.Fatalf("legacy otel table was not removed:\n%s", content)
	}
	if strings.Count(content, "metrics_exporter") != 1 {
		t.Fatalf("metrics_exporter should appear once after self-heal:\n%s", content)
	}
	if !strings.Contains(content, "http://localhost:4318/v1/metrics") {
		t.Fatalf("current metrics endpoint not written:\n%s", content)
	}
}

// #753: extra Codex homes (CODEX_HOME, options.codex_dirs) get the same
// self-heal as the default one — but only a home that already has an otel
// section, so enabling a profile never opts another home in.
func TestAutoMigrateCodexHealsExtraHomesThatHaveOtel(t *testing.T) {
	base := t.TempDir()
	envHome := t.TempDir()
	withoutOtel := t.TempDir()
	t.Setenv("CODEX_CONFIG_DIR", base)
	t.Setenv("CODEX_HOME", envHome)

	legacy := "[otel]\nendpoint = \"http://localhost:4317\"\n"
	plain := "model = \"gpt-5\"\n"
	for dir, content := range map[string]string{base: legacy, envHome: legacy, withoutOtel: plain} {
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(envHome, "auth.json"), []byte(`{"tokens":{"account_id":"acct-2"}}`), 0600); err != nil {
		t.Fatal(err)
	}

	p := profile.NewDefault()
	p.Server.AuthToken = "tok"
	p.Options.CodexSyncEnabled = true
	p.Options.CodexDirs = []string{withoutOtel}
	autoMigrateCodex(p, "", "http://localhost:4317")

	healed, err := os.ReadFile(filepath.Join(envHome, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"http://localhost:4318/v1/metrics", `X-Cctrace-Codex-Account = "acct-2"`} {
		if !strings.Contains(string(healed), want) {
			t.Fatalf("CODEX_HOME config missing %q:\n%s", want, healed)
		}
	}
	untouched, err := os.ReadFile(filepath.Join(withoutOtel, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(untouched) != plain {
		t.Fatalf("home without an otel section was rewritten:\n%s", untouched)
	}
}

// An extra home that sends to another collector is skipped, and says so once:
// otherwise its metrics stop reaching that collector with no word to anyone.
func TestAutoMigrateCodexSkipsAnExtraHomeOfAForeignCollector(t *testing.T) {
	useTempSyncHome(t)
	envHome := t.TempDir()
	t.Setenv("CODEX_HOME", envHome)
	foreign := "[otel]\nendpoint = \"http://collector.example.com:4317\"\n"
	if err := os.WriteFile(filepath.Join(envHome, "config.toml"), []byte(foreign), 0600); err != nil {
		t.Fatal(err)
	}

	p := profile.NewDefault()
	p.Server.AuthToken = "tok"
	p.Options.CodexSyncEnabled = true
	stdout, _ := captureOutput(t, func() { autoMigrateCodex(p, "", "http://localhost:4317") })

	data, err := os.ReadFile(filepath.Join(envHome, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != foreign {
		t.Fatalf("foreign collector's config rewritten:\n%s", data)
	}
	if strings.Count(stdout, envHome) != 1 || !strings.Contains(stdout, "다른 수집기") {
		t.Fatalf("want one skip line naming %s, got:\n%s", envHome, stdout)
	}
}

// A config.toml that is a symlink (dotfile managers) cannot be replaced
// atomically, so every sync used to print a self-heal error for it. It is now
// skipped with one line saying why, in the default home and an extra one alike.
func TestAutoMigrateCodexSkipsASymlinkedConfig(t *testing.T) {
	home := useTempSyncHome(t)
	base := filepath.Join(home, ".codex")
	envHome := t.TempDir()
	t.Setenv("CODEX_HOME", envHome)
	target := filepath.Join(t.TempDir(), "dotfiles-config.toml")
	legacy := "[otel]\nendpoint = \"http://localhost:4317\"\n"
	if err := os.WriteFile(target, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{base, envHome} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, "config.toml")); err != nil {
			t.Fatal(err)
		}
	}

	p := profile.NewDefault()
	p.Server.AuthToken = "tok"
	p.Options.CodexSyncEnabled = true
	stdout, stderr := captureOutput(t, func() { autoMigrateCodex(p, "", "http://localhost:4317") })

	if stderr != "" {
		t.Fatalf("symlinked config reported as an error:\n%s", stderr)
	}
	if strings.Count(stdout, "심볼릭 링크") != 2 {
		t.Fatalf("want one symlink line per home, got:\n%s", stdout)
	}
	if data, _ := os.ReadFile(target); string(data) != legacy {
		t.Fatalf("symlink target rewritten:\n%s", data)
	}
}

// CODEX_HOME or codex_dirs spelling the default home another way is still the
// default home: the extra-home pass must not visit it a second time under the
// extra-home rules.
func TestHealExtraCodexHomesSkipsAliasesOfTheDefaultHome(t *testing.T) {
	home := useTempSyncHome(t)
	base := filepath.Join(home, ".codex")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := "[otel]\nendpoint = \"http://collector.example.com:4317\"\n"
	if err := os.WriteFile(filepath.Join(base, "config.toml"), []byte(foreign), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", base+string(os.PathSeparator))
	p := profile.NewDefault()
	p.Server.AuthToken = "tok"
	p.Options.CodexDirs = []string{filepath.Join(base, "."), "~/.codex"}

	stdout, stderr := captureOutput(t, func() { healExtraCodexHomes(p, "http://localhost:4317", true) })
	if stdout != "" || stderr != "" {
		t.Fatalf("default home visited as an extra one:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

// Sync keeps an extra home's own token, so a reissued token leaves it stale and
// Codex gets 401s in silence. Sync says so -- one line per home -- and points at
// the explicit path, `cctrace init`, which replaces it.
func TestExtraHomeWithAnotherTokenIsNamedOnSyncAndReplacedByInit(t *testing.T) {
	home := useTempSyncHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	envHome := t.TempDir()
	t.Setenv("CODEX_HOME", envHome)
	stale := "[otel]\nmetrics_exporter = { otlp-http = { endpoint = \"http://localhost:4318/v1/metrics\", protocol = \"binary\", headers = { Authorization = \"Bearer old-tok\" } } }\n"
	if err := os.WriteFile(filepath.Join(envHome, "config.toml"), []byte(stale), 0600); err != nil {
		t.Fatal(err)
	}

	p := profile.NewDefault()
	p.Server.Endpoint = "http://localhost:4317"
	p.Server.AuthToken = "new-tok"
	p.Options.CodexSyncEnabled = true
	stdout, _ := captureOutput(t, func() { autoMigrateCodex(p, "", p.Server.Endpoint) })
	if data, _ := os.ReadFile(filepath.Join(envHome, "config.toml")); string(data) != stale {
		t.Fatalf("sync replaced the home's token:\n%s", data)
	}
	if strings.Count(stdout, "토큰이 현재 프로필과 다릅니다") != 1 || !strings.Contains(stdout, "cctrace init") {
		t.Fatalf("want one stale-token line pointing at cctrace init, got:\n%s", stdout)
	}

	captureOutput(t, func() {
		if err := runCodexPatch(p, false, ""); err != nil {
			t.Errorf("runCodexPatch: %v", err)
		}
	})
	if data, _ := os.ReadFile(filepath.Join(envHome, "config.toml")); !strings.Contains(string(data), "Bearer new-tok") {
		t.Fatalf("init did not replace the stale token:\n%s", data)
	}
}

func TestAutoMigrateCodexSkipsWithoutEndpointOrToken(t *testing.T) {
	codexDir := t.TempDir()
	t.Setenv("CODEX_CONFIG_DIR", codexDir)
	p := profile.NewDefault()
	p.Server.AuthToken = "tok"

	autoMigrateCodex(p, "", "")
	if p.Options.CodexSyncEnabled {
		t.Fatal("codex sync should remain disabled without endpoint")
	}
	if _, err := os.Stat(filepath.Join(codexDir, "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("config.toml should not be written without endpoint, stat err=%v", err)
	}

	p.Server.AuthToken = ""
	autoMigrateCodex(p, "", "http://localhost:4317")
	if p.Options.CodexSyncEnabled {
		t.Fatal("codex sync should remain disabled without token")
	}
}

func TestAutoMigrateCodexEnablesDisabledProfile(t *testing.T) {
	useTempSyncHome(t)
	codexDir := t.TempDir()
	t.Setenv("CODEX_CONFIG_DIR", codexDir)
	p := profile.NewDefault()
	p.Server.AuthToken = "tok"

	autoMigrateCodex(p, "", "http://localhost:4317")

	if !p.Options.CodexSyncEnabled {
		t.Fatal("codex sync should be enabled after migration")
	}
	data, err := os.ReadFile(filepath.Join(codexDir, "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	if !strings.Contains(string(data), "http://localhost:4318/v1/metrics") {
		t.Fatalf("metrics endpoint not written:\n%s", data)
	}
	saved, err := profile.Load()
	if err != nil {
		t.Fatalf("profile.Load: %v", err)
	}
	if !saved.Options.CodexSyncEnabled {
		t.Fatal("saved profile should enable codex sync")
	}
}

func TestRunSyncMigratesCodexWhenClaudeSyncIsDisabled(t *testing.T) {
	useTempSyncHome(t)
	codexDir := t.TempDir()
	t.Setenv("CODEX_CONFIG_DIR", codexDir)

	p := profile.NewDefault()
	p.Server.Endpoint = "http://localhost:4317"
	p.Server.AuthToken = "tok"
	p.Options.SyncEnabled = false
	if err := profile.Save(p); err != nil {
		t.Fatalf("save profile: %v", err)
	}

	if err := runSync(false, "", false, false, false, false, time.Second, "", "", "", false, "", false); err != nil {
		t.Fatalf("runSync: %v", err)
	}

	saved, err := profile.Load()
	if err != nil {
		t.Fatalf("load profile: %v", err)
	}
	if !saved.Options.CodexSyncEnabled {
		t.Fatal("CodexSyncEnabled = false, want true")
	}
	data, err := os.ReadFile(filepath.Join(codexDir, "config.toml"))
	if err != nil {
		t.Fatalf("read Codex config: %v", err)
	}
	if !strings.Contains(string(data), "http://localhost:4318/v1/metrics") {
		t.Fatalf("metrics endpoint not written:\n%s", data)
	}
}
