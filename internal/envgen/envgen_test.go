package envgen

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"

	"cctrace/internal/profile"
)

func TestMain(m *testing.M) {
	origExecutablePath := executablePath
	origLookPath := lookPath
	code := m.Run()
	executablePath = origExecutablePath
	lookPath = origLookPath
	os.Exit(code)
}

func fullProfile() *profile.Profile {
	p := profile.NewDefault()
	p.User.Name = "Alice Doe"
	p.User.Email = "alice@example.com"
	p.User.Team = "platform"
	p.Server.Endpoint = "http://trace.company.com:4317"
	p.Server.Protocol = "grpc"
	p.Server.AuthToken = "bearer-token-here"
	return p
}

func TestGenerateShFull(t *testing.T) {
	content := GenerateSh(fullProfile())

	expected := []string{
		"export CLAUDE_CODE_ENABLE_TELEMETRY=1",
		"export OTEL_METRICS_EXPORTER=otlp",
		"export OTEL_LOGS_EXPORTER=otlp",
		"export OTEL_EXPORTER_OTLP_PROTOCOL='grpc'",
		`export OTEL_EXPORTER_OTLP_ENDPOINT='http://trace.company.com:4317'`,
		`export OTEL_EXPORTER_OTLP_HEADERS='Authorization=Bearer bearer-token-here'`,
		"export OTEL_METRIC_EXPORT_INTERVAL=60000",
		"export OTEL_LOGS_EXPORT_INTERVAL=5000",
		"export OTEL_BSP_MAX_QUEUE_SIZE=4096",
		"export OTEL_BSP_SCHEDULE_DELAY=5000",
		"export OTEL_BSP_MAX_EXPORT_BATCH_SIZE=512",
		"export OTEL_BSP_EXPORT_TIMEOUT=30000",
		"export OTEL_RESOURCE_ATTRIBUTES='",
	}
	for _, exp := range expected {
		if !strings.Contains(content, exp) {
			t.Errorf("expected %q in output, got:\n%s", exp, content)
		}
	}
}

func TestGenerateShNoEndpoint(t *testing.T) {
	p := fullProfile()
	p.Server.Endpoint = ""
	p.Server.AuthToken = ""
	content := GenerateSh(p)

	if strings.Contains(content, "OTEL_EXPORTER_OTLP_ENDPOINT") {
		t.Error("expected no ENDPOINT line when endpoint is empty")
	}
	if strings.Contains(content, "OTEL_EXPORTER_OTLP_HEADERS") {
		t.Error("expected no HEADERS line when auth_token is empty")
	}
}

func TestGenerateShNoAuthToken(t *testing.T) {
	p := fullProfile()
	p.Server.AuthToken = ""
	content := GenerateSh(p)

	if !strings.Contains(content, "OTEL_EXPORTER_OTLP_ENDPOINT") {
		t.Error("expected ENDPOINT line when endpoint is set")
	}
	if strings.Contains(content, "OTEL_EXPORTER_OTLP_HEADERS") {
		t.Error("expected no HEADERS line when auth_token is empty")
	}
}

func TestGeneratePs1Full(t *testing.T) {
	content := GeneratePs1(fullProfile())

	expected := []string{
		`$env:CLAUDE_CODE_ENABLE_TELEMETRY = "1"`,
		`$env:OTEL_METRICS_EXPORTER = "otlp"`,
		`$env:OTEL_EXPORTER_OTLP_PROTOCOL = 'grpc'`,
		`$env:OTEL_EXPORTER_OTLP_ENDPOINT = 'http://trace.company.com:4317'`,
		`$env:OTEL_EXPORTER_OTLP_HEADERS = 'Authorization=Bearer bearer-token-here'`,
		`$env:OTEL_BSP_MAX_QUEUE_SIZE = "4096"`,
		`$env:OTEL_BSP_SCHEDULE_DELAY = "5000"`,
		`$env:OTEL_BSP_MAX_EXPORT_BATCH_SIZE = "512"`,
		`$env:OTEL_BSP_EXPORT_TIMEOUT = "30000"`,
	}
	for _, exp := range expected {
		if !strings.Contains(content, exp) {
			t.Errorf("expected %q in output, got:\n%s", exp, content)
		}
	}
}

func TestGenerateEnvFilesQuoteMetacharacters(t *testing.T) {
	p := fullProfile()
	p.Server.Endpoint = "http://trace.example/$(touch pwn)"
	p.Server.AuthToken = "tok'$(bad)`"

	sh := GenerateSh(p)
	wantShEndpoint := "export OTEL_EXPORTER_OTLP_ENDPOINT='http://trace.example/$(touch pwn)'\n"
	wantShHeader := "export OTEL_EXPORTER_OTLP_HEADERS='Authorization=Bearer tok'\\''$(bad)`'\n"
	if !strings.Contains(sh, wantShEndpoint) {
		t.Fatalf("shell endpoint not single quoted:\n%s", sh)
	}
	if !strings.Contains(sh, wantShHeader) {
		t.Fatalf("shell header not escaped:\n%s", sh)
	}

	ps := GeneratePs1(p)
	wantPsEndpoint := "$env:OTEL_EXPORTER_OTLP_ENDPOINT = 'http://trace.example/$(touch pwn)'\r\n"
	wantPsHeader := "$env:OTEL_EXPORTER_OTLP_HEADERS = 'Authorization=Bearer tok''$(bad)`'\r\n"
	if !strings.Contains(ps, wantPsEndpoint) {
		t.Fatalf("PowerShell endpoint not single quoted:\n%s", ps)
	}
	if !strings.Contains(ps, wantPsHeader) {
		t.Fatalf("PowerShell header not escaped:\n%s", ps)
	}
}

