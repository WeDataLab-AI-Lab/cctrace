package tests

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"cctrace/internal/profile"
)

var (
	sharedBinOnce sync.Once
	sharedBinPath string
	sharedBinDir  string
	sharedBinErr  error
)

// buildBinary returns the path to a cctrace binary built once per test run.
// Every test in this package drives the same immutable binary, so rebuilding it
// per test only multiplied the cost of the slowest step in the package.
// -buildvcs=false: the binary under test never reads its version stamp, and VCS
// stamping fails outright inside a git worktree.
func buildBinary(t *testing.T) string {
	t.Helper()
	sharedBinOnce.Do(func() {
		sharedBinDir, sharedBinErr = os.MkdirTemp("", "cctrace-bin")
		if sharedBinErr != nil {
			return
		}
		bin := filepath.Join(sharedBinDir, "cctrace")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		cmd := exec.Command("go", "build", "-buildvcs=false", "-o", bin, "./cmd/cctrace")
		cmd.Dir = rootDir(t)
		out, err := cmd.CombinedOutput()
		if err != nil {
			sharedBinErr = fmt.Errorf("build failed: %v\n%s", err, out)
			return
		}
		sharedBinPath = bin
	})
	if sharedBinErr != nil {
		t.Fatal(sharedBinErr)
	}
	return sharedBinPath
}

func TestMain(m *testing.M) {
	// These tests exec the real cctrace binary, which resolves its Codex
	// directory from CODEX_CONFIG_DIR (preferred over HOME) and additionally
	// scans CODEX_HOME. runCctrace builds the child environment from
	// os.Environ(), so a developer with either variable set had `go test ./tests`
	// write an [otel] block, auth token included, into their real config.toml.
	// Clearing them in the parent keeps every child process contained, including
	// any exec added later that forgets to scrub its own environment.
	for _, key := range []string{"CODEX_CONFIG_DIR", "CODEX_HOME"} {
		if err := os.Unsetenv(key); err != nil {
			panic(err)
		}
	}

	code := m.Run()
	if sharedBinDir != "" {
		_ = os.RemoveAll(sharedBinDir)
	}
	os.Exit(code)
}

func rootDir(t *testing.T) string {
	t.Helper()
	// Walk up from tests/ to project root
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(dir)
}

// setupHome creates an isolated HOME directory and returns cleanup info.
// initAnswers builds the stdin for `cctrace init`, one answer per prompt in
// prompt order.
//
// It exists in one place because a prompt that is added — or, as happened here,
// made required — shifts every later answer by one, and the shift is silent: the
// wrong answers still produce a plausible profile. Five tests fed a blank line
// for the OTEL endpoint after it stopped accepting one. Init rejected the blank,
// re-asked, and from there "testuser" was stored as the OTEL endpoint, the
// password as the user id, and the final "n" landed on the sync prompt — turning
// sync off in a test named for sync being on.
//
// Only one assertion was specific enough to notice. The other four kept passing
// while exercising a configuration nobody meant to test.
//
// The other half of the trap: some prompts are conditional. The PATH-install
// question is skipped when the binary is already reachable, and the gjc/omo
// questions appear only when ~/.gjc and ~/.omo exist. Feeding an answer for a
// question that is not asked shifts everything after it just as surely as
// missing one -- an extra line is harmless only when nothing follows it.
//
// So this helper covers the prompts a plain `init` always asks, in order, and
// stops. A test that expects conditional prompts appends its own answers and
// must decide, for its own environment, whether the PATH question is among them.
func initAnswers(syncEndpoint, otelEndpoint, userID, password, enableSync, installPath string) string {
	return strings.Join([]string{
		syncEndpoint,
		otelEndpoint,
		userID,
		password,
		enableSync,
		installPath,
	}, "\n") + "\n"
}

func setupHome(t *testing.T) (home string, bin string) {
	t.Helper()
	home = t.TempDir()
	bin = buildBinary(t)
	return
}

