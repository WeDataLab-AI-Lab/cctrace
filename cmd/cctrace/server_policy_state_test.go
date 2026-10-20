package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cctrace/internal/configtxn"
	"cctrace/internal/envgen"
	"cctrace/internal/profile"
	"cctrace/internal/serverpolicy"
)

// policyDestination, when a test sets it, answers the requests the fixture's
// destination server gets -- the connectivity test's, not the policy's -- in place
// of a plain 200.
var policyDestination atomic.Pointer[http.HandlerFunc]

// onPolicyDestination makes handler answer the fixture destination's requests for
// the rest of the test.
func onPolicyDestination(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	policyDestination.Store(&handler)
	t.Cleanup(func() { policyDestination.Store(nil) })
}

// HOME and signing-key/runtime injection are process-wide; keep these serial.
func policyStateFixture(t *testing.T, separateCA bool) (serverpolicy.Policy, *profile.Profile) {
	t.Helper()
	useTempSyncHome(t)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
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
	// The destination serves the policy too, as the real server does, so a profile
	// the policy already moved can fetch it again.
	var bundle []byte
	destination := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/client-config-policy" {
			_, _ = w.Write(bundle)
			return
		}
		if handler := policyDestination.Load(); handler != nil {
			(*handler)(w, r)
			return
		}
		w.WriteHeader(200)
	}))
	destination.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	destination.StartTLS()
	t.Cleanup(destination.Close)
	target, err := serverPolicyTarget("")
	if err != nil {
		t.Fatal(err)
	}
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(bundle) }))
	t.Cleanup(source.Close)
	p := profile.NewDefault()
	p.Server.SyncEndpoint = source.URL
	p.Server.Endpoint = source.URL
	p.Server.Protocol = "http/protobuf"
	p.ClaudeConfigDir = filepath.Join(t.TempDir(), "claude")
	p.Options.CodexSyncDeclined = true
	if separateCA {
		p.Server.CACertFile = filepath.Join(t.TempDir(), "user-ca.pem")
		if err := os.WriteFile(p.Server.CACertFile, ca, 0600); err != nil {
			t.Fatal(err)
		}
		ca, err = os.ReadFile(writeTestCAFile(t, t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := profile.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := envgen.ApplyToClaudeSettings(p); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(ca)
	policy := serverpolicy.Policy{Schema: 1, Sequence: 7, ServerHost: "127.0.0.1", NotBefore: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), Targets: []serverpolicy.Target{target}, From: []serverpolicy.Endpoints{policyEndpoints(p)}, To: serverpolicy.Endpoints{Sync: destination.URL, OTEL: destination.URL, Protocol: "http/protobuf"}, CAPEM: string(ca), CASHA256: hex.EncodeToString(sum[:])}
	bundle, err = serverpolicy.Sign(policy, key)
	if err != nil {
		t.Fatal(err)
	}
	return policy, p
}
func TestServerPolicyFailureRecordPreservesCommittedState(t *testing.T) {
	policy, _ := policyStateFixture(t, false)
	original := applyServerPolicyChangesFn
	t.Cleanup(func() { applyServerPolicyChangesFn = original })
	applyServerPolicyChangesFn = func(name string, changes []configtxn.Change) error {
		dir, err := policyPath(name, "server-policy-txn")
		if err != nil {
			return err
		}
		if err := configtxn.Apply(dir, changes, nil); err != nil {
			return err
		}
		return errors.New("post-commit directory sync failed")
	}
	if _, err := runServerPolicy(context.Background(), "", false); err == nil {
		t.Fatal("expected cleanup error")
	}
	state, err := readServerPolicyState("")
	if err != nil {
		t.Fatal(err)
	}
	if state.Sequence != policy.Sequence || state.Digest == "" || state.ManagedCA == "" || state.ManagedCASHA256 == "" {
		t.Fatalf("failure erased committed policy: %+v", state)
	}
	if len(state.History) != 2 || state.History[0].Status != "applied" || state.History[1].Status != "failed" {
		t.Fatalf("lost commit history: %+v", state.History)
	}
}
func TestServerPolicyProbeUsesPlannedUserTrust(t *testing.T) {
	_, p := policyStateFixture(t, true)
	if _, err := runServerPolicy(context.Background(), "", true); err != nil {
		t.Fatalf("probe must trust staged user+policy CA: %v", err)
	}
	current, err := profile.Load()
	if err != nil {
		t.Fatal(err)
	}
	if current.Server != p.Server {
		t.Fatal("check changed configuration")
	}
}

func TestServerPolicyDiscoveryDoesNotBlockOtherStartup(t *testing.T) {
	useTempSyncHome(t)
	original := updatePublicKeyBase64
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	updatePublicKeyBase64 = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { updatePublicKeyBase64 = original })
	entered, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	p := profile.NewDefault()
	p.Server.SyncEndpoint = srv.URL
	if err := profile.Save(p); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := runServerPolicy(context.Background(), "", false); result <- err }()
	<-entered
	barrierResult := make(chan error, 1)
	go func() {
		unlock, err := policySyncStartupBarrier(context.Background(), "other", "", false)
		if err == nil {
			unlock()
		}
		barrierResult <- err
	}()
	blocked := false
	select {
	case err := <-barrierResult:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(200 * time.Millisecond):
		blocked = true
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if blocked {
		<-barrierResult
		t.Fatal("unrelated startup blocked by policy discovery")
	}

}