func TestGeneratePs1LineEndings(t *testing.T) {
	content := GeneratePs1(fullProfile())

	if !strings.Contains(content, "\r\n") {
		t.Error("expected CRLF line endings in PowerShell output")
	}
}

func TestGenerateShLineEndings(t *testing.T) {
	content := GenerateSh(fullProfile())

	if strings.Contains(content, "\r\n") {
		t.Error("expected LF-only line endings in shell output")
	}
	if !strings.Contains(content, "\n") {
		t.Error("expected LF line endings in shell output")
	}
}

func TestUrlEncoding(t *testing.T) {
	p := fullProfile()
	p.User.Name = "Alice Doe"
	content := GenerateSh(p)

	if !strings.Contains(content, "user.name=Alice%20Doe") {
		t.Errorf("expected URL-encoded name with %%20, got:\n%s", content)
	}
}

func TestWriteSh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env.sh")

	if err := WriteSh(fullProfile(), path); err != nil {
		t.Fatalf("WriteSh: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0600 {
			t.Errorf("expected 0600 permissions, got %o", perm)
		}
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "CLAUDE_CODE_ENABLE_TELEMETRY=1") {
		t.Error("env.sh missing expected content")
	}
}

// --- Apply/Remove settings.json tests ---

func testProfile(claudeDir string) *profile.Profile {
	p := fullProfile()
	p.User.ID = "testuser"
	p.ClaudeConfigDir = claudeDir
	return p
}

func TestManagedStampVersionAndAbsentRemoval(t *testing.T) {
	settings := map[string]interface{}{}
	removeManagedStamp(settings)
	if _, ok := settings[cctraceMetaKey]; ok {
		t.Fatal("removeManagedStamp should not create metadata")
	}

	setManagedStamp(settings, "hash-1", "v1.2.3")
	meta := settings[cctraceMetaKey].(map[string]interface{})
	if meta[managedHashField] != "hash-1" {
		t.Fatalf("managed hash = %v, want hash-1", meta[managedHashField])
	}
	if meta[binaryVersField] != "v1.2.3" {
		t.Fatalf("binary version = %v, want v1.2.3", meta[binaryVersField])
	}
}

func TestFirstShellTokenParsesEscapedSingleQuote(t *testing.T) {
	token, rest, ok := firstShellToken("'can'\\''t' sync --daemon")
	if !ok {
		t.Fatal("firstShellToken did not parse quoted token")
	}
	if token != "can't" || rest != "sync --daemon" {
		t.Fatalf("token/rest = %q/%q, want can't/sync --daemon", token, rest)
	}
}

func TestShellQuoteEmptyValue(t *testing.T) {
	if got := shellQuote(""); got != "''" {
		t.Fatalf("shellQuote(empty) = %q, want ''", got)
	}
}

func TestApplyToClaudeSettingsRejectsUnreadableSettingsPath(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	if err := os.MkdirAll(filepath.Join(claudeDir, "settings.json"), 0755); err != nil {
		t.Fatalf("mkdir settings path: %v", err)
	}
	err := ApplyToClaudeSettings(testProfile(claudeDir))
	if err == nil || !strings.Contains(err.Error(), "read settings") {
		t.Fatalf("error = %v, want read settings", err)
	}
}

func TestEnsureClaudeSettingsCurrentRejectsUnreadableSettingsPath(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	if err := os.MkdirAll(filepath.Join(claudeDir, "settings.json"), 0755); err != nil {
		t.Fatalf("mkdir settings path: %v", err)
	}
	changed, err := EnsureClaudeSettingsCurrent(testProfile(claudeDir))
	if err == nil || !strings.Contains(err.Error(), "read settings") {
		t.Fatalf("changed=%v err=%v, want read settings", changed, err)
	}
	if changed {
		t.Fatal("unreadable settings path must not be reported as changed")
	}
}

// On a fresh machine ~/.claude does not exist yet. The lock file lives in that
// directory, so taking the lock before creating it failed with ENOENT and the
// sync-startup self-heal was skipped on every run, silently.
func TestEnsureClaudeSettingsCurrentCreatesMissingClaudeDir(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), "does-not-exist-yet", ".claude")

	changed, err := EnsureClaudeSettingsCurrent(testProfile(claudeDir))
	if err != nil {
		t.Fatalf("EnsureClaudeSettingsCurrent on a missing .claude dir: %v", err)
	}
	if !changed {
		t.Fatal("expected settings to be written")
	}
	if _, err := os.Stat(filepath.Join(claudeDir, "settings.json")); err != nil {
		t.Fatalf("settings.json not created: %v", err)
	}
}

