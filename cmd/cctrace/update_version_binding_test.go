package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"
)

// The manifest signature covered filename and checksum, which identify an
// artifact but not which release it belongs to. Whoever controls the update
// response could therefore serve a genuinely signed OLDER artifact while
// advertising a newer version, and every check passed: the filename matched, the
// checksum matched, the signature verified with a trusted key. The client
// installed a downgrade it had asked to upgrade to (#561).
//
// This is the whole point of the version-bound signature, so it is the test that
// has to fail if that binding is removed.
func TestUpdateRejectsGenuinelySignedOlderArtifact(t *testing.T) {
	publicKey, privateKey := newUpdateSigningKey(t)

	// A real past release: signed correctly, by the real key, for v0.7.20.
	oldBinary := []byte("cctrace v0.7.20 -- genuine, signed, and old")
	srv := manifestServer(t, oldBinary, privateKey, "v0.7.20")

	applied := false
	err := downloadAndApplyUpdateWith(
		context.Background(),
		srv.Client(),
		srv.URL,
		func(io.Reader) error { applied = true; return nil },
		base64.StdEncoding.EncodeToString(publicKey),
		// What the server told the client it was getting.
		"v0.7.49",
	)
	if err == nil {
		t.Fatal("a genuinely signed v0.7.20 artifact was accepted as v0.7.49")
	}
	if !strings.Contains(err.Error(), "signed for v0.7.20") {
		t.Errorf("error does not name the mismatch, so an operator cannot tell a replay from a broken key: %v", err)
	}
	if applied {
		t.Fatal("the downgrade reached the applier")
	}
}

// A manifest with no version is the same attack with the fields removed, so
// absence cannot mean "no opinion".
func TestUpdateRejectsManifestWithoutVersionBinding(t *testing.T) {
	publicKey, privateKey := newUpdateSigningKey(t)
	binary := []byte("cctrace binary")

	srv := manifestServer(t, binary, privateKey, "v0.7.49")
	// Strip the binding the way an attacker replaying an old manifest would.
	stripped := manifestServerWith(t, binary, func(m *updateManifest) {
		m.Version = ""
		m.SignatureV2 = ""
	}, privateKey, "v0.7.49")
	_ = srv

	err := downloadAndApplyUpdateWith(
		context.Background(),
		stripped.Client(),
		stripped.URL,
		func(io.Reader) error { return nil },
		base64.StdEncoding.EncodeToString(publicKey),
		"v0.7.49",
	)
	if err == nil || !strings.Contains(err.Error(), "does not bind a version") {
		t.Fatalf("a manifest with the binding removed was accepted: %v", err)
	}
}

// The v1 and v2 messages must stay distinct, or a v1 signature could be
// presented as a v2 one and the binding would be decorative.
func TestUpdateManifestMessagesAreDistinct(t *testing.T) {
	v1 := string(updateManifestMessage("cctrace-linux-amd64", "abc123"))
	v2 := string(updateManifestMessageV2("cctrace-linux-amd64", "abc123", "v0.7.49"))
	if v1 == v2 {
		t.Fatal("the two signed messages are identical")
	}
	if !strings.HasPrefix(v2, v1) {
		t.Fatalf("v2 no longer extends v1, so the signer and the client may have drifted:\nv1=%q\nv2=%q", v1, v2)
	}
}

// manifestServer serves a correctly signed artifact for the given version.
func manifestServer(t *testing.T, binary []byte, priv ed25519.PrivateKey, version string) *httptest.Server {
	t.Helper()
	return manifestServerWith(t, binary, nil, priv, version)
}

func manifestServerWith(t *testing.T, binary []byte, mutate func(*updateManifest), priv ed25519.PrivateKey, version string) *httptest.Server {
	t.Helper()
	filename := updateFilename()
	sum := sha256.Sum256(binary)
	checksum := hex.EncodeToString(sum[:])
	m := updateManifest{
		Filename:    filename,
		SHA256:      checksum,
		Signature:   signUpdateManifest(priv, filename, checksum),
		Version:     version,
		SignatureV2: signUpdateManifestV2(priv, filename, checksum, version),
	}
	if mutate != nil {
		mutate(&m)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch path.Base(r.URL.Path) {
		case filename:
			_, _ = w.Write(binary)
		case filename + ".manifest.json":
			_, _ = w.Write(raw)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestNewManifestStillVerifiesForOlderClients is the compatibility half, and it
// is the one that matters most.
//
// Every client already installed verifies only `filename\nchecksum\n` and knows
// nothing about the new fields. If the new manifest broke that, this release
// would be uninstallable for all of them -- and a client that cannot install an
// update cannot install the fix for not being able to install updates, which is
// the trap #458 and #623 document with real accounts stuck in it.
//
// So this test reproduces the OLD verification exactly -- three fields, one
// message shape -- and runs it against a manifest built the new way.
func TestNewManifestStillVerifiesForOlderClients(t *testing.T) {
	publicKey, privateKey := newUpdateSigningKey(t)
	binary := []byte("cctrace binary")
	filename := updateFilename()
	sum := sha256.Sum256(binary)
	checksum := hex.EncodeToString(sum[:])

	newStyle := updateManifest{
		Filename:    filename,
		SHA256:      checksum,
		Signature:   signUpdateManifest(privateKey, filename, checksum),
		Version:     "v0.7.50",
		SignatureV2: signUpdateManifestV2(privateKey, filename, checksum, "v0.7.50"),
	}
	raw, err := json.Marshal(newStyle)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// What a pre-#561 client does, written out rather than called, so a change to
	// today's parser cannot quietly change what "the old client" means here.
	var old struct {
		Filename  string `json:"filename"`
		SHA256    string `json:"sha256"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatalf("an older client cannot parse the new manifest: %v", err)
	}
	if old.Filename != filename || old.SHA256 != checksum {
		t.Fatalf("older client read filename=%q sha256=%q", old.Filename, old.SHA256)
	}
	sig, err := base64.StdEncoding.DecodeString(old.Signature)
	if err != nil {
		t.Fatalf("older client cannot decode the signature: %v", err)
	}
	message := []byte(old.Filename + "\n" + old.SHA256 + "\n")
	if !ed25519.Verify(publicKey, message, sig) {
		t.Fatal("an already-installed client would reject this manifest, stranding every existing install")
	}
}
