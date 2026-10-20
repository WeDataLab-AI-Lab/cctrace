package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"cctrace/internal/serverpolicy"
)

// Policies and updates are signed by different keys that overlap in one place:
// the update key stays in policy trust so a lost policy key is recoverable. The
// overlap runs one way only. Nothing here may let a policy key sign an update.
//
// The two keys are package variables set by ldflags, so every test that calls
// this changes process-wide state and none of them can run in parallel.
func usePolicyTrust(t *testing.T, policyKey, updateKey string) {
	t.Helper()
	oldPolicy, oldUpdate := policyPublicKeyBase64, updatePublicKeyBase64
	t.Cleanup(func() { policyPublicKeyBase64, updatePublicKeyBase64 = oldPolicy, oldUpdate })
	policyPublicKeyBase64, updatePublicKeyBase64 = policyKey, updateKey
}

func signedTestPolicy(t *testing.T, priv ed25519.PrivateKey) []byte {
	t.Helper()
	ca, err := os.ReadFile(writeTestCAFile(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(ca)
	now := time.Now().UTC()
	pol := serverpolicy.Policy{
		Schema: 1, Sequence: 1, ServerHost: "cctrace.company.example",
		NotBefore: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		Targets: []serverpolicy.Target{{InstallationID: "fixture-install"}},
		From:    []serverpolicy.Endpoints{{Sync: "http://cctrace.company.example:4318", OTEL: "http://cctrace.company.example:4317", Protocol: "grpc"}},
		To:      serverpolicy.Endpoints{Sync: "https://cctrace.company.example:8443", OTEL: "https://cctrace.company.example:4318", Protocol: "http/protobuf"},
		CAPEM:   string(ca), CASHA256: hex.EncodeToString(sum[:]),
	}
	raw, err := serverpolicy.Sign(pol, priv)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestTrustedPolicyKeysUnion(t *testing.T) {
	// Not parallel: usePolicyTrust sets the process-wide key variables.
	_, policyPub := newSigningKey(t)
	_, updatePub := newSigningKey(t)
	_, nextPub := newSigningKey(t)
	for _, tc := range []struct {
		name, policy, update string
		want                 []string
	}{
		{"both keys", policyPub, updatePub, []string{policyPub, updatePub}},
		{"policy key only", policyPub, "", []string{policyPub}},
		{"update key only", "", updatePub, []string{updatePub}},
		{"comma lists with whitespace and empty fields", " " + policyPub + " , ," + nextPub + " ", updatePub + ",", []string{policyPub, nextPub, updatePub}},
		{"same key in both", policyPub, policyPub, []string{policyPub}},
		{"same key twice in one list", policyPub + "," + policyPub, "", []string{policyPub}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usePolicyTrust(t, tc.policy, tc.update)
			keys, err := trustedPolicyKeys()
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, key := range keys {
				got = append(got, base64.StdEncoding.EncodeToString(key))
			}
			slices.Sort(got)
			want := slices.Clone(tc.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("trusted keys = %v, want %v", got, want)
			}
		})
	}

	t.Run("no key at all names both build settings", func(t *testing.T) {
		for _, in := range [][2]string{{"", ""}, {"  ", " , "}} {
			usePolicyTrust(t, in[0], in[1])
			_, err := trustedPolicyKeys()
			if err == nil || !strings.Contains(err.Error(), "policy public key is not configured") ||
				!strings.Contains(err.Error(), "main.policyPublicKeyBase64") || !strings.Contains(err.Error(), "main.updatePublicKeyBase64") {
				t.Fatalf("trustedPolicyKeys(%q, %q) error = %v", in[0], in[1], err)
			}
		}
	})

	// One malformed entry fails the whole set, as parseUpdatePublicKeys does:
	// dropping it would trust fewer keys than the build declared, and that shows
	// up only as a device that never migrates. The error names the setting.
	short := base64.StdEncoding.EncodeToString([]byte("short"))
	for _, tc := range []struct{ name, policy, update, want string }{
		{"invalid base64 policy key", "not-base64!", updatePub, "invalid policy public key"},
		{"wrong length policy key", short, updatePub, "invalid policy public key"},
		{"invalid base64 update key", policyPub, "not-base64!", "invalid update public key"},
		{"wrong length update key", policyPub, short, "invalid update public key"},
		{"malformed entry beside a good one", policyPub + ",not-base64!", "", "invalid policy public key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usePolicyTrust(t, tc.policy, tc.update)
			if keys, err := trustedPolicyKeys(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("trustedPolicyKeys() = %d keys, %v; want %q", len(keys), err, tc.want)
			}
		})
	}
}

func TestPolicyCoordinatorStartsWithPolicyKeyOnly(t *testing.T) {
	// Not parallel: sets the process-wide key variables and spawnSyncProcessFn.
	_, policyPub := newSigningKey(t)
	usePolicyTrust(t, policyPub, "")
	oldSpawn := spawnSyncProcessFn
	t.Cleanup(func() { spawnSyncProcessFn = oldSpawn })
	calls := 0
	spawnSyncProcessFn = func([]string, string) (int, error) { calls++; return 1, nil }
	var last time.Time
	maybeStartPolicyCoordinator("test", true, false, false, false, "", &last)
	if calls != 1 {
		t.Fatalf("coordinator starts = %d, want 1 for a build carrying only the policy key", calls)
	}
}

func TestPolicyCoordinatorSkipsWithoutAnyKey(t *testing.T) {
	// Not parallel: sets the process-wide key variables and spawnSyncProcessFn.
	usePolicyTrust(t, "", "")
	oldSpawn := spawnSyncProcessFn
	t.Cleanup(func() { spawnSyncProcessFn = oldSpawn })
	calls := 0
	spawnSyncProcessFn = func([]string, string) (int, error) { calls++; return 1, nil }
	var last time.Time
	maybeStartPolicyCoordinator("test", true, false, false, false, "", &last)
	if calls != 0 {
		t.Fatalf("coordinator starts = %d, want 0 for a build with no key", calls)
	}
}

// What v0.7.63 and earlier do with a policy signed by the policy key: they carry
// only the update key, so they must refuse it. The rollout order depends on it.
func TestVerifyRejectsPolicyKeyWhenOnlyUpdateKeyTrusted(t *testing.T) {
	// Not parallel: usePolicyTrust sets the process-wide key variables.
	policyPriv, policyPub := newSigningKey(t)
	updatePriv, updatePub := newSigningKey(t)
	byPolicyKey := signedTestPolicy(t, policyPriv)
	byUpdateKey := signedTestPolicy(t, updatePriv)

	usePolicyTrust(t, "", updatePub)
	keys, err := trustedPolicyKeys()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := serverpolicy.Verify(byPolicyKey, keys, time.Now()); err == nil || !strings.Contains(err.Error(), "untrusted policy signature") {
		t.Fatalf("update-key-only build verified a policy-key signature: %v", err)
	}
	if _, err := serverpolicy.Verify(byUpdateKey, keys, time.Now()); err != nil {
		t.Fatalf("update-key-only build rejected its own key: %v", err)
	}

	usePolicyTrust(t, policyPub, updatePub)
	if keys, err = trustedPolicyKeys(); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{"policy key": byPolicyKey, "update key": byUpdateKey} {
		if _, err := serverpolicy.Verify(raw, keys, time.Now()); err != nil {
			t.Fatalf("build with both keys rejected a %s signature: %v", name, err)
		}
	}
}

// The update path keeps its own trust set. The manifest below is validly signed
// by the policy key for a checksum the server never serves, so a regression
// shows up as "checksum mismatch" rather than as an applied binary.
func TestUpdateManifestRejectsPolicyKeySignature(t *testing.T) {
	// Not parallel: usePolicyTrust sets the process-wide key variables.
	policyPriv, policyPub := newSigningKey(t)
	updatePriv, updatePub := newSigningKey(t)
	usePolicyTrust(t, policyPub, updatePub)
	signedFor := func(priv ed25519.PrivateKey) func(*updateManifest) {
		return func(m *updateManifest) {
			sum := sha256.Sum256([]byte("an artifact this server never serves"))
			m.SHA256 = hex.EncodeToString(sum[:])
			m.Signature = signUpdateManifest(priv, m.Filename, m.SHA256)
			m.SignatureV2 = signUpdateManifestV2(priv, m.Filename, m.SHA256, m.Version)
		}
	}

	srv := signedUpdateServer(t, []byte("served artifact"), policyPriv, signedFor(policyPriv))
	err := downloadAndApplyUpdate(t.Context(), "", srv.URL, testUpdateVersion)
	if err == nil || !strings.Contains(err.Error(), "manifest signature verification failed") {
		t.Fatalf("update with a policy-key manifest = %v, want signature verification failure", err)
	}

	// Control: the same path accepts the update key's signature and only then
	// stops at the checksum, so the failure above is the key set and nothing else.
	srv = signedUpdateServer(t, []byte("served artifact"), updatePriv, signedFor(updatePriv))
	err = downloadAndApplyUpdate(t.Context(), "", srv.URL, testUpdateVersion)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("update with an update-key manifest = %v, want checksum mismatch", err)
	}
}