// The settings writers must serialize against each other. Previously only
// EnsureClaudeSettingsCurrent held the lock while its six direct callers of
// ApplyToClaudeSettings wrote unserialized, so a `cctrace config` running
// against a live sync daemon could drop the daemon's update.
func TestApplyToClaudeSettingsWaitsForSettingsLock(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatal(err)
	}
	p := testProfile(claudeDir)
	settingsPath, err := ClaudeSettingsPath(p)
	if err != nil {
		t.Fatal(err)
	}

	held := flock.New(settingsPath + ".lock")
	if err := held.Lock(); err != nil {
		t.Fatalf("acquire lock: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- ApplyToClaudeSettings(p) }()

	select {
	case err := <-done:
		held.Unlock() //nolint:errcheck
		t.Fatalf("apply completed while the settings lock was held (err=%v)", err)
	case <-time.After(150 * time.Millisecond):
	}

	if err := held.Unlock(); err != nil {
		t.Fatalf("release lock: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("apply after lock release: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("apply did not proceed after the lock was released")
	}
}

// `cctrace reset` on a machine that never applied settings must leave no trace.
// Taking the settings lock creates the directory the lock file lives in, so a
// remove that runs before any apply used to leave an empty ~/.claude behind —
// the operation meant to clean up created something instead.
func TestRemoveFromClaudeSettingsLeavesNoClaudeDir(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), ".claude")

	if err := RemoveFromClaudeSettings(testProfile(claudeDir)); err != nil {
		t.Fatalf("RemoveFromClaudeSettings: %v", err)
	}

	if _, err := os.Stat(claudeDir); !os.IsNotExist(err) {
		t.Fatalf("remove created %s (stat err=%v)", claudeDir, err)
	}
}

// A settings path we cannot prepare must surface an error rather than silently
// reporting "nothing changed". The failure now comes from creating the
// directory, which happens before the lock is taken.
func TestEnsureClaudeSettingsCurrentReportsUnusableSettingsDir(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), "claude-file")
	if err := os.WriteFile(claudeDir, []byte("not a dir"), 0600); err != nil {
		t.Fatalf("write claude dir as file: %v", err)
	}
	changed, err := EnsureClaudeSettingsCurrent(testProfile(claudeDir))
	if err == nil || !strings.Contains(err.Error(), "create .claude directory") {
		t.Fatalf("changed=%v err=%v, want create .claude directory", changed, err)
	}
}

func TestEnsureClaudeSettingsCurrentPropagatesClaudeSettingsPathError(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	p := fullProfile()
	p.ClaudeConfigDir = ""
	changed, err := EnsureClaudeSettingsCurrent(p)
	if err == nil {
		t.Fatal("expected Claude settings path error")
	}
	if changed {
		t.Fatal("path error must not be reported as changed")
	}
}

func TestEnsureClaudeSettingsCurrentPropagatesApplyError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission semantics differ on Windows")
	}
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatalf("mkdir claude dir: %v", err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")
	writeJSON(t, settingsPath, map[string]interface{}{
		cctraceMetaKey: map[string]interface{}{managedHashField: "stale"},
	})
	lockPath := settingsPath + ".lock"
	if err := os.WriteFile(lockPath, []byte{}, 0600); err != nil {
		t.Fatalf("write lock file: %v", err)
	}
	if err := os.Chmod(claudeDir, 0500); err != nil {
		t.Fatalf("chmod claude dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(claudeDir, 0755)
	})

	changed, err := EnsureClaudeSettingsCurrent(testProfile(claudeDir))
	if err == nil {
		t.Fatal("expected apply error")
	}
	if changed {
		t.Fatal("failed apply must not be reported as changed")
	}
}

func TestWriteFileAtomicRejectsParentFile(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-dir")
	if err := os.WriteFile(parent, []byte("x"), 0600); err != nil {
		t.Fatalf("write parent file: %v", err)
	}
	if err := writeFileAtomic(filepath.Join(parent, "settings.json"), []byte("{}"), 0644); err == nil {
		t.Fatal("expected writeFileAtomic to fail when parent is a file")
	}
}

func TestWriteFileAtomicRejectsDestinationDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatalf("mkdir destination: %v", err)
	}
	if err := writeFileAtomic(path, []byte("{}"), 0644); err == nil {
		t.Fatal("expected writeFileAtomic to fail when destination is a directory")
	}
}

func TestUpsertHookCommandCreatesMissingEventInExistingHooks(t *testing.T) {
	settings := map[string]interface{}{
		"hooks": map[string]interface{}{},
	}
	upsertHookCommand(settings, hookCommandSpec{event: "SessionStart", command: "cctrace sync --daemon"})
	hooks := settings["hooks"].(map[string]interface{})
	if _, ok := hooks["SessionStart"]; !ok {
		t.Fatalf("SessionStart event not created: %#v", hooks)
	}
}

func TestUpsertHookCommandAddsDefaultMatcherWithoutReplacingSpecificMatcher(t *testing.T) {
	settings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"matcher": "Bash",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "echo keep"},
					},
				},
			},
		},
	}

	upsertHookCommand(settings, hookCommandSpec{event: "SessionStart", command: "cctrace sync --daemon"})

	hooks := settings["hooks"].(map[string]interface{})
	eventHooks := hooks["SessionStart"].([]interface{})
	if len(eventHooks) != 2 {
		t.Fatalf("SessionStart matchers = %d, want specific + default", len(eventHooks))
	}
	specific := eventHooks[0].(map[string]interface{})
	if specific["matcher"] != "Bash" {
		t.Fatalf("specific matcher changed: %#v", specific)
	}
	defaultMatcher := eventHooks[1].(map[string]interface{})
	if defaultMatcher["matcher"] != "" {
		t.Fatalf("default matcher = %v, want empty string", defaultMatcher["matcher"])
	}
	defaultHooks := defaultMatcher["hooks"].([]interface{})
	command := defaultHooks[0].(map[string]interface{})["command"]
	if command != "cctrace sync --daemon" {
		t.Fatalf("default matcher command = %v", command)
	}
}

