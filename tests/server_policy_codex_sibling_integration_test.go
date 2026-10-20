package tests

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// A profile that shares the default Codex folder with one the policy does not
// move is still moved by `config policy apply`. The file stays what it holds: the
// profile's own block goes to the new server, and a block of the sibling's stays
// as it is, with a note in the report. No sync daemon runs here, so the apply is
// the manual one.
func TestServerPolicyBinaryApplyWithDefaultCodexFolderSibling(t *testing.T) {
	t.Parallel()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fixture := t.TempDir()
	bin := filepath.Join(fixture, "cctrace")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.CommandContext(t.Context(), "go", "build", "-buildvcs=false", "-ldflags", "-X main.updatePublicKeyBase64="+base64.StdEncoding.EncodeToString(pub), "-o", bin, "./cmd/cctrace")
	build.Dir = rootDir(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	home := filepath.Join(fixture, "home")
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0700); err != nil {
		t.Fatal(err)
	}
	childEnv := []string{"HOME=" + home, "USERPROFILE=" + home, "APPDATA=" + filepath.Join(home, "appdata"), "XDG_CONFIG_HOME=" + filepath.Join(home, "config"), "CODEX_HOME=" + codexDir, "CODEX_CONFIG_DIR=" + codexDir, "CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude"), "PATH=" + fixture, "SYSTEMROOT=" + os.Getenv("SYSTEMROOT")}
	run := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = childEnv
		return cmd.CombinedOutput()
	}

	var bundle atomic.Value
	bundle.Store([]byte(nil))
	type report struct {
		AppliedSequence uint64 `json:"applied_sequence"`
		CheckResult     string `json:"check_result"`
		CheckReason     string `json:"check_reason"`
	}
	var mu sync.Mutex
	var reports []report
	lastReport := func() report {
		mu.Lock()
		defer mu.Unlock()
		if len(reports) == 0 {
			t.Fatal("no policy report received")
		}
		return reports[len(reports)-1]
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/client-config-policy":
			raw := bundle.Load().([]byte)
			if len(raw) == 0 {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(raw)
		case "/api/health", "/api/version", "/v1/logs", "/v1/metrics":
			if strings.HasPrefix(r.URL.Path, "/v1/") && r.Header.Get("Authorization") != "Bearer synthetic-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"status":"ok","version":"dev"}`))
		case "/api/client-policy-report":
			var seen report
			_ = json.NewDecoder(r.Body).Decode(&seen)
			mu.Lock()
			reports = append(reports, seen)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	bootstrap := httptest.NewServer(handler)
	defer bootstrap.Close()
	secure, ca := serverPolicyTLSServer(t, handler)
	defer secure.Close()
	rotated, rotatedCA := serverPolicyTLSServer(t, handler)
	defer rotated.Close()

	p := &profile.Profile{Version: 1, User: profile.UserInfo{ID: "fixture", Name: "Fixture", Email: "fixture@example.test"}, Server: profile.ServerInfo{Endpoint: bootstrap.URL, SyncEndpoint: bootstrap.URL, Protocol: "http/protobuf", AuthToken: "synthetic-token"}, Options: profile.ProfileOptions{SyncEnabled: true, CodexSyncEnabled: true}, ClaudeConfigDir: filepath.Join(home, ".claude")}
	if err := profile.SaveTo(p, filepath.Join(home, ".cctrace", "profiles", "fixture", "profile.json")); err != nil {
		t.Fatal(err)
	}
	if err := envgen.ApplyToClaudeSettings(p); err != nil {
		t.Fatal(err)
	}
	// The sibling: its own Claude home and token, the default Codex folder, no
	// daemon, and no place in any policy below.
	sibling := *p
	sibling.ClaudeConfigDir = filepath.Join(home, "sibling-claude")
	sibling.Server.AuthToken = "synthetic-sibling-token"
	if err := profile.SaveTo(&sibling, filepath.Join(home, ".cctrace", "profiles", "home", "profile.json")); err != nil {
		t.Fatal(err)
	}
	if err := codexconfig.WriteOtelBlock(codexDir, p.Server.Endpoint, p.Server.AuthToken, ""); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(codexDir, "config.toml")

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
	policy := func(sequence uint64, from, to string, caPEM []byte) {
		t.Helper()
		sum := sha256.Sum256(caPEM)
		signed, err := serverpolicy.Sign(serverpolicy.Policy{Schema: 1, Sequence: sequence, ServerHost: "127.0.0.1", NotBefore: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Hour), Targets: []serverpolicy.Target{{InstallationID: status.InstallationID, Profile: "fixture"}}, From: []serverpolicy.Endpoints{{Sync: from, OTEL: from, Protocol: "http/protobuf"}}, To: serverpolicy.Endpoints{Sync: to, OTEL: to, Protocol: "http/protobuf"}, CAPEM: string(caPEM), CASHA256: hex.EncodeToString(sum[:])}, key)
		if err != nil {
			t.Fatal(err)
		}
		bundle.Store(signed)
	}

	// The file holds the profile's own block: the apply moves it with the profile.
	policy(1, bootstrap.URL, secure.URL, ca)
	if out, err = run("config", "policy", "apply", "--profile", "fixture"); err != nil {
		t.Fatalf("apply beside a sibling sharing the default Codex folder: %v\n%s", err, out)
	}
	moved, err := os.ReadFile(configPath)
	if err != nil || !bytes.Contains(moved, []byte(secure.URL)) || bytes.Contains(moved, []byte(bootstrap.URL)) {
		t.Fatalf("the profile's block did not move: %v\n%s", err, moved)
	}
	if r := lastReport(); r.AppliedSequence != 1 || r.CheckResult != "ok" || r.CheckReason != "" {
		t.Fatalf("report after the first apply = %+v", r)
	}

	// The file now holds the sibling's block, which is the sibling's to move.
	if err := codexconfig.WriteOtelBlock(codexDir, sibling.Server.Endpoint, sibling.Server.AuthToken, ""); err != nil {
		t.Fatal(err)
	}
	siblings, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	policy(2, secure.URL, rotated.URL, rotatedCA)
	if out, err = run("config", "policy", "apply", "--profile", "fixture"); err != nil {
		t.Fatalf("apply over the sibling's block: %v\n%s", err, out)
	}
	if got, err := os.ReadFile(configPath); err != nil || !bytes.Equal(got, siblings) {
		t.Fatalf("the sibling's block was overwritten: %v\n%s", err, got)
	}
	var moved2 profile.Profile
	data, err := os.ReadFile(filepath.Join(home, ".cctrace", "profiles", "fixture", "profile.json"))
	if err != nil || json.Unmarshal(data, &moved2) != nil || moved2.Server.SyncEndpoint != rotated.URL {
		t.Fatalf("the profile did not move on: %v, %+v", err, moved2.Server)
	}
	r := lastReport()
	if r.AppliedSequence != 2 || r.CheckResult != "ok" || !strings.HasPrefix(r.CheckReason, "default Codex config kept: ") || !strings.HasSuffix(filepath.ToSlash(r.CheckReason), ".codex/config.toml") {
		t.Fatalf("report after the second apply = %+v; want ok at sequence 2 with the note", r)
	}
}
