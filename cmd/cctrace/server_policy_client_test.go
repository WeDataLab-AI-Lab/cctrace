package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cctrace/internal/profile"
	"cctrace/internal/serverpolicy"
)

// Environment changes are process-wide; these tests must not run in parallel.
func TestServerPolicyIdentityStableAndProfileScoped(t *testing.T) {
	useTempSyncHome(t)
	a, err := serverPolicyTarget("")
	if err != nil {
		t.Fatal(err)
	}
	b, err := serverPolicyTarget("work")
	if err != nil {
		t.Fatal(err)
	}
	c, err := serverPolicyTarget("")
	if err != nil {
		t.Fatal(err)
	}
	if a.InstallationID == "" || a != c || a.InstallationID != b.InstallationID || b.Profile != "work" {
		t.Fatalf("unstable targets: %#v %#v %#v", a, b, c)
	}
	if _, err := serverPolicyTarget("../escape"); err == nil {
		t.Fatal("invalid profile accepted")
	}
	if _, err := os.Stat(profile.DefaultPath()); !os.IsNotExist(err) {
		t.Fatal("identity creation wrote a profile")
	}
}

func TestServerPolicyCorruptIdentityIsNotReplaced(t *testing.T) {
	useTempSyncHome(t)
	if err := os.MkdirAll(profile.DefaultDir(), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(profile.DefaultDir(), "installation-id")
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := serverPolicyTarget(""); err == nil {
		t.Fatal("corrupt identity silently replaced")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "broken" {
		t.Fatal("identity changed")
	}
}

func TestServerPolicyFetchOldServerAndDisabledAreNoop(t *testing.T) {
	t.Parallel()
	for _, code := range []int{http.StatusNotFound, http.StatusNoContent} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/client-config-policy" || r.URL.Query().Get("installation_id") != "test-install" || r.URL.Query().Get("profile") != "work" {
					t.Errorf("wrong request: %s", r.URL)
				}
				if r.Header.Get("Authorization") != "" {
					t.Error("policy request disclosed auth token")
				}
				w.WriteHeader(code)
			}))
			defer srv.Close()
			p := profile.NewDefault()
			p.Server.SyncEndpoint, p.Server.AuthToken = srv.URL, "private-token"
			pol, _, err := fetchServerPolicy(context.Background(), p, serverpolicy.Target{InstallationID: "test-install", Profile: "work"}, nil, time.Now())
			if err != nil || pol != nil {
				t.Fatalf("disabled policy: %v %v", pol, err)
			}
		})
	}
}

func TestServerPolicyFetchNeverFollowsRedirect(t *testing.T) {
	t.Parallel()
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("redirect followed") }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	p := profile.NewDefault()
	p.Server.SyncEndpoint = source.URL
	if _, _, err := fetchServerPolicy(context.Background(), p, serverpolicy.Target{}, nil, time.Now()); err == nil {
		t.Fatal("redirect accepted")
	}
}

func TestServerPolicyFetchRejectsUnsignedAndOversize(t *testing.T) {
	t.Parallel()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	for _, body := range [][]byte{[]byte(`{"policy":{},"signature":""}`), make([]byte, serverpolicy.MaxBundleBytes+1)} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
		p := profile.NewDefault()
		p.Server.SyncEndpoint = srv.URL
		if _, _, err := fetchServerPolicy(context.Background(), p, serverpolicy.Target{}, []ed25519.PublicKey{pub}, time.Now()); err == nil {
			t.Error("invalid response accepted")
		}
		srv.Close()
	}
}

func TestServerPolicyBootstrapNeverBypassesHTTPOrSignatureErrors(t *testing.T) {
	t.Parallel()
	for _, status := range []int{200, 204, 404, 503, 302} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("bootstrap used without transport failure") }))
			defer fallback.Close()
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", fallback.URL)
				w.WriteHeader(status)
				w.Write([]byte(`{"invalid":true}`))
			}))
			defer source.Close()
			p := profile.NewDefault()
			p.Server.SyncEndpoint = source.URL
			p.Server.AuthToken = "private"
			_, _, err := fetchServerPolicyWithBootstrap(context.Background(), p, serverpolicy.Target{}, nil, time.Now(), serverPolicyState{BootstrapEndpoint: fallback.URL})
			if (status == 204 || status == 404) && err != nil {
				t.Fatal(err)
			}
			if status != 204 && status != 404 && err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
}
func TestServerPolicyBootstrapOriginRestrictions(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"http://cctrace.company.example:8443", "http://localhost", "https://localhost:1234", "http://user@localhost:1234", "http://localhost:1234/path", "http://localhost:1234?", "http://localhost:1234#"} {
		if policyBootstrapAllowed(bad, "https://localhost:1235") {
			t.Fatalf("accepted bootstrap %s", bad)
		}
	}
	if !policyBootstrapAllowed("http://localhost:1234", "https://localhost:1235") {
		t.Fatal("rejected signed same host port")
	}
}

// The policy request says this client applies a policy for every profile on the
// server; the server's handler serves one only to a client that says so.
func TestFetchServerPolicyAcceptsAllTargets(t *testing.T) {
	useTempSyncHome(t)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := os.ReadFile(writeTestCAFile(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(ca)
	now := time.Now()
	old := serverpolicy.Endpoints{Sync: "http://127.0.0.1:1", OTEL: "http://127.0.0.1:1", Protocol: "grpc"}
	pol := serverpolicy.Policy{Schema: 1, Sequence: 1, ServerHost: "127.0.0.1", NotBefore: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), AllTargets: true, From: []serverpolicy.Endpoints{old}, To: serverpolicy.Endpoints{Sync: "https://127.0.0.1:1", OTEL: "https://127.0.0.1:1", Protocol: "http/protobuf"}, CAPEM: string(ca), CASHA256: hex.EncodeToString(sum[:])}
	raw, err := serverpolicy.Sign(pol, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(serverpolicy.Handler(path))
	defer server.Close()
	p := profile.NewDefault()
	p.Server.SyncEndpoint = server.URL
	got, _, err := fetchServerPolicy(context.Background(), p, serverpolicy.Target{InstallationID: "any-install", Profile: "work"}, []ed25519.PublicKey{pub}, now)
	if err != nil || got == nil || !got.AllTargets {
		t.Fatalf("policy for all targets not received: %+v, %v", got, err)
	}
}