func TestServerPolicyOfflineTLSFailureUsesSignedBootstrap(t *testing.T) {
	policy, p := policyStateFixture(t, false)
	// The first signed policy's HTTP endpoint remains reachable while the current
	// HTTPS listener presents a CA absent from this offline client's trust store.
	bootstrap := p.Server.SyncEndpoint
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("untrusted TLS request reached handler") }))
	defer destination.Close()
	p.Server.SyncEndpoint = destination.URL
	keys, err := parseUpdatePublicKeys(updatePublicKeyBase64)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := fetchServerPolicyWithBootstrap(context.Background(), p, policy.Targets[0], keys, time.Now(), serverPolicyState{BootstrapEndpoint: bootstrap})
	if err != nil || got == nil || got.Sequence != policy.Sequence {
		t.Fatalf("offline recovery failed: %v %v", got, err)
	}
}
func TestServerPolicyBootstrapIsPersistedFromSignedFrom(t *testing.T) {
	policy, p := policyStateFixture(t, false)
	changes, state, err := planServerPolicy(p, "", policy, "digest", serverPolicyState{})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) == 0 || state.BootstrapEndpoint != policy.From[0].Sync {
		t.Fatalf("missing signed bootstrap: %+v", state)
	}
	prior := state.BootstrapEndpoint
	state.BootstrapEndpoint = prior
	policy.From[0].Sync = policy.To.Sync
	_, next, err := planServerPolicy(p, "", policy, "digest", serverPolicyState{BootstrapEndpoint: prior})
	if err != nil {
		t.Fatal(err)
	}
	if next.BootstrapEndpoint != prior {
		t.Fatal("discarded original HTTP bootstrap")
	}
}