// runCctrace runs cctrace with the given args, using fakeHome as HOME.
// stdinInput is fed to stdin.
func runCctrace(t *testing.T, bin, fakeHome string, stdinInput string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(),
		"HOME="+fakeHome,
		"USERPROFILE="+fakeHome,                // Windows
		"SHELL="+os.Getenv("SHELL"),            // preserve for detection
		"XDG_CONFIG_HOME="+fakeHome+"/.config", // for fish
		// Positively bind the child's Codex paths inside the fake home. TestMain
		// already clears these, but the binary writes an auth token into whatever
		// config.toml they resolve to, so state it explicitly here too rather than
		// relying on an absence two files away.
		"CODEX_CONFIG_DIR="+filepath.Join(fakeHome, ".codex"),
		"CODEX_HOME="+filepath.Join(fakeHome, ".codex"),
	)
	if stdinInput != "" {
		cmd.Stdin = strings.NewReader(stdinInput)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// startAuthServer starts a mock HTTP server that responds to POST /api/cli/auth
// with the given user details. All other paths return 404.
func startAuthServer(t *testing.T, name, email, team string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/cli/auth" {
			// Drain request body
			io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"name":%q,"email":%q,"team":%q,"must_change_password":false,"api_token":"test-token"}`,
				name, email, team)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(func() { ts.Close() })
	return ts
}

func TestInitCreatesValidJSON(t *testing.T) {
	home, bin := setupHome(t)

	ts := startAuthServer(t, "Test User", "test@example.com", "engineering")
	// Prompts: sync endpoint, otel endpoint (default), userID, password, sync toggle (y), decline PATH install
	input := initAnswers(ts.URL, ts.URL, "testuser", "testpass", "y", "n")
	out, err := runCctrace(t, bin, home, input, "init")
	if err != nil {
		t.Fatalf("init failed: %v\noutput: %s", err, out)
	}

	// Verify profile.json exists and is valid
	profilePath := filepath.Join(home, ".cctrace", "profile.json")
	data, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("could not read profile.json: %v", err)
	}

	var p profile.Profile
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if p.Version != 1 {
		t.Errorf("expected version 1, got %d", p.Version)
	}
	if p.User.Name != "Test User" {
		t.Errorf("expected name 'Test User', got %q", p.User.Name)
	}
	if p.User.Email != "test@example.com" {
		t.Errorf("expected email 'test@example.com', got %q", p.User.Email)
	}
	if p.User.Team != "engineering" {
		t.Errorf("expected team 'engineering', got %q", p.User.Team)
	}
}

func TestInitEnvShIsSourceable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash not available on Windows")
	}

	home, bin := setupHome(t)

	ts := startAuthServer(t, "Test User", "test@example.com", "engineering")
	// sync no, decline PATH install
	input := initAnswers(ts.URL, ts.URL, "testuser", "testpass", "n", "n")
	out, err := runCctrace(t, bin, home, input, "init")
	if err != nil {
		t.Fatalf("init failed: %v\noutput: %s", err, out)
	}

	// init now writes OTEL env vars to ~/.claude/settings.json instead of env.sh.
	// Verify settings.json contains CLAUDE_CODE_ENABLE_TELEMETRY=1.
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("could not read settings.json: %v", err)
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("invalid settings.json: %v", err)
	}

	env, _ := settings["env"].(map[string]interface{})
	if val, _ := env["CLAUDE_CODE_ENABLE_TELEMETRY"].(string); val != "1" {
		t.Errorf("expected CLAUDE_CODE_ENABLE_TELEMETRY=1 in settings.json, got %q", val)
	}
}

func TestReInitUsesExistingDefaults(t *testing.T) {
	home, bin := setupHome(t)

	// First init — mock server stays alive via t.Cleanup (registered by startAuthServer)
	ts := startAuthServer(t, "Original Name", "original@test.com", "alpha")

	// sync no, decline PATH install
	input1 := initAnswers(ts.URL, ts.URL, "originaluser", "testpass", "n", "n")
	_, err := runCctrace(t, bin, home, input1, "init")
	if err != nil {
		t.Fatal("first init failed:", err)
	}

	// Re-init: accept defaults for sync/otel, re-enter userID (no default), password, sync no, decline PATH install
	input2 := "\n\noriguser\ntestpass\nn\nn\n"
	_, err = runCctrace(t, bin, home, input2, "init")
	if err != nil {
		t.Fatal("re-init failed:", err)
	}

	// Verify profile unchanged
	data, _ := os.ReadFile(filepath.Join(home, ".cctrace", "profile.json"))
	var p profile.Profile
	json.Unmarshal(data, &p)

	if p.User.Name != "Original Name" {
		t.Errorf("expected name preserved as 'Original Name', got %q", p.User.Name)
	}
	if p.User.Email != "original@test.com" {
		t.Errorf("expected email preserved, got %q", p.User.Email)
	}
	if p.User.Team != "alpha" {
		t.Errorf("expected team preserved, got %q", p.User.Team)
	}
}

func TestInitWritesSettingsJSON(t *testing.T) {
	home, bin := setupHome(t)

	ts := startAuthServer(t, "Test User", "test@example.com", "eng")
	// sync yes, decline PATH install
	input := initAnswers(ts.URL, ts.URL, "testuser", "testpass", "y", "n")
	out, err := runCctrace(t, bin, home, input, "init")
	if err != nil {
		t.Fatalf("init failed: %v\noutput: %s", err, out)
	}

	// Verify settings.json has OTEL env vars and sync hooks
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("could not read settings.json: %v", err)
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("invalid settings.json: %v", err)
	}

	env, _ := settings["env"].(map[string]interface{})
	if val, _ := env["CLAUDE_CODE_ENABLE_TELEMETRY"].(string); val != "1" {
		t.Errorf("expected CLAUDE_CODE_ENABLE_TELEMETRY=1, got %q", val)
	}

	// Pin a value that only the right answer can produce. The telemetry flag above
	// is "1" no matter which prompt each answer landed on, so it cannot tell a
	// correct run from a shifted one — when the answers slid by one, this endpoint
	// held the user id with a scheme glued to the front, and every assertion above
	// still passed. A shift now fails here, next to its cause, instead of at whatever
	// unrelated line happens to be specific enough.
	if got, _ := env["OTEL_EXPORTER_OTLP_ENDPOINT"].(string); got != ts.URL {
		t.Fatalf("OTEL endpoint = %q, want %q — the answers are off by one, so every "+
			"assertion below this is testing a configuration nobody asked for", got, ts.URL)
	}

	// Sync hooks should be present (sync was enabled)
	content := string(data)
	if !strings.Contains(content, "cctrace sync") {
		t.Error("expected settings.json to contain cctrace sync hooks when sync enabled")
	}
}

func TestInitDeclineShell(t *testing.T) {
	home, bin := setupHome(t)

	// Create shell profile
	shellProfile := filepath.Join(home, ".zshrc")
	os.WriteFile(shellProfile, []byte("# my zshrc\n"), 0644)

	ts := startAuthServer(t, "Test User", "test@example.com", "eng")
	// sync no, decline PATH install
	input := initAnswers(ts.URL, ts.URL, "testuser", "testpass", "n", "n")
	out, err := runCctrace(t, bin, home, input, "init")
	if err != nil {
		t.Fatalf("init failed: %v\noutput: %s", err, out)
	}

	// settings.json should exist (init writes OTEL env to Claude settings)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if _, err := os.Stat(settingsPath); err != nil {
		t.Error("settings.json should exist after init")
	}

	// Shell profile should NOT be modified (no --auto-install)
	data, _ := os.ReadFile(shellProfile)
	if strings.Contains(string(data), "cctrace") {
		t.Error("shell profile should NOT contain cctrace marker when declined")
	}

	// Sync hooks should NOT be present (sync was declined)
	settingsData, _ := os.ReadFile(settingsPath)
	if strings.Contains(string(settingsData), "cctrace sync") {
		t.Error("settings.json should NOT contain cctrace sync hooks when sync declined")
	}
}

func TestEnvUrlEncoding(t *testing.T) {
	home, bin := setupHome(t)

	// Name and team with spaces come from server response → get URL-encoded in OTEL_RESOURCE_ATTRIBUTES
	ts := startAuthServer(t, "Alice Doe", "alice@example.com", "platform team")
	// sync no, decline PATH install
	input := initAnswers(ts.URL, ts.URL, "alice", "testpass", "n", "n")
	out, err := runCctrace(t, bin, home, input, "init")
	if err != nil {
		t.Fatalf("init failed: %v\noutput: %s", err, out)
	}

	// init now writes env vars to ~/.claude/settings.json; check OTEL_RESOURCE_ATTRIBUTES there.
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("could not read settings.json: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "Alice%20Doe") {
		t.Errorf("expected URL-encoded name with %%20 in settings.json, got:\n%s", content)
	}
	if !strings.Contains(content, "platform%20team") {
		t.Errorf("expected URL-encoded team with %%20 in settings.json, got:\n%s", content)
	}
}
