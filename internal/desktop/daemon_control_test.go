package desktop

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDaemonControlHelloIsOptInAndKeepsLegacyBytes(t *testing.T) {
	fixture := newTestService(t, t.TempDir(), "")
	for _, test := range []struct {
		name    string
		payload any
		want    string
	}{
		{
			name:    "legacy",
			payload: map[string]any{},
			want:    `{"protocol_version":1,"self_update":false,"capabilities":{"fixture_only":true,"progress":false,"cancellation":false}}`,
		},
		{
			name:    "quota selection",
			payload: map[string]bool{"quota_selection": true},
			want:    `{"protocol_version":1,"self_update":false,"capabilities":{"fixture_only":true,"progress":false,"cancellation":false,"quota_selection":true}}`,
		},
		{
			name:    "daemon status",
			payload: map[string]bool{"daemon_status": true},
			want:    `{"protocol_version":1,"self_update":false,"capabilities":{"fixture_only":true,"progress":false,"cancellation":false,"daemon_status":true}}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := call(t, fixture, test.name, "hello", test.payload)
			if response.Error != nil || string(response.Result) != test.want {
				t.Fatalf("hello result=%s error=%+v, want exact bytes %s", response.Result, response.Error, test.want)
			}
		})
	}

	fixtureControl := newDaemonControlFixtureService(t, t.TempDir())
	response := call(t, fixtureControl, "fixture-control", "hello", map[string]bool{"daemon_control": true})
	wantFixtureControl := `{"protocol_version":1,"self_update":false,"capabilities":{"fixture_only":true,"progress":false,"cancellation":false,"daemon_control":false}}`
	if response.Error != nil || string(response.Result) != wantFixtureControl {
		t.Fatalf("fixture control hello=%s error=%+v, want %s", response.Result, response.Error, wantFixtureControl)
	}

	root := t.TempDir()
	writeDaemonProfile(t, root, true, "")
	writeEmptyDaemonStates(t, root)
	service := newDaemonControlTestService(t, root, root,
		func(string) daemonLockState { return daemonLockFree }, nil, nil, 0)
	response = call(t, service, "control", "hello", map[string]bool{"daemon_control": true})
	wantControl := `{"protocol_version":1,"self_update":false,"capabilities":{"fixture_only":false,"progress":false,"cancellation":false,"daemon_control":true}}`
	if response.Error != nil || string(response.Result) != wantControl {
		t.Fatalf("provider control hello=%s error=%+v, want %s", response.Result, response.Error, wantControl)
	}
}

func TestDaemonControlCapabilityRequiresSupportedRootAndInstalledCLI(t *testing.T) {
	root := t.TempDir()
	writeDaemonProfile(t, root, true, "")
	writeEmptyDaemonStates(t, root)
	noCLI := newDaemonControlTestService(t, root, root,
		func(string) daemonLockState { return daemonLockFree }, nil,
		func(string) (string, error) { return "", errors.New("synthetic no CLI") }, 0)
	assertDaemonControlCapability(t, noCLI, false)
	if response := call(t, noCLI, "no-cli", "daemon.restart", map[string]string{"profile_id": "default"}); response.Error == nil || response.Error.Code != "feature_unavailable" {
		t.Fatalf("restart without installed CLI=%+v", response)
	}

	otherRoot := t.TempDir()
	mismatched := newDaemonControlTestService(t, root, otherRoot,
		func(string) daemonLockState { return daemonLockFree }, nil, nil, 0)
	assertDaemonControlCapability(t, mismatched, false)
	nonNormalRoot := root + string(os.PathSeparator) + ".." + string(os.PathSeparator) + filepath.Base(root)
	nonNormal := newDaemonControlTestService(t, nonNormalRoot, root,
		func(string) daemonLockState { return daemonLockFree }, nil, nil, 0)
	assertDaemonControlCapability(t, nonNormal, false)

	actualRoot := t.TempDir()
	writeDaemonProfile(t, actualRoot, true, "")
	writeEmptyDaemonStates(t, actualRoot)
	rootAlias := filepath.Join(t.TempDir(), "profile-root-link")
	if err := os.Symlink(actualRoot, rootAlias); err != nil {
		t.Fatal(err)
	}
	symlinkRoot := newDaemonControlTestService(t, rootAlias, rootAlias,
		func(string) daemonLockState { return daemonLockFree }, nil, nil, 0)
	assertDaemonControlCapability(t, symlinkRoot, false)
}

func TestServeRequiresDaemonControlNegotiationAndPreservesQuotaNegotiation(t *testing.T) {
	root := t.TempDir()
	writeDaemonProfile(t, root, true, "")
	writeEmptyDaemonStates(t, root)
	var state atomic.Int32
	state.Store(int32(daemonLockFree))
	service := newDaemonControlTestService(t, root, root,
		func(string) daemonLockState { return daemonLockState(state.Load()) },
		func(_ context.Context, _ string, args, _ []string) error {
			if slicesEqual(args, []string{"sync", "start"}) {
				state.Store(int32(daemonLockHeld))
			}
			return nil
		}, nil, 0)
	type helloRequest struct {
		id      string
		payload map[string]bool
	}
	for _, ordering := range []struct {
		name  string
		hello []helloRequest
	}{
		{name: "GUI control then status then quota", hello: []helloRequest{{"control-cap", map[string]bool{"daemon_control": true}}, {"status-cap", map[string]bool{"daemon_status": true}}, {"quota", map[string]bool{"quota_selection": true}}}},
		{name: "quota then status then control", hello: []helloRequest{{"quota", map[string]bool{"quota_selection": true}}, {"status-cap", map[string]bool{"daemon_status": true}}, {"control-cap", map[string]bool{"daemon_control": true}}}},
	} {
		t.Run(ordering.name, func(t *testing.T) {
			state.Store(int32(daemonLockFree))
			inputClient, inputServer := net.Pipe()
			outputServer, outputClient := net.Pipe()
			serveDone := make(chan error, 1)
			go func() { serveDone <- Serve(context.Background(), service, inputServer, outputServer) }()
			t.Cleanup(func() {
				_ = inputClient.Close()
				_ = inputServer.Close()
				_ = outputClient.Close()
				_ = outputServer.Close()
			})
			if err := outputClient.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Fatal(err)
			}
			scanner := bufio.NewScanner(outputClient)
			for _, hello := range ordering.hello {
				writeDaemonControlRequest(t, inputClient, hello.id, "hello", hello.payload)
				if response := readDaemonControlResponse(t, scanner); response.Error != nil {
					t.Fatalf("hello %s failed: %+v", hello.id, response.Error)
				}
			}
			writeDaemonControlRequest(t, inputClient, "selection", "quota.selection.read", map[string]any{})
			if response := readDaemonControlResponse(t, scanner); response.Error != nil {
				t.Fatalf("quota read after hello ordering failed: %+v", response.Error)
			}
			writeDaemonControlRequest(t, inputClient, "restart", "daemon.restart", map[string]string{"profile_id": "default"})
			response := readDaemonControlResponse(t, scanner)
			if response.Error != nil || daemonControlJSON(t, response.Result) != `{"profile_id":"default","state":"restarted"}` {
				t.Fatalf("restart after hello ordering=%s error=%+v", daemonControlJSON(t, response.Result), response.Error)
			}
			_ = inputClient.Close()
			if err := <-serveDone; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestServeRejectsUnnegotiatedDaemonRestartWithoutRunningCLI(t *testing.T) {
	root := t.TempDir()
	writeDaemonProfile(t, root, true, "")
	writeEmptyDaemonStates(t, root)
	var calls atomic.Int32
	service := newDaemonControlTestService(t, root, root,
		func(string) daemonLockState { return daemonLockFree },
		func(context.Context, string, []string, []string) error { calls.Add(1); return nil }, nil, 0)
	input := daemonControlRequestLine(t, "legacy", "hello", map[string]any{}) +
		daemonControlRequestLine(t, "restart", "daemon.restart", map[string]string{"profile_id": "default"})
	var output bytes.Buffer
	if err := Serve(context.Background(), service, strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	responses := daemonControlResponses(t, output.Bytes())
	if len(responses) != 2 || responses[1].Error == nil || responses[1].Error.Code != "capability_not_negotiated" || calls.Load() != 0 {
		t.Fatalf("unnegotiated restart responses=%+v command calls=%d", responses, calls.Load())
	}
}

func TestDaemonRestartRoutesDefaultAndNamedDefaultSeparately(t *testing.T) {
	root := t.TempDir()
	writeDaemonProfile(t, root, true, "")
	writeEmptyDaemonStates(t, root)
	namedDefault := filepath.Join(root, "profiles", "default")
	writeDaemonProfile(t, namedDefault, true, "")
	writeEmptyDaemonStates(t, namedDefault)

	var stateMu sync.Mutex
	states := map[string]daemonLockState{"default": daemonLockFree, "named:default": daemonLockFree}
	probe := func(path string) daemonLockState {
		id := "default"
		if filepath.Dir(path) == namedDefault {
			id = "named:default"
		}
		stateMu.Lock()
		defer stateMu.Unlock()
		return states[id]
	}
	var callsMu sync.Mutex
	var calls [][]string
	runner := func(_ context.Context, _ string, args, env []string) error {
		callsMu.Lock()
		calls = append(calls, append([]string(nil), args...))
		callsMu.Unlock()
		if !hasEnv(env, "HOME", filepath.Dir(root)) {
			return errors.New("unexpected HOME")
		}
		for _, key := range []string{"CCTRACE_PROFILE", "CCTRACE_SYNC_ENDPOINT", "OTEL_EXPORTER_OTLP_ENDPOINT", "CLAUDE_CONFIG_DIR", "CODEX_HOME"} {
			if hasEnvKey(env, key) {
				return fmt.Errorf("environment leaked %s", key)
			}
		}
		if !hasEnv(env, "HTTPS_PROXY", "http://proxy.invalid:8080") {
			return errors.New("runtime proxy was not preserved")
		}
		id := "default"
		if hasArgPair(args, "--profile", "default") {
			id = "named:default"
		}
		stateMu.Lock()
		if len(args) > 1 && args[1] == "--stop" {
			states[id] = daemonLockFree
		} else {
			states[id] = daemonLockHeld
		}
		stateMu.Unlock()
		return nil
	}
	service := newDaemonControlTestService(t, root, root, probe, runner, nil, 0)
	t.Setenv("CCTRACE_PROFILE", "poison-profile")
	t.Setenv("CCTRACE_SYNC_ENDPOINT", "https://poison.invalid")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://poison.invalid")
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:8080")

	for _, test := range []struct {
		id        string
		initial   daemonLockState
		wantCalls [][]string
	}{
		{id: "default", initial: daemonLockFree, wantCalls: [][]string{{"sync", "start"}}},
		{id: "named:default", initial: daemonLockHeld, wantCalls: [][]string{
			{"sync", "--stop", "--profile", "default"},
			{"sync", "start", "--profile", "default"},
		}},
	} {
		stateMu.Lock()
		states[test.id] = test.initial
		stateMu.Unlock()
		callsMu.Lock()
		firstCall := len(calls)
		callsMu.Unlock()
		response := call(t, service, "restart-"+strings.ReplaceAll(test.id, ":", "-"), "daemon.restart", map[string]string{"profile_id": test.id})
		if response.Error != nil || string(response.Result) != fmt.Sprintf(`{"profile_id":%q,"state":"restarted"}`, test.id) {
			t.Fatalf("profile %q response=%s error=%+v", test.id, response.Result, response.Error)
		}
		callsMu.Lock()
		got := make([][]string, len(calls)-firstCall)
		for i := range got {
			got[i] = append([]string(nil), calls[firstCall+i]...)
		}
		callsMu.Unlock()
		if !commandCallsEqual(got, test.wantCalls) {
			t.Fatalf("profile %q CLI args=%q want=%q", test.id, got, test.wantCalls)
		}
	}
}

func TestDaemonControlEnvironmentKeepsRuntimeEnvAndDropsOverrides(t *testing.T) {
	for key, value := range map[string]string{
		"PATH":                        "/synthetic/bin:/usr/bin",
		"USER":                        "desktop-user",
		"LOGNAME":                     "desktop-user",
		"SHELL":                       "/bin/zsh",
		"HTTP_PROXY":                  "http://http-proxy.invalid:8080",
		"HTTPS_PROXY":                 "http://https-proxy.invalid:8080",
		"NO_PROXY":                    "localhost,.internal",
		"ALL_PROXY":                   "socks5://all-proxy.invalid:1080",
		"SSL_CERT_FILE":               "/synthetic/certs/ca.pem",
		"SSL_CERT_DIR":                "/synthetic/certs",
		"CCTRACE_PROFILE":             "named:attacker",
		"CCTRACE_ENDPOINT":            "https://override.invalid",
		"CCTRACE_SYNC_ENDPOINT":       "https://override.invalid",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "https://override.invalid",
		"CLAUDE_CONFIG_DIR":           "/synthetic/account/claude",
		"CODEX_HOME":                  "/synthetic/account/codex",
		"ANTHROPIC_API_KEY":           "synthetic-secret",
		"OPENAI_API_KEY":              "synthetic-secret",
		"ORCA_REVIEW_SECRET":          "synthetic-secret",
	} {
		t.Setenv(key, value)
	}
	t.Setenv("HOME", "/synthetic/unrelated-home")
	root := filepath.Join(string(os.PathSeparator), "synthetic", "provider", "profiles", "default")
	env := daemonControlEnvironment(root)
	for _, key := range []string{"PATH", "USER", "LOGNAME", "SHELL", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if !hasEnvKey(env, key) {
			t.Errorf("runtime environment key %s was dropped", key)
		}
	}
	for _, key := range []string{"CCTRACE_PROFILE", "CCTRACE_ENDPOINT", "CCTRACE_SYNC_ENDPOINT", "OTEL_EXPORTER_OTLP_ENDPOINT", "CLAUDE_CONFIG_DIR", "CODEX_HOME", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "ORCA_REVIEW_SECRET"} {
		if hasEnvKey(env, key) {
			t.Errorf("sensitive environment key %s was retained", key)
		}
	}
	if !hasEnv(env, "PATH", "/synthetic/bin:/usr/bin") || !hasEnv(env, "HTTPS_PROXY", "http://https-proxy.invalid:8080") ||
		!hasEnv(env, "HOME", filepath.Dir(root)) {
		t.Fatal("safe runtime environment values or canonical HOME were not preserved")
	}
}

func TestDaemonRestartRejectsDisabledUnknownAndSymlinkProfiles(t *testing.T) {
	cases := []struct {
		name     string
		enabled  bool
		state    daemonLockState
		symlink  bool
		wantCode string
	}{
		{name: "disabled", enabled: false, state: daemonLockFree, wantCode: "daemon_profile_unavailable"},
		{name: "unknown lock", enabled: true, state: daemonLockUnknown, wantCode: "daemon_state_unknown"},
		{name: "symlink profile", enabled: true, state: daemonLockFree, symlink: true, wantCode: "daemon_state_unknown"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeDaemonProfile(t, root, test.enabled, "")
			writeEmptyDaemonStates(t, root)
			if test.symlink {
				profilePath := filepath.Join(root, "profile.json")
				target := filepath.Join(t.TempDir(), "profile-target.json")
				if err := os.Rename(profilePath, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, profilePath); err != nil {
					t.Fatal(err)
				}
			}
			var calls atomic.Int32
			service := newDaemonControlTestService(t, root, root,
				func(string) daemonLockState { return test.state },
				func(context.Context, string, []string, []string) error { calls.Add(1); return nil }, nil, 0)
			response := call(t, service, "restart", "daemon.restart", map[string]string{"profile_id": "default"})
			if response.Error == nil || response.Error.Code != test.wantCode || calls.Load() != 0 {
				t.Fatalf("response=%+v calls=%d", response, calls.Load())
			}
		})
	}
}

func TestDaemonRestartRejectsPersistedServerFailureBeforeCLI(t *testing.T) {
	for _, bodyLimit := range []bool{false, true} {
		name := "server failure"
		if bodyLimit {
			name = "server failure with body limit block"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeDaemonProfile(t, root, true, "")
			files := map[string]daemonFileState{}
			if bodyLimit {
				files["/synthetic/session"] = daemonFileState{BlockedByBodyLimit: true}
			}
			now := time.Now().UTC()
			writeDaemonStates(t, root, daemonStateFile{
				Files: files,
				TransportFailure: &daemonTransportFailure{
					Class: "server", FirstFailedAt: now.Add(-time.Minute), LastFailedAt: now, FilesFailed: 1,
				},
			})
			var calls atomic.Int32
			service := newDaemonControlTestService(t, root, root,
				func(string) daemonLockState { return daemonLockFree },
				func(context.Context, string, []string, []string) error { calls.Add(1); return nil }, nil, 0)
			response := call(t, service, "rate-limited", "daemon.restart", map[string]string{"profile_id": "default"})
			if response.Error == nil || response.Error.Code != "daemon_server_rate_limited" ||
				!strings.Contains(response.Error.Message, "wait") || calls.Load() != 0 {
				t.Fatalf("server failure response=%+v CLI calls=%d", response.Error, calls.Load())
			}
		})
	}
}

func TestDaemonRestartRequiresCurrentStoppedStateAndExactPayload(t *testing.T) {
	t.Run("state change before exec", func(t *testing.T) {
		root := t.TempDir()
		writeDaemonProfile(t, root, true, "")
		writeEmptyDaemonStates(t, root)
		var probes atomic.Int32
		var calls atomic.Int32
		service := newDaemonControlTestService(t, root, root,
			func(string) daemonLockState {
				if probes.Add(1) == 1 {
					return daemonLockFree
				}
				return daemonLockHeld
			},
			func(context.Context, string, []string, []string) error { calls.Add(1); return nil }, nil, 0)
		response := call(t, service, "changed", "daemon.restart", map[string]string{"profile_id": "default"})
		if response.Error == nil || response.Error.Code != "daemon_state_changed" || calls.Load() != 0 {
			t.Fatalf("revalidated state response=%+v command calls=%d", response, calls.Load())
		}
	})

	t.Run("extra payload field", func(t *testing.T) {
		root := t.TempDir()
		writeDaemonProfile(t, root, true, "")
		writeEmptyDaemonStates(t, root)
		var calls atomic.Int32
		service := newDaemonControlTestService(t, root, root,
			func(string) daemonLockState { return daemonLockFree },
			func(context.Context, string, []string, []string) error { calls.Add(1); return nil }, nil, 0)
		response := call(t, service, "extra", "daemon.restart", map[string]any{"profile_id": "default", "extra": true})
		if response.Error == nil || response.Error.Code != "invalid_payload" || calls.Load() != 0 {
			t.Fatalf("extra-field payload response=%+v command calls=%d", response, calls.Load())
		}
	})
}

func TestDaemonRestartStopsOnlyRunningProfileAndNeverStartsAfterStopFailure(t *testing.T) {
	t.Run("stopped profile skips stop", func(t *testing.T) {
		root := t.TempDir()
		writeDaemonProfile(t, root, true, "")
		writeEmptyDaemonStates(t, root)
		var state atomic.Int32
		state.Store(int32(daemonLockFree))
		var calls [][]string
		service := newDaemonControlTestService(t, root, root,
			func(string) daemonLockState { return daemonLockState(state.Load()) },
			func(_ context.Context, _ string, args, _ []string) error {
				calls = append(calls, append([]string(nil), args...))
				state.Store(int32(daemonLockHeld))
				return nil
			}, nil, 0)
		response := call(t, service, "restart", "daemon.restart", map[string]string{"profile_id": "default"})
		if response.Error != nil || !commandCallsEqual(calls, [][]string{{"sync", "start"}}) {
			t.Fatalf("response=%+v calls=%q", response.Error, calls)
		}
	})

	t.Run("stop failure prevents start and is redacted", func(t *testing.T) {
		root := t.TempDir()
		writeDaemonProfile(t, root, true, "")
		writeEmptyDaemonStates(t, root)
		var calls [][]string
		service := newDaemonControlTestService(t, root, root,
			func(string) daemonLockState { return daemonLockHeld },
			func(_ context.Context, _ string, args, _ []string) error {
				calls = append(calls, append([]string(nil), args...))
				return errors.New("private-token /private/profile path")
			}, nil, 0)
		response := call(t, service, "restart", "daemon.restart", map[string]string{"profile_id": "default"})
		encoded, _ := json.Marshal(response)
		if response.Error == nil || response.Error.Code != "daemon_stop_failed" ||
			!commandCallsEqual(calls, [][]string{{"sync", "--stop"}}) ||
			strings.Contains(string(encoded), "private-token") || strings.Contains(string(encoded), "/private/") {
			t.Fatalf("response=%s calls=%q", encoded, calls)
		}
	})
}

func TestDaemonRestartDeadlineAndPositiveRunningConfirmation(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		root := t.TempDir()
		writeDaemonProfile(t, root, true, "")
		writeEmptyDaemonStates(t, root)
		service := newDaemonControlTestService(t, root, root,
			func(string) daemonLockState { return daemonLockFree },
			func(ctx context.Context, _ string, _ []string, _ []string) error { <-ctx.Done(); return ctx.Err() }, nil, 20*time.Millisecond)
		response := call(t, service, "deadline", "daemon.restart", map[string]string{"profile_id": "default"})
		if response.Error == nil || response.Error.Code != "daemon_control_timeout" {
			t.Fatalf("deadline response=%+v", response)
		}
	})

	t.Run("start success must still prove lock held", func(t *testing.T) {
		root := t.TempDir()
		writeDaemonProfile(t, root, true, "")
		writeEmptyDaemonStates(t, root)
		service := newDaemonControlTestService(t, root, root,
			func(string) daemonLockState { return daemonLockFree },
			func(context.Context, string, []string, []string) error { return nil }, nil, 0)
		response := call(t, service, "unconfirmed", "daemon.restart", map[string]string{"profile_id": "default"})
		if response.Error == nil || response.Error.Code != "daemon_start_unconfirmed" {
			t.Fatalf("unconfirmed start response=%+v", response)
		}
	})
}

func TestServeDaemonRestartIsAsyncResponsiveAndSerializesMutations(t *testing.T) {
	root := t.TempDir()
	writeDaemonProfile(t, root, true, "")
	writeEmptyDaemonStates(t, root)
	var state atomic.Int32
	state.Store(int32(daemonLockHeld))
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	runner := func(ctx context.Context, _ string, args, _ []string) error {
		if slicesEqual(args, []string{"sync", "--stop"}) {
			startOnce.Do(func() { close(started) })
			select {
			case <-release:
				state.Store(int32(daemonLockFree))
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if slicesEqual(args, []string{"sync", "start"}) {
			state.Store(int32(daemonLockHeld))
			return nil
		}
		return errors.New("unexpected synthetic command")
	}
	service := newDaemonControlTestService(t, root, root,
		func(string) daemonLockState { return daemonLockState(state.Load()) }, runner, nil, 0)

	inputClient, inputServer := net.Pipe()
	outputServer, outputClient := net.Pipe()
	serveDone := make(chan error, 1)
	go func() { serveDone <- Serve(context.Background(), service, inputServer, outputServer) }()
	t.Cleanup(func() {
		_ = inputClient.Close()
		_ = inputServer.Close()
		_ = outputClient.Close()
		_ = outputServer.Close()
	})
	if err := outputClient.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(outputClient)
	writeDaemonControlRequest(t, inputClient, "quota-cap", "hello", map[string]bool{"quota_selection": true})
	if response := readDaemonControlResponse(t, scanner); response.Error != nil {
		t.Fatalf("quota hello error=%+v", response.Error)
	}
	writeDaemonControlRequest(t, inputClient, "control", "hello", map[string]bool{"daemon_control": true})
	if response := readDaemonControlResponse(t, scanner); response.Error != nil {
		t.Fatalf("control hello error=%+v", response.Error)
	}
	writeDaemonControlRequest(t, inputClient, "first", "daemon.restart", map[string]string{"profile_id": "default"})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("restart command did not start")
	}

	writeDaemonControlRequest(t, inputClient, "quota-read", "quota.selection.read", map[string]any{})
	quotaRead := readDaemonControlResponse(t, scanner)
	if quotaRead.ID != "quota-read" || quotaRead.Error != nil {
		t.Fatalf("quota response while restart held=%+v", quotaRead)
	}
	writeDaemonControlRequest(t, inputClient, "status", "daemon.status.read", map[string]any{})
	status := readDaemonControlResponse(t, scanner)
	if status.ID != "status" || status.Error != nil {
		t.Fatalf("status response while restart held=%+v", status)
	}
	writeDaemonControlRequest(t, inputClient, "second", "daemon.restart", map[string]string{"profile_id": "default"})
	busy := readDaemonControlResponse(t, scanner)
	if busy.ID != "second" || busy.Error == nil || busy.Error.Code != "daemon_busy" {
		t.Fatalf("concurrent restart response=%+v", busy)
	}

	close(release)
	first := readDaemonControlResponse(t, scanner)
	if first.ID != "first" || first.Error != nil || daemonControlJSON(t, first.Result) != `{"profile_id":"default","state":"restarted"}` {
		t.Fatalf("restart completion=%+v", first)
	}
	_ = inputClient.Close()
	if err := <-serveDone; err != nil {
		t.Fatal(err)
	}
}

func TestServeCancelsHeldDaemonRestartOnInputEOF(t *testing.T) {
	root := t.TempDir()
	writeDaemonProfile(t, root, true, "")
	writeEmptyDaemonStates(t, root)
	var state atomic.Int32
	state.Store(int32(daemonLockHeld))
	started := make(chan struct{})
	cancelObserved := make(chan struct{})
	release := make(chan struct{})
	var startCalls atomic.Int32
	var startOnce sync.Once
	var cancelOnce sync.Once
	var releaseOnce sync.Once
	runner := func(ctx context.Context, _ string, args, _ []string) error {
		if slicesEqual(args, []string{"sync", "--stop"}) {
			startOnce.Do(func() { close(started) })
			select {
			case <-ctx.Done():
				cancelOnce.Do(func() { close(cancelObserved) })
				return ctx.Err()
			case <-release:
				state.Store(int32(daemonLockFree))
				return nil
			}
		}
		if slicesEqual(args, []string{"sync", "start"}) {
			startCalls.Add(1)
			state.Store(int32(daemonLockHeld))
			return nil
		}
		return errors.New("unexpected synthetic command")
	}
	service := newDaemonControlTestService(t, root, root,
		func(string) daemonLockState { return daemonLockState(state.Load()) }, runner, nil, 0)
	inputClient, inputServer := net.Pipe()
	var output bytes.Buffer
	serveDone := make(chan error, 1)
	go func() { serveDone <- Serve(context.Background(), service, inputServer, &output) }()
	t.Cleanup(func() {
		_ = inputClient.Close()
		_ = inputServer.Close()
		releaseOnce.Do(func() { close(release) })
	})
	writeDaemonControlRequest(t, inputClient, "control", "hello", map[string]bool{"daemon_control": true})
	writeDaemonControlRequest(t, inputClient, "restart", "daemon.restart", map[string]string{"profile_id": "default"})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("stop command did not start")
	}
	_ = inputClient.Close()
	wasCancelled := false
	select {
	case <-cancelObserved:
		wasCancelled = true
	case <-time.After(500 * time.Millisecond):
		releaseOnce.Do(func() { close(release) })
	}
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve returned error after EOF: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return promptly after EOF")
	}
	if !wasCancelled || startCalls.Load() != 0 {
		t.Fatalf("EOF cancellation observed=%v start calls=%d", wasCancelled, startCalls.Load())
	}
}

func newDaemonControlTestService(t *testing.T, daemonRoot, defaultRoot string, probe func(string) daemonLockState,
	runner func(context.Context, string, []string, []string) error,
	resolver func(string) (string, error), timeout time.Duration) *Service {
	t.Helper()
	if resolver == nil {
		resolver = func(string) (string, error) { return "/synthetic/cctrace", nil }
	}
	dependencies := &daemonControlDependencies{
		profileDefaultDir: func() string { return defaultRoot },
		resolveCLI:        resolver,
		runCommand:        runner,
		lockProbe:         probe,
		timeout:           timeout,
	}
	service, err := New(Config{
		AppSupportRoot:    t.TempDir(),
		DaemonRoot:        daemonRoot,
		ProviderMode:      true,
		daemonControlDeps: dependencies,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func newDaemonControlFixtureService(t *testing.T, defaultRoot string) *Service {
	t.Helper()
	fixtureRoot := t.TempDir()
	dependencies := &daemonControlDependencies{
		profileDefaultDir: func() string { return defaultRoot },
		resolveCLI:        func(string) (string, error) { return "/synthetic/cctrace", nil },
		runCommand:        func(context.Context, string, []string, []string) error { return nil },
		lockProbe:         func(string) daemonLockState { return daemonLockFree },
	}
	service, err := New(Config{AppSupportRoot: t.TempDir(), FixtureRoot: fixtureRoot, daemonControlDeps: dependencies})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func assertDaemonControlCapability(t *testing.T, service *Service, want bool) {
	t.Helper()
	response := call(t, service, "control", "hello", map[string]bool{"daemon_control": true})
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	var got struct {
		Capabilities struct {
			DaemonControl bool `json:"daemon_control"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(response.Result, &got); err != nil || got.Capabilities.DaemonControl != want {
		t.Fatalf("daemon_control capability=%+v want=%v error=%v", got.Capabilities, want, err)
	}
}

func daemonControlRequestLine(t *testing.T, id, op string, payload any) string {
	t.Helper()
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	requestJSON, err := json.Marshal(Request{Version: ProtocolVersion, Kind: "request", ID: id, Op: op, Payload: payloadJSON})
	if err != nil {
		t.Fatal(err)
	}
	return string(requestJSON) + "\n"
}

func daemonControlResponses(t *testing.T, data []byte) []Response {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	responses := make([]Response, 0)
	for {
		var response Response
		if err := decoder.Decode(&response); err != nil {
			if errors.Is(err, io.EOF) {
				return responses
			}
			t.Fatal(err)
		}
		responses = append(responses, response)
	}
}

func writeDaemonControlRequest(t *testing.T, writer io.Writer, id, op string, payload any) {
	t.Helper()
	if _, err := io.WriteString(writer, daemonControlRequestLine(t, id, op, payload)); err != nil {
		t.Fatal(err)
	}
}

func readDaemonControlResponse(t *testing.T, scanner *bufio.Scanner) Response {
	t.Helper()
	if !scanner.Scan() {
		t.Fatalf("read daemon response: %v", scanner.Err())
	}
	var response Response
	if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func daemonControlJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func hasEnv(env []string, key, value string) bool {
	for _, entry := range env {
		name, got, ok := strings.Cut(entry, "=")
		if ok && name == key && got == value {
			return true
		}
	}
	return false
}

func hasEnvKey(env []string, key string) bool {
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if name == key {
			return true
		}
	}
	return false
}

func hasArgPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func slicesEqual[T comparable](left, right []T) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func commandCallsEqual(left, right [][]string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if !slicesEqual(left[i], right[i]) {
			return false
		}
	}
	return true
}