func writeJSON(t *testing.T, path string, v interface{}) {
	t.Helper()
	data, _ := json.MarshalIndent(v, "", "  ")
	os.WriteFile(path, data, 0644)
}

func readJSON(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]interface{}
	json.Unmarshal(data, &m)
	return m
}

func TestApplyPreservesExistingKeys(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	os.MkdirAll(claudeDir, 0755)

	// Pre-existing settings with custom keys
	writeJSON(t, filepath.Join(claudeDir, "settings.json"), map[string]interface{}{
		"env": map[string]interface{}{
			"MY_CUSTOM_VAR": "keep_this",
			"ANOTHER_VAR":   "also_keep",
		},
		"permissions": map[string]interface{}{
			"allow": []interface{}{"Bash(npm run *)"},
		},
	})

	p := testProfile(claudeDir)
	if err := ApplyToClaudeSettings(p); err != nil {
		t.Fatalf("apply: %v", err)
	}

	s := readJSON(t, filepath.Join(claudeDir, "settings.json"))
	env := s["env"].(map[string]interface{})

	// Non-OTEL keys preserved
	if env["MY_CUSTOM_VAR"] != "keep_this" {
		t.Errorf("MY_CUSTOM_VAR lost: got %v", env["MY_CUSTOM_VAR"])
	}
	if env["ANOTHER_VAR"] != "also_keep" {
		t.Errorf("ANOTHER_VAR lost: got %v", env["ANOTHER_VAR"])
	}

	// OTEL keys added
	if env["CLAUDE_CODE_ENABLE_TELEMETRY"] != "1" {
		t.Error("OTEL key not applied")
	}

	// Non-env section preserved
	perms, ok := s["permissions"].(map[string]interface{})
	if !ok {
		t.Fatal("permissions section lost")
	}
	allow, ok := perms["allow"].([]interface{})
	if !ok || len(allow) != 1 {
		t.Fatalf("permissions.allow lost: %v", perms)
	}
}

func TestHookBinaryPathPrefersPathBinaryOverTempExecutable(t *testing.T) {
	t.Cleanup(func() {
		executablePath = os.Executable
		lookPath = exec.LookPath
	})

	executablePath = func() (string, error) {
		return "/private/tmp/cctrace-test", nil
	}
	lookPath = func(file string) (string, error) {
		if file != "cctrace" {
			t.Fatalf("unexpected lookup for %q", file)
		}
		return "/Users/alice/.local/bin/cctrace", nil
	}

	got := hookBinaryPath()
	want := "/Users/alice/.local/bin/cctrace"
	if got != want {
		t.Fatalf("hookBinaryPath() = %q, want %q", got, want)
	}
}

func TestApplyRewritesTempHookPathsToStableBinary(t *testing.T) {
	t.Cleanup(func() {
		executablePath = os.Executable
		lookPath = exec.LookPath
	})

	executablePath = func() (string, error) {
		return "/private/tmp/cctrace-test", nil
	}
	lookPath = func(file string) (string, error) {
		if file != "cctrace" {
			t.Fatalf("unexpected lookup for %q", file)
		}
		return "/Users/alice/.local/bin/cctrace", nil
	}

	claudeDir := filepath.Join(t.TempDir(), ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	writeJSON(t, filepath.Join(claudeDir, "settings.json"), map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"hooks": []interface{}{
						map[string]interface{}{
							"command": "/private/tmp/cctrace-test sync --daemon --claude-dir /Users/alice/.claude --interval 1s",
							"type":    "command",
						},
					},
					"matcher": "",
				},
			},
			"SessionEnd": []interface{}{
				map[string]interface{}{
					"hooks": []interface{}{
						map[string]interface{}{
							"command": "/private/tmp/cctrace-test sync --stop 2>/dev/null; /private/tmp/cctrace-test sync --daemon --once --claude-dir /Users/alice/.claude",
							"type":    "command",
						},
					},
					"matcher": "",
				},
			},
		},
	})

	if err := ApplyToClaudeSettings(testProfile(claudeDir)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	s := readJSON(t, filepath.Join(claudeDir, "settings.json"))
	hooks := s["hooks"].(map[string]interface{})
	start := hooks["SessionStart"].([]interface{})[0].(map[string]interface{})["hooks"].([]interface{})[0].(map[string]interface{})["command"].(string)
	end := hooks["SessionEnd"].([]interface{})[0].(map[string]interface{})["hooks"].([]interface{})[0].(map[string]interface{})["command"].(string)

	if strings.Contains(start, "/private/tmp/cctrace-test") {
		t.Fatalf("SessionStart kept temp binary: %q", start)
	}
	if strings.Contains(end, "/private/tmp/cctrace-test") {
		t.Fatalf("SessionEnd kept temp binary: %q", end)
	}
	if !strings.Contains(start, "/Users/alice/.local/bin/cctrace sync --daemon") {
		t.Fatalf("SessionStart not rewritten to stable binary: %q", start)
	}
	if !strings.Contains(end, "/Users/alice/.local/bin/cctrace sync --daemon --once") {
		t.Fatalf("SessionEnd not rewritten to stable binary: %q", end)
	}
	// Migration: the old --stop prefix must be dropped (it killed the global
	// daemon for other sessions and blocked the 1.5s SessionEnd budget).
	if strings.Contains(end, "--stop") {
		t.Fatalf("SessionEnd should no longer call --stop: %q", end)
	}
	endEntry := hooks["SessionEnd"].([]interface{})[0].(map[string]interface{})["hooks"].([]interface{})[0].(map[string]interface{})
	if endEntry["async"] != true {
		t.Fatalf("SessionEnd should be async, got %v", endEntry["async"])
	}
	if endEntry["timeout"] != float64(30) {
		t.Fatalf("SessionEnd timeout = %v, want 30", endEntry["timeout"])
	}
}