func TestServerPolicyOfflineRootRotationAppliesThroughBootstrap(t *testing.T) {
	useTempSyncHome(t)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	original := updatePublicKeyBase64
	updatePublicKeyBase64 = base64.StdEncoding.EncodeToString(pub)
	defer func() { updatePublicKeyBase64 = original }()
	now := time.Now()
	makeCA := func(serial int64) (*tls.Certificate, []byte) {
		caPub, caKey, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			t.Fatal(e)
		}
		cert := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
		der, e := x509.CreateCertificate(rand.Reader, cert, cert, caPub, caKey)
		if e != nil {
			t.Fatal(e)
		}
		return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: caKey}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	}
	certA, caA := makeCA(1)
	certB, caB := makeCA(2)
	var active atomic.Pointer[tls.Certificate]
	active.Store(certA)
	var bundle atomic.Value
	bundle.Store([]byte{})
	var bootstrapGets atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bootstrapGets.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("bootstrap leaked credentials")
		}
		w.Write(bundle.Load().([]byte))
	}))
	defer source.Close()
	destination := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/client-config-policy" {
			w.Write(bundle.Load().([]byte))
			return
		}
		w.WriteHeader(200)
	}))
	destination.TLS = &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
		return &tls.Config{Certificates: []tls.Certificate{*active.Load()}}, nil
	}}
	destination.StartTLS()
	defer destination.Close()
	p := profile.NewDefault()
	p.Server.Endpoint = source.URL
	p.Server.SyncEndpoint = source.URL
	p.Server.Protocol = "http/protobuf"
	p.Server.AuthToken = "private-test-token"
	p.ClaudeConfigDir = filepath.Join(t.TempDir(), "claude")
	p.Options.CodexSyncDeclined = true
	if err := profile.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := envgen.ApplyToClaudeSettings(p); err != nil {
		t.Fatal(err)
	}
	target, err := serverPolicyTarget("")
	if err != nil {
		t.Fatal(err)
	}
	pol := serverpolicy.Policy{Schema: 1, Sequence: 1, ServerHost: "127.0.0.1", NotBefore: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), Targets: []serverpolicy.Target{target}, From: []serverpolicy.Endpoints{policyEndpoints(p)}, To: serverpolicy.Endpoints{Sync: destination.URL, OTEL: destination.URL, Protocol: "http/protobuf"}}
	publish := func(ca []byte) {
		pol.CAPEM = string(ca)
		sum := sha256.Sum256(ca)
		pol.CASHA256 = hex.EncodeToString(sum[:])
		raw, e := serverpolicy.Sign(pol, key)
		if e != nil {
			t.Fatal(e)
		}
		bundle.Store(raw)
	}
	publish(caA)
	if _, err := runServerPolicy(context.Background(), "", false); err != nil {
		t.Fatalf("initial transition: %v", err)
	}
	pol.Sequence = 2
	pol.From = []serverpolicy.Endpoints{pol.To}
	publish(caB)
	active.Store(certB)
	if _, err := runServerPolicy(context.Background(), "", false); err != nil {
		t.Fatalf("offline root rotation: %v", err)
	}
	state, err := readServerPolicyState("")
	if err != nil {
		t.Fatal(err)
	}
	if state.Sequence != 2 || state.BootstrapEndpoint != source.URL || bootstrapGets.Load() != 2 {
		t.Fatalf("rotation did not use retained bootstrap: %+v requests=%d", state, bootstrapGets.Load())
	}
	current, err := profile.Load()
	if err != nil {
		t.Fatal(err)
	}
	client, err := serverClient(current.Server.CACertFile, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(destination.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
}

// A deferral repeats every five minutes until its owner acts. Recording each
// would push the applied policies out of the 32 kept within hours.
func TestServerPolicyRepeatedFailureRecordedOnce(t *testing.T) {
	useTempSyncHome(t)
	usePolicySigningKey(t)
	var status atomic.Int64
	status.Store(http.StatusInternalServerError)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(int(status.Load())) }))
	t.Cleanup(ts.Close)
	policyReportProfile(t, "", ts.URL)
	run := func() string {
		t.Helper()
		_, err := runServerPolicy(context.Background(), "", false)
		if err == nil {
			t.Fatal("fixture server no longer fails the run")
		}
		return err.Error()
	}
	first := run()
	if again := run(); again != first {
		t.Fatalf("the same failure reads %q then %q", first, again)
	}
	state, err := readServerPolicyState("")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.History) != 1 || state.Status != "failed" || state.Reason != first {
		t.Fatalf("repeated failure recorded as %+v", state)
	}
	status.Store(http.StatusServiceUnavailable)
	if run() == first {
		t.Fatal("fixture failure did not change")
	}
	if state, err = readServerPolicyState(""); err != nil || len(state.History) != 2 {
		t.Fatalf("a new failure was not recorded: %+v %v", state.History, err)
	}
}

// A directory under the named profiles that holds no profile is no profile. The
// run does not read it as a profile that failed to load, so it neither stops a
// policy nor hides what the real profiles share.
func TestServerPolicyRunIgnoresDirectoryWithoutProfile(t *testing.T) {
	policy, _ := policyStateFixture(t, false)
	if err := os.MkdirAll(filepath.Join(profile.ProfilesDir(), "stray"), 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := runServerPolicy(context.Background(), "", false); err != nil {
		t.Fatalf("run: %v", err)
	}

	if state, err := readServerPolicyState(""); err != nil || state.Status != "applied" || state.Sequence != policy.Sequence {
		t.Fatalf("state %+v %v; want policy %d applied", state, err, policy.Sequence)
	}
}

func TestServerPolicyDeferralFindsSharedProfileBesideDirectoryWithoutProfile(t *testing.T) {
	policy, p := policyStateFixture(t, false)
	if err := os.MkdirAll(filepath.Join(profile.ProfilesDir(), "stray"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := profile.SaveNamed(p, "other"); err != nil {
		t.Fatal(err)
	}

	_, _, err := planServerPolicy(p, "", policy, "digest", serverPolicyState{})

	const want = `shared settings with profiles "other" (claude); policy deferred`
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}
