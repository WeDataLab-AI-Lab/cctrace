package main

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
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/flock"

	"cctrace/internal/codexconfig"
	"cctrace/internal/configtxn"
	"cctrace/internal/envgen"
	"cctrace/internal/profile"
	"cctrace/internal/serverpolicy"
	"cctrace/internal/store"
)

// HOME, the signing key, the runtime seams and the sync locks are process-wide,
// so none of these tests run in parallel.

const (
	sharedRunTokenA    = "fixture-token-a"
	sharedRunTokenHome = "fixture-token-home"
)

// sharedRunFixture runs whole policy applications for two profiles, the
// default and "home", that share the default Codex home. Both are on one
// server, the policy moves both, and "home" has a live sync daemon.
type sharedRunFixture struct {
	key      ed25519.PrivateKey
	bundle   atomic.Value // []byte; empty answers 204
	pol      serverpolicy.Policy
	codexDir string
	config   string // the shared config.toml, canonical
	a, home  *profile.Profile
	homeLock *flock.Flock

	mu      sync.Mutex
	reports [][]byte // report bodies, as received

	blocks map[[3]string]string
}

func newSharedRunFixture(t *testing.T) *sharedRunFixture {
	t.Helper()
	dir := useTempSyncHome(t)
	f := &sharedRunFixture{codexDir: filepath.Join(dir, ".codex")}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.key = key
	original := updatePublicKeyBase64
	updatePublicKeyBase64 = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { updatePublicKeyBase64 = original })
	now := time.Now()
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	report := func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/client-policy-report" {
			return false
		}
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reports = append(f.reports, body)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	policy := func(w http.ResponseWriter) {
		raw := f.bundle.Load().([]byte)
		if len(raw) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write(raw)
	}
	// The profile fetches from here once it has moved; anything else is the probe.
	destination := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case report(w, r):
		case r.URL.Path == "/api/client-config-policy":
			policy(w)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	destination.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	destination.StartTLS()
	t.Cleanup(destination.Close)
	f.bundle.Store([]byte(nil))
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !report(w, r) {
			policy(w)
		}
	}))
	t.Cleanup(source.Close)
	if err := os.MkdirAll(f.codexDir, 0700); err != nil {
		t.Fatal(err)
	}
	if f.config, err = canonicalPolicyPath(filepath.Join(f.codexDir, "config.toml")); err != nil {
		t.Fatal(err)
	}
	save := func(name, token string) *profile.Profile {
		p := profile.NewDefault()
		p.Server.SyncEndpoint, p.Server.Endpoint, p.Server.Protocol = source.URL, source.URL, "http/protobuf"
		p.Server.AuthToken = token
		p.ClaudeConfigDir = filepath.Join(t.TempDir(), "claude")
		if err := saveProfile(p, name); err != nil {
			t.Fatal(err)
		}
		return p
	}
	f.a, f.home = save("", sharedRunTokenA), save("home", sharedRunTokenHome)
	f.homeLock = startSharedPolicyDaemon(t, "home", liveSharedDaemon)
	// "home" hands its daemon over when it applies; the test holds that lock.
	oldStop, oldStart := policyStopRuntimeFn, policyStartRuntimeFn
	t.Cleanup(func() { policyStopRuntimeFn, policyStartRuntimeFn = oldStop, oldStart })
	policyStopRuntimeFn = func(context.Context, string) error { return f.homeLock.Unlock() }
	policyStartRuntimeFn = func(context.Context, string, *syncRuntime) error { return nil }
	if err := codexconfig.WriteOtelBlock(f.codexDir, source.URL, sharedRunTokenA, ""); err != nil {
		t.Fatal(err)
	}
	var targets []serverpolicy.Target
	for _, name := range []string{"", "home"} {
		target, err := serverPolicyTarget(name)
		if err != nil {
			t.Fatal(err)
		}
		targets = append(targets, target)
	}
	sum := sha256.Sum256(ca)
	f.pol = serverpolicy.Policy{Schema: 1, Sequence: 7, ServerHost: "127.0.0.1", NotBefore: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), Targets: targets, From: []serverpolicy.Endpoints{policyEndpoints(f.a)}, To: serverpolicy.Endpoints{Sync: destination.URL, OTEL: destination.URL, Protocol: "http/protobuf"}, CAPEM: string(ca), CASHA256: hex.EncodeToString(sum[:])}
	f.serve(t, f.pol)
	// The two blocks most tests compare against, without writing more files:
	// the config as written, and its move worked out by the planner.
	caPath, err := policyCAPath("", f.pol)
	if err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(f.config)
	if err != nil {
		t.Fatal(err)
	}
	old := codexconfig.PolicyBlock{Endpoint: source.URL, Token: sharedRunTokenA}
	next := codexconfig.PolicyBlock{Endpoint: f.pol.To.OTEL, Token: sharedRunTokenA, CA: caPath}
	plan, err := codexconfig.PlanSharedServerPolicyConfig(written, old, next, nil)
	if err != nil || plan.Holds != codexconfig.SharedBlockOld {
		t.Fatalf("fixture config: %+v %v", plan, err)
	}
	f.blocks = map[[3]string]string{{old.Endpoint, old.Token, old.CA}: string(written), {next.Endpoint, next.Token, next.CA}: string(plan.Content)}
	return f
}