func TestApplyCreatesAsyncSessionEndHook(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	os.MkdirAll(claudeDir, 0755)

	// Fresh settings (no existing hooks) exercises the create path.
	if err := ApplyToClaudeSettings(testProfile(claudeDir)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	s := readJSON(t, filepath.Join(claudeDir, "settings.json"))
	hooks := s["hooks"].(map[string]interface{})
	endEntry := hooks["SessionEnd"].([]interface{})[0].(map[string]interface{})["hooks"].([]interface{})[0].(map[string]interface{})
	cmd := endEntry["command"].(string)
	if strings.Contains(cmd, "--stop") {
		t.Fatalf("SessionEnd should not call --stop: %q", cmd)
	}
	if !strings.Contains(cmd, "sync --daemon --once") {
		t.Fatalf("SessionEnd should do a one-shot sync: %q", cmd)
	}
	if !strings.Contains(cmd, "--auto-profile") {
		t.Fatalf("SessionEnd should auto-resolve profile from Claude dir: %q", cmd)
	}
	if endEntry["async"] != true {
		t.Fatalf("SessionEnd should be async, got %v", endEntry["async"])
	}
	if endEntry["timeout"] != float64(30) {
		t.Fatalf("SessionEnd timeout = %v, want 30", endEntry["timeout"])
	}

	// SessionStart must NOT be async: daemon spawn is fast and its
	// "Sync daemon started (pid…)" output is useful to the user.
	startEntry := hooks["SessionStart"].([]interface{})[0].(map[string]interface{})["hooks"].([]interface{})[0].(map[string]interface{})
	if _, has := startEntry["async"]; has {
		t.Fatalf("SessionStart should not be async")
	}
}

func TestApplyRefreshesSessionStartHookExecutionModifiers(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	os.MkdirAll(claudeDir, 0755)
	writeJSON(t, filepath.Join(claudeDir, "settings.json"), map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"hooks": []interface{}{
						map[string]interface{}{
							"command": "cctrace sync --daemon --claude-dir " + claudeDir + " --interval 1s",
							"type":    "command",
							"async":   true,
							"timeout": 30,
						},
					},
					"matcher": "",
				},
			},
		},
	})

	if err := ApplyToClaudeSettings(testProfile(claudeDir)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	s := readJSON(t, filepath.Join(claudeDir, "settings.json"))
	hooks := s["hooks"].(map[string]interface{})
	startEntry := hooks["SessionStart"].([]interface{})[0].(map[string]interface{})["hooks"].([]interface{})[0].(map[string]interface{})
	if _, has := startEntry["async"]; has {
		t.Fatalf("SessionStart async modifier should be removed when current spec is synchronous: %v", startEntry)
	}
	if _, has := startEntry["timeout"]; has {
		t.Fatalf("SessionStart timeout modifier should be removed when current spec has no timeout: %v", startEntry)
	}
}

func TestApplyQuotesClaudeDirWithSpacesInSyncHooks(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), "Claude Config With Spaces")
	os.MkdirAll(claudeDir, 0755)

	if err := ApplyToClaudeSettings(testProfile(claudeDir)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	s := readJSON(t, filepath.Join(claudeDir, "settings.json"))
	hooks := s["hooks"].(map[string]interface{})
	start := hooks["SessionStart"].([]interface{})[0].(map[string]interface{})["hooks"].([]interface{})[0].(map[string]interface{})["command"].(string)
	end := hooks["SessionEnd"].([]interface{})[0].(map[string]interface{})["hooks"].([]interface{})[0].(map[string]interface{})["command"].(string)
	// The same function the implementation uses, not a restatement of it. On
	// Windows the conversion is ToSlash plus a drive-letter rewrite (C:/ -> /c/)
	// because the hook is run by a bash-like shell; spelling out only half of that
	// here is what made this test fail while the implementation was correct.
	quotedDir := shellQuote(normalizeHookPath(claudeDir))
	if !strings.Contains(start, "--claude-dir "+quotedDir) {
		t.Fatalf("SessionStart must quote Claude dir with spaces, got %q", start)
	}
	if !strings.Contains(end, "--claude-dir "+quotedDir) {
		t.Fatalf("SessionEnd must quote Claude dir with spaces, got %q", end)
	}
	if !strings.Contains(start, "--auto-profile") || !strings.Contains(end, "--auto-profile") {
		t.Fatalf("sync hooks must include --auto-profile, start=%q end=%q", start, end)
	}
}

func TestIsCctraceHookRequiresSyncSubcommandToken(t *testing.T) {
	if isCctraceHook("cctrace synchronize --dangerous") {
		t.Fatal("cctrace command with non-sync subcommand must not be treated as managed hook")
	}
	if !isCctraceHook("'/Applications/Cctrace App/cctrace' sync --daemon --claude-dir /tmp/claude") {
		t.Fatal("quoted cctrace binary followed by sync should be treated as managed hook")
	}
	if !isCctraceHook("/c/Program Files/cctrace/cctrace.exe sync --stop 2>/dev/null; /c/Program Files/cctrace/cctrace.exe sync --daemon --once") {
		t.Fatal("old unquoted cctrace path with spaces should be treated as managed hook")
	}
	if !isCctraceHook(`"/Applications/Cctrace App/cctrace" sync --daemon --claude-dir /tmp/claude`) {
		t.Fatal("double-quoted cctrace binary followed by sync should be treated as managed hook")
	}
	if isCctraceHook("echo /usr/bin/cctrace sync --daemon") {
		t.Fatal("user hook that merely mentions cctrace sync must not be treated as managed hook")
	}
	if isCctraceHook("bash -lc '/usr/bin/cctrace sync --daemon'") {
		t.Fatal("wrapped shell command must not be treated as managed hook")
	}
}

