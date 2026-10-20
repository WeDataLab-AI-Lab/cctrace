package tests

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cctrace/internal/codexconfig"
	"cctrace/internal/envgen"
	"cctrace/internal/profile"
	"cctrace/internal/serverpolicy"
)

func TestServerPolicyBinary(t *testing.T) {
	t.Parallel()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// The build trusts both keys, as a release does: the first transition is
	// signed by the update key and every later one by the policy key.
	policyPub, policyKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fixture := t.TempDir()
	bin := filepath.Join(fixture, "cctrace")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.CommandContext(t.Context(), "go", "build", "-buildvcs=false", "-ldflags", "-X main.updatePublicKeyBase64="+base64.StdEncoding.EncodeToString(pub)+" -X main.policyPublicKeyBase64="+base64.StdEncoding.EncodeToString(policyPub), "-o", bin, "./cmd/cctrace")
	build.Dir = rootDir(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	home := filepath.Join(fixture, "home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	childEnv := []string{"HOME=" + home, "USERPROFILE=" + home, "APPDATA=" + filepath.Join(home, "appdata"), "XDG_CONFIG_HOME=" + filepath.Join(home, "config"), "CODEX_HOME=" + filepath.Join(home, ".codex"), "CODEX_CONFIG_DIR=" + filepath.Join(home, ".codex"), "CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude"), "PATH=" + fixture, "SYSTEMROOT=" + os.Getenv("SYSTEMROOT")}
	run := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = childEnv
		return cmd.CombinedOutput()
	}
	runtimePath := filepath.Join(home, ".cctrace", "profiles", "fixture", "sync-runtime.json")
	type watcherState struct {
		PID        int           `json:"pid"`
		InstanceID string        `json:"instance_id"`
		StartedAt  time.Time     `json:"started_at"`
		Interval   time.Duration `json:"interval"`
	}
	// Keep the actual runtime sampled at each fixture boundary. A successful
	// policy report alone cannot explain whether the old lease exited, a new
	// identity was published, or runtime metadata could not be read.
	var runtimeMu sync.Mutex
	var runtimeTrace []string
	lastRuntimeObservation := ""
	observeRuntime := func(stage string) watcherState {
		var state watcherState
		data, readErr := os.ReadFile(runtimePath)
		var decodeErr error
		if readErr == nil {
			decodeErr = json.Unmarshal(data, &state)
		}
		observation := fmt.Sprintf("pid=%d instance=%q started=%s interval=%s read=%v decode=%v", state.PID, state.InstanceID, state.StartedAt.Format(time.RFC3339Nano), state.Interval, readErr, decodeErr)
		runtimeMu.Lock()
		defer runtimeMu.Unlock()
		if stage != "poll" || observation != lastRuntimeObservation {
			runtimeTrace = append(runtimeTrace, fmt.Sprintf("%s %s %s", time.Now().UTC().Format(time.RFC3339Nano), stage, observation))
			lastRuntimeObservation = observation
		}
		return state
	}
	startupPolicyGate := make(chan struct{})
	var gateStartup atomic.Bool
	var policyRequests atomic.Int64
	uploads := make(chan string, 16)
	var reads atomic.Int64
	var bundle atomic.Value
	bundle.Store([]byte(nil))
	// Set, the policy route is served by the server's own handler instead.
	var policyHandler atomic.Value
	// What the binary reports about itself after each `config policy apply`.
	type policyReportSeen struct {
		tls  bool
		body struct {
			InstallationID  string `json:"installation_id"`
			Profile         string `json:"profile"`
			SyncEndpoint    string `json:"sync_endpoint"`
			AppliedSequence uint64 `json:"applied_sequence"`
			CheckResult     string `json:"check_result"`
			CheckSequence   uint64 `json:"check_sequence"`
		}
	}
	var reportsMu sync.Mutex
	var reports []policyReportSeen
	lastReport := func() (policyReportSeen, bool) {
		reportsMu.Lock()
		defer reportsMu.Unlock()
		if len(reports) == 0 {
			return policyReportSeen{}, false
		}
		return reports[len(reports)-1], true
	}
	t.Cleanup(func() {
		if t.Failed() {
			reportsMu.Lock()
			defer reportsMu.Unlock()
			t.Logf("policy reports received: %+v", reports)
		}
	})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/client-config-policy" {
			policyRequests.Add(1)
			if gateStartup.Load() {
				observeRuntime("policy gate entered profile=" + r.URL.Query().Get("profile"))
				select {
				case <-startupPolicyGate:
					observeRuntime("policy gate released profile=" + r.URL.Query().Get("profile"))
				case <-r.Context().Done():
					observeRuntime("policy gate cancelled profile=" + r.URL.Query().Get("profile"))
					return
				}
			}
			if h, ok := policyHandler.Load().(http.HandlerFunc); ok {
				h(w, r)
				return
			}
			raw := bundle.Load().([]byte)
			if len(raw) == 0 {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(raw)
			return
		}
		if r.URL.Path == "/api/health" || r.URL.Path == "/api/version" || r.URL.Path == "/v1/logs" || r.URL.Path == "/v1/metrics" {
			if strings.HasPrefix(r.URL.Path, "/v1/") && r.Header.Get("Authorization") != "Bearer synthetic-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"status":"ok","version":"dev"}`))
			return
		}
		if r.URL.Path == "/api/sync" {
			if r.Header.Get("Authorization") != "Bearer synthetic-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			body, _ := io.ReadAll(r.Body)
			if r.TLS != nil && bytes.Contains(body, []byte("policy-session")) {
				select {
				case uploads <- string(body):
				default:
				}
			}
			_, _ = w.Write([]byte(`{"inserted":1}`))
			return
		}
		if r.URL.Path == "/api/client-policy-report" {
			if r.Header.Get("Authorization") != "Bearer synthetic-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			seen := policyReportSeen{tls: r.TLS != nil}
			_ = json.NewDecoder(r.Body).Decode(&seen.body)
			reportsMu.Lock()
			reports = append(reports, seen)
			reportsMu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/api/open/v1/sessions" && r.TLS != nil && r.Header.Get("Authorization") == "Bearer synthetic-read-token" {
			reads.Add(1)
			_, _ = w.Write([]byte(`{"sessions":[],"total":0}`))
			return
		}
		http.NotFound(w, r)
	})
	bootstrap := httptest.NewServer(handler)
	defer bootstrap.Close()
	secure, ca := serverPolicyTLSServer(t, handler)
	defer secure.Close()
	rotatedServer, newCA := serverPolicyTLSServer(t, handler)
	defer rotatedServer.Close()
	profilePath := filepath.Join(home, ".cctrace", "profiles", "fixture", "profile.json")
	p := &profile.Profile{Version: 1, User: profile.UserInfo{ID: "fixture", Name: "Fixture", Email: "fixture@example.test"}, Server: profile.ServerInfo{Endpoint: bootstrap.URL, SyncEndpoint: bootstrap.URL, Protocol: "http/protobuf", AuthToken: "synthetic-token", ReadToken: "synthetic-read-token"}, Options: profile.ProfileOptions{SyncEnabled: true, CodexSyncEnabled: true}, ClaudeConfigDir: filepath.Join(home, ".claude")}
	if err := profile.SaveTo(p, profilePath); err != nil {
		t.Fatal(err)
	}
	if err := envgen.ApplyToClaudeSettings(p); err != nil {
		t.Fatal(err)
	}
	codexDir := filepath.Join(home, ".codex")
	if err := codexconfig.WriteOtelBlock(codexDir, p.Server.Endpoint, p.Server.AuthToken, ""); err != nil {
		t.Fatal(err)
	}
	defaultProfile := *p
	defaultProfile.ClaudeConfigDir = filepath.Join(home, "untargeted-claude")
	defaultProfile.Options.CodexSyncEnabled = false
	defaultProfile.Options.CodexSyncDeclined = true
	defaultPath := filepath.Join(home, ".cctrace", "profile.json")
	if err := profile.SaveTo(&defaultProfile, defaultPath); err != nil {
		t.Fatal(err)
	}
	defaultBefore, err := os.ReadFile(defaultPath)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{profilePath, filepath.Join(p.ClaudeConfigDir, "settings.json"), filepath.Join(codexDir, "config.toml")}
	snapshot := func() [][]byte {
		out := make([][]byte, len(paths))
		for i, path := range paths {
			var err error
			out[i], err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	unchanged := func(want [][]byte) {
		t.Helper()
		for i, got := range snapshot() {
			if !bytes.Equal(got, want[i]) {
				t.Fatalf("unexpected mutation: %s", paths[i])
			}
		}
	}
	out, err := run("config", "policy", "status", "--profile", "fixture")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	var status struct {
		InstallationID string `json:"installation_id"`
	}
	if err := json.Unmarshal(out, &status); err != nil || status.InstallationID == "" {
		t.Fatalf("status target: %s, %v", out, err)
	}
	original := snapshot()
	out, err = run("config", "policy", "apply", "--profile", "fixture")
	if err != nil {
		t.Fatalf("no policy apply: %v, %s", err, out)
	}
	unchanged(original)
	// Even with no policy to apply the run reports: that is how a device reaches
	// the operator's list before any policy names it.
	if r, ok := lastReport(); !ok || r.tls || r.body.InstallationID != status.InstallationID || r.body.Profile != "fixture" ||
		r.body.SyncEndpoint != bootstrap.URL || r.body.AppliedSequence != 0 || r.body.CheckResult != "ok" {
		t.Fatalf("report after a no-policy apply = %+v (received %v), want the untouched target over HTTP", r, ok)
	}
	sum := sha256.Sum256(ca)
	policy := serverpolicy.Policy{Schema: 1, Sequence: 1, ServerHost: "127.0.0.1", NotBefore: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Hour), Targets: []serverpolicy.Target{{InstallationID: status.InstallationID, Profile: "fixture"}}, From: []serverpolicy.Endpoints{{Sync: bootstrap.URL, OTEL: bootstrap.URL, Protocol: "http/protobuf"}}, To: serverpolicy.Endpoints{Sync: secure.URL, OTEL: secure.URL, Protocol: "http/protobuf"}, CAPEM: string(ca), CASHA256: hex.EncodeToString(sum[:])}
	signed, err := serverpolicy.Sign(policy, key)
	if err != nil {
		t.Fatal(err)
	}
	_, foreignKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := serverpolicy.Sign(policy, foreignKey)
	if err != nil {
		t.Fatal(err)
	}
	bundle.Store(foreign)
	if out, err = run("config", "policy", "apply", "--profile", "fixture"); err == nil {
		t.Fatalf("accepted foreign signature: %s", out)
	}
	unchanged(original)
	// A plain HTTP listener presented as HTTPS must fail the TLS probe.
	broken := policy
	broken.To.Sync = strings.Replace(bootstrap.URL, "http://", "https://", 1)
	badTLS, err := serverpolicy.Sign(broken, key)
	if err != nil {
		t.Fatal(err)
	}
	bundle.Store(badTLS)
	if out, err = run("config", "policy", "apply", "--profile", "fixture"); err == nil {
		t.Fatalf("accepted failing TLS: %s", out)
	}
	unchanged(original)
	bundle.Store(signed)
	sharedPath := filepath.Join(home, ".cctrace", "profiles", "shared", "profile.json")
	if err := profile.SaveTo(p, sharedPath); err != nil {
		t.Fatal(err)
	}
	if out, err = run("config", "policy", "apply", "--profile", "fixture"); err == nil {
		t.Fatalf("accepted shared settings: %s", out)
	}
	unchanged(original)
	if r, ok := lastReport(); !ok || r.body.CheckResult != "deferred" || r.body.CheckSequence != policy.Sequence || r.body.AppliedSequence != 0 {
		t.Fatalf("report after a deferred apply = %+v (received %v), want deferred at sequence %d", r, ok, policy.Sequence)
	}
	if err := os.RemoveAll(filepath.Dir(sharedPath)); err != nil {
		t.Fatal(err)
	}
	if out, err = run("config", "policy", "check", "--profile", "fixture"); err != nil {
		t.Fatalf("check: %v, %s", err, out)
	}
	unchanged(original)
	// A profile that shares only the default Codex folder does not hold the policy
	// back: every profile writes that file, so the policy moves this profile and
	// leaves a block that is not its own as it is.
	codexOnly := *p
	codexOnly.ClaudeConfigDir = filepath.Join(home, "codex-only-claude")
	codexOnlyPath := filepath.Join(home, ".cctrace", "profiles", "codexonly", "profile.json")
	if err := profile.SaveTo(&codexOnly, codexOnlyPath); err != nil {
		t.Fatal(err)
	}
	configPath := paths[2]
	for label, content := range map[string][]byte{
		"own block":    original[2],
		"edited block": []byte(strings.Replace(string(original[2]), "[otel]", "[otel]\nlog_user_prompt = true", 1)),
	} {
		if err := os.WriteFile(configPath, content, 0600); err != nil {
			t.Fatal(err)
		}
		out, err = run("config", "policy", "check", "--profile", "fixture")
		if err != nil {
			t.Fatalf("check with a Codex-only sibling, %s: %v, %s", label, err, out)
		}
		var checked struct {
			Changes []string `json:"changes"`
		}
		if err := json.Unmarshal(out, &checked); err != nil || len(checked.Changes) == 0 {
			t.Fatalf("check with a Codex-only sibling, %s: %v, %s", label, err, out)
		}
		for _, changed := range checked.Changes {
			if filepath.Base(changed) == "config.toml" {
				t.Fatalf("check with a Codex-only sibling, %s, plans the shared config inside the transaction: %s", label, out)
			}
		}
		if got, err := os.ReadFile(configPath); err != nil || !bytes.Equal(got, content) {
			t.Fatalf("check with a Codex-only sibling, %s, changed the shared config: %v", label, err)
		}
	}
	if err := os.WriteFile(configPath, original[2], 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Dir(codexOnlyPath)); err != nil {
		t.Fatal(err)
	}
	unchanged(original)
	watcherLog, err := os.Create(filepath.Join(fixture, "watcher.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer watcherLog.Close()
	watcher := exec.Command(bin, "sync", "--watch", "--interval", "25ms", "--profile", "fixture")
	watcher.Env = childEnv
	watcher.Stdout, watcher.Stderr = watcherLog, watcherLog
	// Hold the startup policy response until the original watcher's identity is
	// observed, then release the signed policy to its automatic coordinator.
	gateStartup.Store(true)
	defer func() {
		select {
		case <-startupPolicyGate:
		default:
			close(startupPolicyGate)
		}
	}()
	beforeRequests := policyRequests.Load()
	if err := watcher.Start(); err != nil {
		t.Fatal(err)
	}
	watchDone := make(chan error, 1)
	go func() { watchDone <- watcher.Wait() }()
	t.Cleanup(func() {
		if out, err := run("sync", "--stop", "--profile", "fixture"); err != nil {
			t.Errorf("cleanup watcher: %v, %s", err, out)
		}
		select {
		case <-watchDone:
		default:
			_ = watcher.Process.Kill()
			<-watchDone
		}
	})
	runtimeState := func() watcherState { return observeRuntime("poll") }
	waitFor := func(label string, ready func() bool) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for !ready() {
			select {
			case <-ctx.Done():
				observeRuntime("timeout " + label)
				select {
				case err := <-watchDone:
					t.Logf("watcher already exited: %v", err)
					watchDone <- err // Preserve the result for cleanup.
				default:
					t.Log("watcher process has not returned from Wait")
				}
				runtimeMu.Lock()
				trace := strings.Join(runtimeTrace, "\n")
				runtimeMu.Unlock()
				t.Logf("watcher runtime observations:\n%s", trace)
				// Fixture-only lifecycle metadata; never dump profile/auth contents.
				for _, path := range []string{
					filepath.Join(filepath.Dir(runtimePath), "sync-exit.json"),
					filepath.Join(filepath.Dir(runtimePath), "server-policy-handoff.json"),
					filepath.Join(home, ".cctrace", "daemon-runtime.json"),
					filepath.Join(home, ".cctrace", "daemon-status.json"),
				} {
					data, err := os.ReadFile(path)
					t.Logf("%s: %s (read=%v)", filepath.Base(path), data, err)
				}
				log, _ := os.ReadFile(watcherLog.Name())
				t.Fatalf("%s: %v\n%s", label, ctx.Err(), log)
			case <-ticker.C:
			}
		}
	}
	waitFor("watcher startup", func() bool { return runtimeState().InstanceID != "" && policyRequests.Load() > beforeRequests })
	firstRuntime := observeRuntime("replacement baseline")
	close(startupPolicyGate)
	waitFor("watcher replacement", func() bool {
		state := runtimeState()
		return state.InstanceID != "" && state.InstanceID != firstRuntime.InstanceID
	})
	if got := runtimeState().Interval; got != 25*time.Millisecond {
		t.Fatalf("watcher interval = %s", got)
	}
	// The run that applied the policy reports afterwards, so the report itself
	// travels over the new endpoint with the CA the policy just installed.
	// Any report, not the last one: the watcher also checks the untargeted default
	// profile, whose own report can land after this one.
	waitFor("policy report over HTTPS", func() bool {
		reportsMu.Lock()
		defer reportsMu.Unlock()
		for _, r := range reports {
			if r.tls && r.body.Profile == "fixture" && r.body.AppliedSequence == policy.Sequence && r.body.CheckResult == "ok" && r.body.SyncEndpoint == secure.URL {
				return true
			}
		}
		return false
	})
	projectDir := filepath.Join(p.ClaudeConfigDir, "projects", "fixture")
	if err := os.MkdirAll(projectDir, 0700); err != nil {
		t.Fatal(err)
	}
	record := map[string]any{"type": "user", "timestamp": time.Now().UTC().Format(time.RFC3339), "sessionId": "policy-session", "uuid": "policy-record", "cwd": home, "message": map[string]any{"role": "user", "content": "synthetic policy migration"}}
	raw, _ := json.Marshal(record)
	if err := os.WriteFile(filepath.Join(projectDir, "policy-session.jsonl"), append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	waitFor("HTTPS synthetic upload", func() bool {
		select {
		case body := <-uploads:
			return strings.Contains(body, "synthetic policy migration")
		default:
			return false
		}
	})
	if out, err = run("ls", "--json", "--profile", "fixture"); err != nil || reads.Load() == 0 {
		t.Fatalf("HTTPS read: %v, %s", err, out)
	}
	updated := snapshot()
	var actual profile.Profile
	if err := json.Unmarshal(updated[0], &actual); err != nil {
		t.Fatal(err)
	}
	if actual.Server.Endpoint != secure.URL || actual.Server.SyncEndpoint != secure.URL || actual.Server.CACertFile == "" {
		t.Fatalf("profile did not migrate: %+v", actual.Server)
	}
	installed, err := os.ReadFile(actual.Server.CACertFile)
	if err != nil || !bytes.Equal(installed, ca) {
		t.Fatalf("installed CA mismatch: %v", err)
	}
	var claudeSettings struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(updated[1], &claudeSettings); err != nil {
		t.Fatal(err)
	}
	if claudeSettings.Env["OTEL_EXPORTER_OTLP_ENDPOINT"] != secure.URL || claudeSettings.Env["NODE_EXTRA_CA_CERTS"] != actual.Server.CACertFile {
		t.Fatalf("Claude settings did not migrate: %s", updated[1])
	}
	if !bytes.Contains(updated[2], []byte(secure.URL)) || !bytes.Contains(updated[2], []byte(strconv.Quote(actual.Server.CACertFile))) {
		t.Fatalf("Codex settings did not migrate: %s", updated[2])
	}

	if out, err = run("config", "policy", "apply", "--profile", "fixture"); err != nil {
		t.Fatalf("repeat apply: %v, %s", err, out)
	}
	unchanged(updated)
	// Rotate to a separately signed TLS root, remove the old root, then recover
	// to the original HTTPS server with a higher sequence and original CA.
	for i, step := range []struct {
		endpoint string
		ca       []byte
	}{
		{rotatedServer.URL, append(append([]byte(nil), ca...), newCA...)},
		{rotatedServer.URL, newCA},
		{secure.URL, ca},
	} {
		policy.Sequence = uint64(i + 2)
		policy.From = []serverpolicy.Endpoints{policy.To}
		policy.To = serverpolicy.Endpoints{Sync: step.endpoint, OTEL: step.endpoint, Protocol: "http/protobuf"}
		policy.CAPEM = string(step.ca)
		digest := sha256.Sum256(step.ca)
		policy.CASHA256 = hex.EncodeToString(digest[:])
		nextBundle, err := serverpolicy.Sign(policy, policyKey)
		if err != nil {
			t.Fatal(err)
		}
		bundle.Store(nextBundle)
		if out, err = run("config", "policy", "apply", "--profile", "fixture"); err != nil {
			t.Fatalf("rotation/recovery sequence %d: %v, %s", policy.Sequence, err, out)
		}
		changed := snapshot()
		if err := json.Unmarshal(changed[0], &actual); err != nil {
			t.Fatal(err)
		}
		if actual.Server.SyncEndpoint != step.endpoint {
			t.Fatalf("sequence %d did not switch endpoint", policy.Sequence)
		}
		installed, err := os.ReadFile(actual.Server.CACertFile)
		if err != nil || !bytes.Equal(installed, step.ca) {
			t.Fatalf("sequence %d installed wrong trust bundle: %v", policy.Sequence, err)
		}
	}
	// A policy for every profile on the server, served by the server's handler:
	// the profile moves without being named, and a request without the
	// capability parameter, as older clients send it, gets nothing. The default
	// profile is on endpoints this policy does not move from and stays.
	all := policy
	all.Sequence++
	all.Targets, all.AllTargets = nil, true
	all.From = []serverpolicy.Endpoints{policy.To}
	all.To = serverpolicy.Endpoints{Sync: rotatedServer.URL, OTEL: rotatedServer.URL, Protocol: "http/protobuf"}
	all.CAPEM = string(newCA)
	allDigest := sha256.Sum256(newCA)
	all.CASHA256 = hex.EncodeToString(allDigest[:])
	allBundle, err := serverpolicy.Sign(all, policyKey)
	if err != nil {
		t.Fatal(err)
	}
	allPath := filepath.Join(fixture, "all-targets-policy.json")
	if err := os.WriteFile(allPath, allBundle, 0600); err != nil {
		t.Fatal(err)
	}
	policyHandler.Store(serverpolicy.Handler(allPath))
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca)
	older := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}}
	resp, err := older.Get(secure.URL + "/api/client-config-policy?installation_id=" + status.InstallationID + "&profile=fixture")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("request without the capability got HTTP %d", resp.StatusCode)
	}
	if out, err = run("config", "policy", "apply", "--profile", "fixture"); err != nil {
		t.Fatalf("policy for all targets: %v, %s", err, out)
	}
	if err := json.Unmarshal(snapshot()[0], &actual); err != nil {
		t.Fatal(err)
	}
	if moved, err := os.ReadFile(actual.Server.CACertFile); actual.Server.SyncEndpoint != rotatedServer.URL || err != nil || !bytes.Equal(moved, newCA) {
		t.Fatalf("policy for all targets did not move the profile: %+v, %v", actual.Server, err)
	}
	if r, ok := lastReport(); !ok || r.body.Profile != "fixture" || r.body.AppliedSequence != all.Sequence || r.body.CheckResult != "ok" {
		t.Fatalf("report after a policy for all targets = %+v (received %v)", r, ok)
	}
	defaultAfter, err := os.ReadFile(defaultPath)
	if err != nil || !bytes.Equal(defaultBefore, defaultAfter) {
		t.Fatalf("untargeted default profile mutated: %v", err)
	}
	t.Logf("isolated CLI transition and rejection cases passed for %s", runtime.GOOS)
}

// Independent self-signed CA/server certificates make trust-root replacement
// observable: httptest's default certificate is shared by all its TLS servers.
func serverPolicyTLSServer(t *testing.T, handler http.Handler) (*httptest.Server, []byte) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	return server, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