// planned gives the profiles managed Claude settings, which planning a policy
// for them requires. Most tests only carry out recorded writes and skip it.
func (f *sharedRunFixture) planned(t *testing.T, profiles ...*profile.Profile) {
	t.Helper()
	for _, p := range profiles {
		if err := envgen.ApplyToClaudeSettings(p); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *sharedRunFixture) serve(t *testing.T, pol serverpolicy.Policy) {
	t.Helper()
	raw, err := serverpolicy.Sign(pol, f.key)
	if err != nil {
		t.Fatal(err)
	}
	f.bundle.Store(raw)
}

func (f *sharedRunFixture) serveNothing() { f.bundle.Store([]byte(nil)) }

// block is the shared config as it reads holding only this block.
func (f *sharedRunFixture) block(t *testing.T, endpoint, token, ca string) string {
	t.Helper()
	key := [3]string{endpoint, token, ca}
	if b, ok := f.blocks[key]; ok {
		return b
	}
	dir := t.TempDir()
	if err := codexconfig.WriteOtelBlock(dir, endpoint, token, ca); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	f.blocks[key] = string(b)
	return string(b)
}

func (f *sharedRunFixture) oldBlock(t *testing.T) string {
	return f.block(t, f.a.Server.Endpoint, sharedRunTokenA, "")
}

func (f *sharedRunFixture) newBlock(t *testing.T, name, token string) string {
	t.Helper()
	ca, err := policyCAPath(name, f.pol)
	if err != nil {
		t.Fatal(err)
	}
	return f.block(t, f.pol.To.OTEL, token, ca)
}

func (f *sharedRunFixture) read(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(f.config)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (f *sharedRunFixture) writeConfig(t *testing.T, content string) {
	t.Helper()
	if err := os.WriteFile(f.config, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

// useExtraFolder moves the shared config out of the default Codex folder into
// one the profiles list besides it, holding the default profile's old block.
func (f *sharedRunFixture) useExtraFolder(t *testing.T) {
	t.Helper()
	extra := filepath.Join(t.TempDir(), "codex-extra")
	if err := codexconfig.WriteOtelBlock(extra, f.a.Server.Endpoint, sharedRunTokenA, ""); err != nil {
		t.Fatal(err)
	}
	var err error
	if f.config, err = canonicalPolicyPath(filepath.Join(extra, "config.toml")); err != nil {
		t.Fatal(err)
	}
	f.codexDir = extra
}

func (f *sharedRunFixture) state(t *testing.T, name string) serverPolicyState {
	t.Helper()
	state, err := readServerPolicyState(name)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

// failSharedWrites makes every shared config write fail until the test ends or
// the returned function restores it.
func failSharedWrites(t *testing.T) func() {
	t.Helper()
	original := writeSharedPolicyConfigFn
	writeSharedPolicyConfigFn = func(string, []byte, os.FileMode) error { return errors.New("disk full") }
	restore := func() { writeSharedPolicyConfigFn = original }
	t.Cleanup(restore)
	return restore
}

// leaveWrite puts the default profile where an apply whose shared write failed
// leaves it: the policy committed, the move to its new block recorded. Only
// the OTEL endpoint and CA matter to the write, so the sync endpoint stays on
// the plain fixture server the later runs fetch from. The record is the one
// planning writes (TestSharedPolicyPendingWriteRecordedInTransaction); building
// it here keeps each test to one fixture's cost instead of a whole apply.
func (f *sharedRunFixture) leaveWrite(t *testing.T) {
	t.Helper()
	ca, err := policyCAPath("", f.pol)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(ca), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ca, []byte(f.pol.CAPEM), 0600); err != nil {
		t.Fatal(err)
	}
	editSibling(t, "", func(p *profile.Profile) { p.Server.Endpoint, p.Server.CACertFile = f.pol.To.OTEL, ca })
	old := codexconfig.PolicyBlock{Endpoint: f.a.Server.Endpoint, Token: sharedRunTokenA}
	next := codexconfig.PolicyBlock{Endpoint: f.pol.To.OTEL, Token: sharedRunTokenA, CA: ca}
	plan, err := codexconfig.PlanSharedServerPolicyConfig([]byte(f.read(t)), old, next, nil)
	if err != nil || plan.Holds != codexconfig.SharedBlockOld {
		t.Fatalf("shared config does not hold the old block: %+v %v", plan, err)
	}
	state, err := json.Marshal(serverPolicyState{Sequence: f.pol.Sequence, Status: "applied", SharedWrites: []sharedPolicyWrite{{Path: f.config, OldSHA256: plan.OldSHA256, NewSHA256: plan.NewSHA256, OldEndpoint: old.Endpoint}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile.DefaultDir(), "server-policy-state.json"), state, 0600); err != nil {
		t.Fatal(err)
	}
	f.serveNothing()
}

func TestSharedPolicyPendingWriteRecordedInTransaction(t *testing.T) {
	f := newSharedCodexFixture(t)
	changes, next, err := planServerPolicy(f.a, "", f.pol, "digest", serverPolicyState{})
	if err != nil {
		t.Fatal(err)
	}
	config, err := canonicalPolicyPath(filepath.Join(f.codexDir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	statePath, err := policyPath("", "server-policy-state.json")
	if err != nil {
		t.Fatal(err)
	}
	var committed []byte
	for _, c := range changes {
		if c.Path == config {
			t.Fatal("shared Codex config is written inside this profile's transaction")
		}
		if c.Path == statePath {
			committed = c.After
		}
	}
	var state serverPolicyState
	if err := json.Unmarshal(committed, &state); err != nil {
		t.Fatal(err)
	}
	want := sharedPolicyWrite{Path: config, OldEndpoint: sharedPolicyFrom.OTEL}
	if len(state.SharedWrites) != 1 || len(next.SharedWrites) != 1 {
		t.Fatalf("shared writes committed %+v, returned %+v", state.SharedWrites, next.SharedWrites)
	}
	got := state.SharedWrites[0]
	if got.Path != want.Path || got.OldEndpoint != want.OldEndpoint || got.OldCAFile != "" || len(got.OldSHA256) != 64 || len(got.NewSHA256) != 64 || got.OldSHA256 == got.NewSHA256 {
		t.Fatalf("shared write %+v", got)
	}
}

// The state file, `config policy status`, the report and the log all leave the
// device or are shown to people. None may carry the token the blocks hold.
func TestSharedPolicyPendingWriteLeaksNoToken(t *testing.T) {
	f := newSharedRunFixture(t)
	f.planned(t, f.a)
	failSharedWrites(t)
	name := ""
	run := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		cmd := serverPolicyCmd(&name)
		cmd.SetArgs(args)
		cmd.SetOut(&out)
		cmd.SetErr(io.Discard)
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		_ = cmd.Execute()
		return out.String()
	}
	run("apply", "--log-to-file")
	status := run("status")
	state, err := os.ReadFile(filepath.Join(profile.DefaultDir(), "server-policy-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(syncLogPath(""))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	f.mu.Lock()
	reports := bytes.Join(f.reports, []byte("\n"))
	f.mu.Unlock()
	if !strings.Contains(status, `"shared_writes"`) || !bytes.Contains(reports, []byte("shared settings write pending")) {
		t.Fatalf("fixture left no shared write to report:\nstatus %s\nreports %s", status, reports)
	}
	for label, text := range map[string]string{"state file": string(state), "status output": status, "report": string(reports), "log": string(log)} {
		for _, token := range []string{sharedRunTokenA, sharedRunTokenHome} {
			if strings.Contains(text, token) {
				t.Errorf("%s carries a token:\n%s", label, text)
			}
		}
	}
}

// Recovery may roll the state back to before the write was recorded. A write
// carried out first would follow a record that is about to disappear.
func TestSharedPolicyPendingWriteWaitsForRecovery(t *testing.T) {
	f := newSharedRunFixture(t)
	f.leaveWrite(t)
	statePath, err := policyPath("", "server-policy-state.json")
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	before := f.state(t, "")
	before.SharedWrites = nil
	rolledBack, err := json.MarshalIndent(before, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, rolledBack, 0600); err != nil {
		t.Fatal(err)
	}
	journal, err := policyPath("", "server-policy-txn")
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected interruption")
			}
		}()
		_ = configtxn.Apply(journal, []configtxn.Change{{Path: statePath, Before: rolledBack, BeforeExists: true, After: recorded, Mode: 0600}}, func() error { panic("interrupted") })
	}()
	// A run checks for recovery and lets go of the global lock before taking it
	// again for the writes; a command interrupted in between leaves exactly this.
	// Rewriting the state now would leave a journal Recover can never finish.
	if err := drainRecordedSharedPolicyWrites(context.Background(), ""); err == nil || err.Error() != "shared settings write pending" {
		t.Fatalf("drain with a journal pending: %v", err)
	}
	if got, err := os.ReadFile(statePath); err != nil || !bytes.Equal(got, recorded) || f.read(t) != f.oldBlock(t) {
		t.Fatalf("carried out a shared write while a transaction awaited recovery: %v", err)
	}
	f.serveNothing()
	if _, err := runServerPolicy(context.Background(), "", false); err != nil {
		t.Fatal(err)
	}
	if got := f.read(t); got != f.oldBlock(t) {
		t.Fatalf("wrote from a record recovery rolled back:\n%s", got)
	}
	if state := f.state(t, ""); len(state.SharedWrites) != 0 {
		t.Fatalf("recovery kept the rolled back record: %+v", state.SharedWrites)
	}
}

func TestSharedPolicyPendingWriteDrainsWithoutPolicy(t *testing.T) {
	f := newSharedRunFixture(t)
	f.leaveWrite(t)
	f.serveNothing()
	if _, err := runServerPolicy(context.Background(), "", false); err != nil {
		t.Fatal(err)
	}
	if got := f.read(t); got != f.newBlock(t, "", sharedRunTokenA) {
		t.Fatalf("shared config not moved:\n%s", got)
	}
	if state := f.state(t, ""); len(state.SharedWrites) != 0 {
		t.Fatalf("record kept: %+v", state.SharedWrites)
	}
}

// A higher sequence that does not name where this profile is now ends the run
// at the match, before any write a policy brings. The recorded write is done by
// then, and the run reports the match failure.
func TestSharedPolicyPendingWriteDrainsAfterHigherSequence(t *testing.T) {
	f := newSharedRunFixture(t)
	f.leaveWrite(t)
	higher := f.pol
	higher.Sequence++
	f.serve(t, higher)
	_, err := runServerPolicy(context.Background(), "", false)
	if err == nil || !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("run against a policy for other endpoints: %v", err)
	}
	if got := f.read(t); got != f.newBlock(t, "", sharedRunTokenA) {
		t.Fatalf("shared config not moved:\n%s", got)
	}
	if state := f.state(t, ""); len(state.SharedWrites) != 0 {
		t.Fatalf("record kept: %+v", state.SharedWrites)
	}
}

// A sibling's block is the sibling's to move: it applies the policy itself.
func TestSharedPolicyPendingWriteYieldsToSibling(t *testing.T) {
	f := newSharedRunFixture(t)
	f.leaveWrite(t)
	sibling := f.block(t, f.home.Server.Endpoint, sharedRunTokenHome, "")
	f.writeConfig(t, sibling)
	f.serveNothing()
	if _, err := runServerPolicy(context.Background(), "", false); err != nil {
		t.Fatal(err)
	}
	if got := f.read(t); got != sibling {
		t.Fatalf("overwrote the sibling's block:\n%s", got)
	}
	if state := f.state(t, ""); len(state.SharedWrites) != 0 {
		t.Fatalf("record kept: %+v", state.SharedWrites)
	}
}

// editedBlock is the old block with an option someone added.
func (f *sharedRunFixture) editedBlock(t *testing.T) string {
	return strings.Replace(f.oldBlock(t), "[otel]", "[otel]\nlog_user_prompt = true", 1)
}

// An edited block outside the default Codex folder waits on its owner. Every
// run says so, as a deferral, and records it once.
func TestSharedPolicyPendingWriteEditedBlockDefers(t *testing.T) {
	f := newSharedRunFixture(t)
	f.useExtraFolder(t)
	f.leaveWrite(t)
	edited := f.editedBlock(t)
	f.writeConfig(t, edited)
	f.serveNothing()
	history := len(f.state(t, "").History)
	statePath, err := policyPath("", "server-policy-state.json")
	if err != nil {
		t.Fatal(err)
	}
	var seen os.FileInfo
	for i := 0; i < 2; i++ {
		_, err := runServerPolicy(context.Background(), "", false)
		if err == nil || err.Error() != "shared Codex settings edited; policy deferred" || policyCheckResult(err) != store.ClientPolicyCheckDeferred {
			t.Fatalf("run %d: %v", i, err)
		}
		// Waiting changes nothing, so the second run leaves the state file alone,
		// as any run with no policy to apply does. An atomic write would replace it.
		info, err := os.Stat(statePath)
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 && !os.SameFile(seen, info) {
			t.Fatal("a run with nothing to change rewrote the state")
		}
		seen = info
	}
	if got := f.read(t); got != edited {
		t.Fatalf("overwrote an edited block:\n%s", got)
	}
	state := f.state(t, "")
	if len(state.SharedWrites) != 1 || len(state.History) != history+1 {
		t.Fatalf("record or history: %+v", state)
	}
}

// In the default Codex folder the profile has moved and its Codex block is not
// the policy's to force: the record is dropped, the file stays as it is, and
// the run is a success that says what it left.
func TestSharedPolicyPendingWriteEditedBlockInDefaultFolderIsKept(t *testing.T) {
	f := newSharedRunFixture(t)
	f.leaveWrite(t)
	edited := f.editedBlock(t)
	f.writeConfig(t, edited)
	f.serveNothing()
	history := len(f.state(t, "").History)
	note := "default Codex config kept: " + f.config
	statePath, err := policyPath("", "server-policy-state.json")
	if err != nil {
		t.Fatal(err)
	}
	var seen os.FileInfo
	for i := 0; i < 2; i++ {
		if _, err := runServerPolicy(context.Background(), "", false); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		info, err := os.Stat(statePath)
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 && !os.SameFile(seen, info) {
			t.Fatal("a run with nothing to change rewrote the state")
		}
		seen = info
	}
	if got := f.read(t); got != edited {
		t.Fatalf("overwrote an edited block:\n%s", got)
	}
	state := f.state(t, "")
	last := state.History[len(state.History)-1]
	if len(state.SharedWrites) != 0 || len(state.History) != history+1 || last.Status != "cleared" || last.Reason != note || state.Status != "applied" || state.Reason != note {
		t.Fatalf("record or history: %+v", state)
	}
	report, _, err := buildPolicyReport("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.CheckResult != store.ClientPolicyCheckOK || report.CheckReason != note {
		t.Fatalf("report %q %q; want ok carrying the note", report.CheckResult, report.CheckReason)
	}
}

// A multi-line root otel key is nobody's block either: kept in the default Codex
// folder, waited on elsewhere.
func TestSharedPolicyPendingWriteMultilineRootOtelKey(t *testing.T) {
	const multiline = "otel.exporter = \"\"\"\nx\n\"\"\"\n"
	t.Run("default_folder", func(t *testing.T) {
		f := newSharedRunFixture(t)
		f.leaveWrite(t)
		f.writeConfig(t, multiline)
		f.serveNothing()
		if _, err := runServerPolicy(context.Background(), "", false); err != nil {
			t.Fatal(err)
		}
		state := f.state(t, "")
		if got := f.read(t); got != multiline || len(state.SharedWrites) != 0 || state.Reason != "default Codex config kept: "+f.config {
			t.Fatalf("config %q, state %+v; want the file untouched and the record dropped with the note", got, state)
		}
	})
	t.Run("extra_folder", func(t *testing.T) {
		f := newSharedRunFixture(t)
		f.useExtraFolder(t)
		f.leaveWrite(t)
		f.writeConfig(t, multiline)
		f.serveNothing()
		_, err := runServerPolicy(context.Background(), "", false)
		if err == nil || err.Error() != "shared Codex settings edited; policy deferred" || len(f.state(t, "").SharedWrites) != 1 {
			t.Fatalf("run: %v; want the deferral with the record kept", err)
		}
	})
}

// v0.7.64 recorded the same write, deferred on it every run, and left the state
// failed. The run that finds it now drops it and ends the failure.
func TestSharedPolicyPendingWrite_DeferredBefore0765_IsKept(t *testing.T) {
	f := newSharedRunFixture(t)
	f.leaveWrite(t)
	f.writeConfig(t, f.editedBlock(t))
	f.serveNothing()
	state := f.state(t, "")
	appendPolicyRecord(&state, state.Sequence, "failed", errSharedCodexEdited.Error())
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	statePath, err := policyPath("", "server-policy-state.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := runServerPolicy(context.Background(), "", false); err != nil {
		t.Fatal(err)
	}
	note := "default Codex config kept: " + f.config
	state = f.state(t, "")
	last := state.History[len(state.History)-1]
	if len(state.SharedWrites) != 0 || state.Status != "applied" || state.Reason != note || last.Status != "applied" {
		t.Fatalf("state after the run: %+v", state)
	}
}

// The recorded path is the canonical file. A link put on the way to it since
// is not followed: the write waits rather than land somewhere else. (A link at
// the file itself the atomic write refuses on its own.)
func TestSharedPolicyPendingWriteNotThroughLink(t *testing.T) {
	f := newSharedRunFixture(t)
	f.leaveWrite(t)
	elsewhere := t.TempDir()
	if err := os.WriteFile(filepath.Join(elsewhere, "config.toml"), []byte(f.oldBlock(t)), 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(f.config)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := runServerPolicy(context.Background(), "", false); err == nil || err.Error() != "shared settings write pending" {
		t.Fatalf("write through a link: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(elsewhere, "config.toml")); err != nil || string(got) != f.oldBlock(t) {
		t.Fatalf("wrote through the link: %s %v", got, err)
	}
	if state := f.state(t, ""); len(state.SharedWrites) != 1 {
		t.Fatalf("record lost: %+v", state.SharedWrites)
	}
}

// A shared config that is gone has nothing left to move; whoever writes it next
// writes its own block.
func TestSharedPolicyPendingWriteClearedWhenFileGone(t *testing.T) {
	f := newSharedRunFixture(t)
	f.leaveWrite(t)
	if err := os.Remove(f.config); err != nil {
		t.Fatal(err)
	}
	if _, err := runServerPolicy(context.Background(), "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.config); !os.IsNotExist(err) {
		t.Fatalf("recreated the shared config: %v", err)
	}
	if state := f.state(t, ""); len(state.SharedWrites) != 0 {
		t.Fatalf("record kept: %+v", state.SharedWrites)
	}
}

// A reissued token changes both blocks. The record no longer describes this
// file's move, so it is dropped unwritten, with a note in the history.
func TestSharedPolicyPendingWriteHashMismatchClears(t *testing.T) {
	f := newSharedRunFixture(t)
	f.leaveWrite(t)
	editSibling(t, "", func(p *profile.Profile) { p.Server.AuthToken = "fixture-token-reissued" })
	f.serveNothing()
	if _, err := runServerPolicy(context.Background(), "", false); err != nil {
		t.Fatal(err)
	}
	if got := f.read(t); got != f.oldBlock(t) {
		t.Fatalf("wrote from a stale record:\n%s", got)
	}
	state := f.state(t, "")
	last := state.History[len(state.History)-1]
	if len(state.SharedWrites) != 0 || !strings.Contains(last.Reason, "stale") || state.Status != "applied" {
		t.Fatalf("stale record: %+v", state)
	}
}

// Every shared write happens while both the global policy lock and the file's
// own writers' lock are held, at commit and on a later run. The order they are
// taken in is not observed here.
func TestSharedPolicyWriteHoldsGlobalAndFileLocks(t *testing.T) {
	f := newSharedRunFixture(t)
	f.planned(t, f.a)
	global, err := policyPath("", "server-policy.lock")
	if err != nil {
		t.Fatal(err)
	}
	file, err := canonicalPolicyPath(f.config + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	original := writeSharedPolicyConfigFn
	t.Cleanup(func() { writeSharedPolicyConfigFn = original })
	writes, fail := 0, true
	writeSharedPolicyConfigFn = func(path string, data []byte, mode os.FileMode) error {
		for _, lock := range []string{global, file} {
			// flock(2) locks belong to the open file, so this probe conflicts
			// with the run's lock even within one process.
			probe := flock.New(lock)
			free, err := probe.TryLock()
			if free {
				_ = probe.Unlock()
			}
			if err != nil || free {
				t.Errorf("%s not held during the shared write", lock)
			}
		}
		writes++
		if fail {
			return errors.New("disk full")
		}
		return original(path, data, mode)
	}
	// The policy commits though its shared write fails; the write waits.
	if _, err := runServerPolicy(context.Background(), "", false); err == nil || err.Error() != "shared settings write pending" {
		t.Fatalf("apply with a failing shared write: %v", err)
	}
	if state := f.state(t, ""); state.Sequence != f.pol.Sequence || len(state.SharedWrites) != 1 || f.read(t) != f.oldBlock(t) {
		t.Fatalf("commit with a failing shared write: %+v", state)
	}
	fail = false
	f.serveNothing()
	if _, err := runServerPolicy(context.Background(), "", false); err != nil {
		t.Fatal(err)
	}
	if writes != 2 || f.read(t) != f.newBlock(t, "", sharedRunTokenA) {
		t.Fatalf("%d writes, config:\n%s", writes, f.read(t))
	}
	// The failure the waiting write left is over with it: the policy is applied.
	state := f.state(t, "")
	last := state.History[len(state.History)-1]
	if state.Status != "applied" || state.Reason != "" || last.Status != "applied" || last.Sequence != f.pol.Sequence {
		t.Fatalf("finished write left %+v", state)
	}
}

// A shared write that cannot be done now is retried later; it never stops the
// fetch or the apply, and the run reports it only when nothing else failed.
func TestSharedPolicyPendingFailureDoesNotBlockApply(t *testing.T) {
	// TestSharedPolicyWriteUnderHeldLocks has the commit itself go through.
	t.Run("retried later", func(t *testing.T) {
		f := newSharedRunFixture(t)
		f.leaveWrite(t)
		failSharedWrites(t)
		_, err := runServerPolicy(context.Background(), "", false)
		if err == nil || err.Error() != "shared settings write pending" || policyCheckResult(err) != store.ClientPolicyCheckFailed {
			t.Fatalf("retry that fails again: %v", err)
		}
		if state := f.state(t, ""); len(state.SharedWrites) != 1 {
			t.Fatalf("record lost: %+v", state.SharedWrites)
		}
		// The run's own failure is what it reports.
		higher := f.pol
		higher.Sequence++
		f.serve(t, higher)
		if _, err := runServerPolicy(context.Background(), "", false); err == nil || !strings.Contains(err.Error(), "not authorized") {
			t.Fatalf("a failing run reported %v", err)
		}
	})
	t.Run("before fetch", func(t *testing.T) {
		pol, p := policyStateFixture(t, false)
		// A write left by an earlier move, in a config this policy does not plan.
		old := codexconfig.PolicyBlock{Endpoint: "http://localhost:4317", Token: p.Server.AuthToken}
		next := codexconfig.PolicyBlock{Endpoint: p.Server.Endpoint, Token: p.Server.AuthToken, CA: p.Server.CACertFile}
		dir := t.TempDir()
		if err := codexconfig.WriteOtelBlock(dir, old.Endpoint, old.Token, old.CA); err != nil {
			t.Fatal(err)
		}
		config, err := canonicalPolicyPath(filepath.Join(dir, "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(config)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := codexconfig.PlanSharedServerPolicyConfig(content, old, next, nil)
		if err != nil || plan.Holds != codexconfig.SharedBlockOld {
			t.Fatalf("fixture block: %+v %v", plan, err)
		}
		state, err := json.Marshal(serverPolicyState{Status: "applied", SharedWrites: []sharedPolicyWrite{{Path: config, OldSHA256: plan.OldSHA256, NewSHA256: plan.NewSHA256, OldEndpoint: old.Endpoint}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(profile.DefaultDir(), "server-policy-state.json"), state, 0600); err != nil {
			t.Fatal(err)
		}
		failed := 0
		original := writeSharedPolicyConfigFn
		t.Cleanup(func() { writeSharedPolicyConfigFn = original })
		writeSharedPolicyConfigFn = func(string, []byte, os.FileMode) error { failed++; return errors.New("disk full") }
		if _, err := runServerPolicy(context.Background(), "", false); err != nil {
			t.Fatalf("apply after a failed shared write: %v", err)
		}
		if failed != 1 || readStateSequence(t) != pol.Sequence {
			t.Fatalf("%d failed writes; sequence %d, want %d", failed, readStateSequence(t), pol.Sequence)
		}
	})
}

func readStateSequence(t *testing.T) uint64 {
	t.Helper()
	state, err := readServerPolicyState("")
	if err != nil {
		t.Fatal(err)
	}
	return state.Sequence
}

// v0.7.62 and v0.7.63 wrote the state without shared writes. N reads it as it
// is, and its own additions are optional fields an older reader ignores.
func TestServerPolicyStateReadsPreviousReleaseFile(t *testing.T) {
	useTempSyncHome(t)
	raw := `{
  "bootstrap_endpoint": "http://cctrace.company.example:4318",
  "sequence": 2,
  "digest": "d",
  "managed_ca": "/managed.pem",
  "managed_ca_sha256": "s",
  "status": "applied",
  "history": [
    {"sequence": 2, "status": "applied", "at": "2026-10-13T10:00:00Z"}
  ]
}`
	if err := os.MkdirAll(profile.DefaultDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile.DefaultDir(), "server-policy-state.json"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	state, err := readServerPolicyState("")
	if err != nil {
		t.Fatal(err)
	}
	if state.Sequence != 2 || state.ManagedCA != "/managed.pem" || state.Status != "applied" || len(state.History) != 1 || state.SharedWrites != nil {
		t.Fatalf("read %+v", state)
	}
	out, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "shared_writes") {
		t.Fatalf("a state without shared writes gained the field: %s", out)
	}
}

func TestSharedPolicyCheckOnlyWritesNothing(t *testing.T) {
	f := newSharedRunFixture(t)
	f.planned(t, f.a)
	result, err := runServerPolicy(context.Background(), "", true)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(listed), f.config) {
		t.Fatalf("check lists the shared config among the transaction's changes: %s", listed)
	}
	if _, err := os.Stat(filepath.Join(profile.DefaultDir(), "server-policy-state.json")); !os.IsNotExist(err) {
		t.Fatalf("check wrote the state: %v", err)
	}
	f.leaveWrite(t)
	statePath := filepath.Join(profile.DefaultDir(), "server-policy-state.json")
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	f.serveNothing()
	if _, err := runServerPolicy(context.Background(), "", true); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if f.read(t) != f.oldBlock(t) || !bytes.Equal(before, after) {
		t.Fatal("check carried out a recorded shared write")
	}
}

// The cost the rule accepts: the sibling's own session puts its old block back
// after this profile moved the shared config. Once the sibling applies the
// policy too, the config ends at the new server again.
func TestSharedPolicyRevertedBlockConvergesWhenSiblingApplies(t *testing.T) {
	f := newSharedRunFixture(t)
	f.planned(t, f.a, f.home)
	if _, err := runServerPolicy(context.Background(), "", false); err != nil {
		t.Fatal(err)
	}
	if got := f.read(t); got != f.newBlock(t, "", sharedRunTokenA) {
		t.Fatalf("first profile did not move the shared config:\n%s", got)
	}
	// What the sibling's `sync --daemon --once` writes before the sibling moves.
	if err := codexconfig.WriteOtelBlock(f.codexDir, f.home.Server.Endpoint, sharedRunTokenHome, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := runServerPolicy(context.Background(), "home", false); err != nil {
		t.Fatal(err)
	}
	if got := f.read(t); got != f.newBlock(t, "home", sharedRunTokenHome) {
		t.Fatalf("sibling's apply did not move the shared config back to the new server:\n%s", got)
	}
	for _, name := range []string{"", "home"} {
		if state := f.state(t, name); state.Sequence != f.pol.Sequence || len(state.SharedWrites) != 0 {
			t.Fatalf("profile %q: %+v", name, state)
		}
	}
}