func TestShellQuoteEscapesSingleQuotesAndLeavesSimpleValues(t *testing.T) {
	if got := shellQuote("/usr/local/bin/cctrace"); got != "/usr/local/bin/cctrace" {
		t.Fatalf("simple path quoted as %q", got)
	}
	want := "'/Users/Alice'\\''s Claude/.claude'"
	if got := shellQuote("/Users/Alice's Claude/.claude"); got != want {
		t.Fatalf("shellQuote = %q, want %q", got, want)
	}
}

func TestIsCctraceHookRecognizesManagedBinaryNamesOnly(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want bool
	}{
		{"bare cctrace", "cctrace sync --daemon", true},
		{"windows exe", "/c/Users/alice/bin/cctrace.exe sync --daemon", true},
		{"test binary", "/tmp/cctrace-test sync --daemon", true},
		{"non-sync cctrace", "cctrace status", false},
		{"other binary", "other sync --daemon", false},
		{"missing subcommand", "cctrace", false},
		{"unterminated quote", "'/tmp/cctrace sync --daemon", false},
		// A foreign binary whose name merely ends in "cctrace" is somebody
		// else's hook. Claiming it means `cctrace reset` deletes it, and the
		// settings guard cannot catch that because it classifies with this same
		// predicate — a hook we misclaim is excluded from the protected set.
		{"foreign suffix match", "/opt/tools/my-cctrace sync --all", false},
		{"foreign word ending in cctrace", "/usr/bin/notcctrace sync --daemon", false},
		// A `cctrace-`-prefixed name alone is not enough: nothing tells somebody's
		// /opt/security/cctrace-audit apart from our own cctrace-linux by name,
		// and claiming it means apply overwrites their hook and reset deletes it.
		{"foreign prefix match", "/opt/security/cctrace-audit sync --all", false},
		{"foreign dotted prefix", "/opt/security/cctrace.audit sync --all", false},
		// But a prefixed name invoking sync with the flags only we generate IS
		// ours. The quickstart tells Linux and Windows users to run the
		// downloaded binary in place (./cctrace-linux init), so these hooks are
		// what those installs actually write. Failing to recognize them makes
		// upsertHookCommand append a second hook on every later run.
		{"distribution binary, generated hook", "/usr/local/bin/cctrace-linux sync --daemon --claude-dir /home/u/.claude --auto-profile --interval 1s", true},
		{"distribution binary, generated session-end hook", "/usr/local/bin/cctrace-linux sync --daemon --once --claude-dir /home/u/.claude --auto-profile", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCctraceHook(tc.cmd); got != tc.want {
				t.Fatalf("isCctraceHook(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

func TestRemoveOnlyDeletesOTELKeys(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	os.MkdirAll(claudeDir, 0755)

	// Start with custom env keys + a non-cctrace hook
	writeJSON(t, filepath.Join(claudeDir, "settings.json"), map[string]interface{}{
		"env": map[string]interface{}{
			"MY_CUSTOM_VAR": "keep_this",
			"PATH_EXTRA":    "/usr/local/bin",
		},
		"hooks": map[string]interface{}{
			"SessionEnd": []interface{}{
				map[string]interface{}{
					"hooks": []interface{}{
						map[string]interface{}{"command": "bash ~/other-hook.sh", "type": "command"},
					},
					"matcher": "",
				},
			},
		},
	})

	p := testProfile(claudeDir)

	// Apply OTEL vars
	if err := ApplyToClaudeSettings(p); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Remove OTEL vars
	if err := RemoveFromClaudeSettings(p); err != nil {
		t.Fatalf("remove: %v", err)
	}

	s := readJSON(t, filepath.Join(claudeDir, "settings.json"))
	env := s["env"].(map[string]interface{})

	// OTEL keys gone
	otelKeys := []string{
		"CLAUDE_CODE_ENABLE_TELEMETRY", "OTEL_METRICS_EXPORTER",
		"OTEL_LOGS_EXPORTER", "OTEL_EXPORTER_OTLP_PROTOCOL",
		"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_HEADERS",
		"OTEL_RESOURCE_ATTRIBUTES", "OTEL_METRIC_EXPORT_INTERVAL",
		"OTEL_LOGS_EXPORT_INTERVAL", "OTEL_BSP_MAX_QUEUE_SIZE",
		"OTEL_BSP_SCHEDULE_DELAY", "OTEL_BSP_MAX_EXPORT_BATCH_SIZE",
		"OTEL_BSP_EXPORT_TIMEOUT",
	}
	for _, key := range otelKeys {
		if _, ok := env[key]; ok {
			t.Errorf("OTEL key %q should be removed", key)
		}
	}

	// Non-OTEL keys still present
	if env["MY_CUSTOM_VAR"] != "keep_this" {
		t.Errorf("MY_CUSTOM_VAR lost after remove")
	}
	if env["PATH_EXTRA"] != "/usr/local/bin" {
		t.Errorf("PATH_EXTRA lost after remove")
	}

	// Non-env sections preserved
	hooks := s["hooks"].(map[string]interface{})
	sessionEnd, ok := hooks["SessionEnd"].([]interface{})
	if !ok || len(sessionEnd) == 0 {
		t.Fatal("hooks.SessionEnd lost")
	}
	// The non-cctrace hook should still be there
	matcher := sessionEnd[0].(map[string]interface{})
	hooksList := matcher["hooks"].([]interface{})
	foundOtherHook := false
	foundCctrace := false
	for _, h := range hooksList {
		hm := h.(map[string]interface{})
		cmd := hm["command"].(string)
		if cmd == "bash ~/other-hook.sh" {
			foundOtherHook = true
		}
		if strings.HasPrefix(cmd, "cctrace sync") {
			foundCctrace = true
		}
	}
	if !foundOtherHook {
		t.Error("non-cctrace hook was lost after remove")
	}
	if foundCctrace {
		t.Error("cctrace sync hook should have been removed")
	}
}

func TestRemoveDeletesCctraceHooksAndStampWhenEnvSectionMissing(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	os.MkdirAll(claudeDir, 0755)
	writeJSON(t, filepath.Join(claudeDir, "settings.json"), map[string]interface{}{
		cctraceMetaKey: map[string]interface{}{
			managedHashField: "old-hash",
			"keep":           "user-owned",
		},
		"hooks": map[string]interface{}{
			"SessionEnd": []interface{}{
				map[string]interface{}{
					"hooks": []interface{}{
						map[string]interface{}{"command": "cctrace sync --daemon --once --claude-dir " + claudeDir, "type": "command"},
						map[string]interface{}{"command": "bash ~/cleanup.sh", "type": "command"},
					},
					"matcher": "",
				},
			},
		},
	})

	if err := RemoveFromClaudeSettings(testProfile(claudeDir)); err != nil {
		t.Fatalf("remove: %v", err)
	}

	s := readJSON(t, filepath.Join(claudeDir, "settings.json"))
	meta := s[cctraceMetaKey].(map[string]interface{})
	if _, has := meta[managedHashField]; has {
		t.Fatalf("managed stamp should be removed even when env is absent: %v", meta)
	}
	if meta["keep"] != "user-owned" {
		t.Fatalf("unknown cctrace metadata should be preserved: %v", meta)
	}
	hooks := s["hooks"].(map[string]interface{})
	sessionEnd := hooks["SessionEnd"].([]interface{})
	hooksList := sessionEnd[0].(map[string]interface{})["hooks"].([]interface{})
	if len(hooksList) != 1 {
		t.Fatalf("expected only user hook to remain, got %v", hooksList)
	}
	if hooksList[0].(map[string]interface{})["command"] != "bash ~/cleanup.sh" {
		t.Fatalf("wrong hook remained: %v", hooksList)
	}
}

func TestValidateSettingsJSONRejectsMalformedHooks(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{"event not array", `{"hooks":{"SessionEnd":{}}}`},
		{"matcher not object", `{"hooks":{"SessionEnd":[null]}}`},
		{"hooks missing", `{"hooks":{"SessionEnd":[{"matcher":""}]}}`},
		{"hooks null", `{"hooks":{"SessionEnd":[{"matcher":"","hooks":null}]}}`},
		{"hooks not array", `{"hooks":{"SessionEnd":[{"matcher":"","hooks":{}}]}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateSettingsJSON([]byte(tc.json)); err == nil {
				t.Fatal("expected malformed hooks to be rejected")
			}
		})
	}
}

func TestRemoveNoNullHooks(t *testing.T) {
	// Regression: removeSyncHook must not leave hooks: null in settings.json
	// which causes Claude Code to reject the file.
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	os.MkdirAll(claudeDir, 0755)

	p := testProfile(claudeDir)

	// Apply (creates SessionStart + SessionEnd hooks with only cctrace commands)
	if err := ApplyToClaudeSettings(p); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Verify hooks were created
	s := readJSON(t, filepath.Join(claudeDir, "settings.json"))
	hooks := s["hooks"].(map[string]interface{})
	if _, ok := hooks["SessionStart"]; !ok {
		t.Fatal("SessionStart should exist after apply")
	}

	// Remove — cctrace is the only hook, so matchers and events should be fully cleaned
	if err := RemoveFromClaudeSettings(p); err != nil {
		t.Fatalf("remove: %v", err)
	}

	s = readJSON(t, filepath.Join(claudeDir, "settings.json"))
	hooks = s["hooks"].(map[string]interface{})

	// SessionStart/SessionEnd should be completely gone (not null, not empty array)
	if v, exists := hooks["SessionStart"]; exists {
		t.Errorf("SessionStart should be deleted, got %v", v)
	}
	if v, exists := hooks["SessionEnd"]; exists {
		t.Errorf("SessionEnd should be deleted, got %v", v)
	}

	// Verify the raw JSON contains no null values in hooks
	path := filepath.Join(claudeDir, "settings.json")
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), ": null") || strings.Contains(string(raw), ":null") {
		t.Errorf("settings.json contains null value:\n%s", raw)
	}
}

// `cctrace reset` must not delete a hook that merely runs a binary whose name
// ends in "cctrace". The settings guard cannot catch this on its own: it picks
// the hooks to protect with the same predicate, so anything we misclaim is
// excluded from the protected set and removed silently.
func TestRemoveKeepsForeignHookWithCctraceSuffix(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	os.MkdirAll(claudeDir, 0755)

	const foreign = "/opt/tools/my-cctrace sync --all"
	writeJSON(t, filepath.Join(claudeDir, "settings.json"), map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"hooks": []interface{}{
						map[string]interface{}{"command": foreign, "type": "command"},
					},
					"matcher": "",
				},
			},
		},
	})

	p := testProfile(claudeDir)
	if err := ApplyToClaudeSettings(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := RemoveFromClaudeSettings(p); err != nil {
		t.Fatalf("remove: %v", err)
	}

	s := readJSON(t, filepath.Join(claudeDir, "settings.json"))
	hooks, _ := s["hooks"].(map[string]interface{})
	sessionStart, _ := hooks["SessionStart"].([]interface{})

	found := false
	for _, m := range sessionStart {
		matcher, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		entries, _ := matcher["hooks"].([]interface{})
		for _, h := range entries {
			hm, ok := h.(map[string]interface{})
			if !ok {
				continue
			}
			if c, _ := hm["command"].(string); c == foreign {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("reset deleted a hook it does not own (%q); settings now: %#v", foreign, s["hooks"])
	}
}

func TestRemoveCctraceOnlyKeepsOtherHooks(t *testing.T) {
	// When SessionEnd has both cctrace and non-cctrace hooks,
	// remove should delete cctrace hooks but keep the matcher with other hooks.
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	os.MkdirAll(claudeDir, 0755)

	// Pre-existing SessionEnd with a non-cctrace hook
	writeJSON(t, filepath.Join(claudeDir, "settings.json"), map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionEnd": []interface{}{
				map[string]interface{}{
					"hooks": []interface{}{
						map[string]interface{}{"command": "bash ~/cleanup.sh", "type": "command"},
					},
					"matcher": "",
				},
			},
		},
	})

	p := testProfile(claudeDir)

	// Apply adds cctrace hooks alongside existing ones
	if err := ApplyToClaudeSettings(p); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Remove cctrace hooks
	if err := RemoveFromClaudeSettings(p); err != nil {
		t.Fatalf("remove: %v", err)
	}

	s := readJSON(t, filepath.Join(claudeDir, "settings.json"))
	hooks := s["hooks"].(map[string]interface{})

	// SessionEnd should still exist (has non-cctrace hook)
	sessionEnd, ok := hooks["SessionEnd"].([]interface{})
	if !ok || len(sessionEnd) == 0 {
		t.Fatal("SessionEnd should still exist with non-cctrace hook")
	}

	// Only cleanup.sh should remain
	matcher := sessionEnd[0].(map[string]interface{})
	hooksList := matcher["hooks"].([]interface{})
	if len(hooksList) != 1 {
		t.Fatalf("expected 1 hook remaining, got %d", len(hooksList))
	}
	hm := hooksList[0].(map[string]interface{})
	if hm["command"] != "bash ~/cleanup.sh" {
		t.Errorf("wrong hook remaining: %v", hm["command"])
	}

	// SessionStart should be gone (was cctrace-only)
	if _, exists := hooks["SessionStart"]; exists {
		t.Error("SessionStart should be deleted (was cctrace-only)")
	}
}

func TestApplyCreatesFileIfMissing(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	// Don't create dir — Apply should create it
	p := testProfile(claudeDir)

	if err := ApplyToClaudeSettings(p); err != nil {
		t.Fatalf("apply to missing file: %v", err)
	}

	settingsPath := filepath.Join(claudeDir, "settings.json")
	if runtime.GOOS != "windows" {
		info, err := os.Stat(settingsPath)
		if err != nil {
			t.Fatalf("stat settings: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0600 {
			t.Fatalf("settings mode = %o, want 0600", perm)
		}
	}

	s := readJSON(t, settingsPath)
	env := s["env"].(map[string]interface{})
	if env["CLAUDE_CODE_ENABLE_TELEMETRY"] != "1" {
		t.Error("OTEL key not applied to new file")
	}
}

func TestRemoveNoopIfNoFile(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	p := testProfile(claudeDir)

	if err := RemoveFromClaudeSettings(p); err != nil {
		t.Fatalf("remove from missing file should not error: %v", err)
	}
}

func TestClaudeSettingsPathUsesClaudeConfigDir(t *testing.T) {
	// The expected value is built the way the implementation builds it. A literal
	// "/tmp/custom-claude/settings.json" fails on Windows, where filepath.Join
	// produces backslashes -- and the sibling test below already does it this way.
	dir := filepath.FromSlash("/tmp/custom-claude")
	p := &profile.Profile{ClaudeConfigDir: dir}
	path, _ := ClaudeSettingsPath(p)
	want := filepath.Join(dir, "settings.json")
	if path != want {
		t.Errorf("expected %s, got %s", want, path)
	}
}

func TestClaudeSettingsPathDefaultsToHome(t *testing.T) {
	p := &profile.Profile{}
	path, _ := ClaudeSettingsPath(p)
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".claude", "settings.json")
	if path != expected {
		t.Errorf("expected %s, got %s", expected, path)
	}
}

func TestWritePs1(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env.ps1")

	if err := WritePs1(fullProfile(), path); err != nil {
		t.Fatalf("WritePs1: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0600 {
			t.Errorf("expected 0600 permissions, got %o", perm)
		}
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "\r\n") {
		t.Error("env.ps1 should have CRLF line endings")
	}
}
