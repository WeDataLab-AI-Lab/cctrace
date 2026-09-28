package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func newSigningKey(t *testing.T) (ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv, base64.StdEncoding.EncodeToString(pub)
}

// signedArtifactServer publishes one artifact plus a manifest signed by priv,
// mirroring what deploy/Dockerfile produces via scripts/sign-update-manifest.go.
func signedArtifactServer(t *testing.T, priv ed25519.PrivateKey, payload []byte) *httptest.Server {
	t.Helper()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	name := "cctrace-" + runtime.GOOS + "-" + runtime.GOARCH + suffix

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	checksum := hex.EncodeToString(sum[:])
	manifest := updateManifest{
		Filename:    name,
		SHA256:      checksum,
		Signature:   base64.StdEncoding.EncodeToString(ed25519.Sign(priv, updateManifestMessage(name, checksum))),
		Version:     testUpdateVersion,
		SignatureV2: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, updateManifestMessageV2(name, checksum, testUpdateVersion))),
	}
	blob, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".manifest.json"), blob, 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.StripPrefix("/downloads/", http.FileServer(http.Dir(dir))))
	t.Cleanup(srv.Close)
	return srv
}

func tryUpdate(t *testing.T, srv *httptest.Server, keys string) error {
	t.Helper()
	return downloadAndApplyUpdateWith(context.Background(), srv.Client(), srv.URL,
		func(r io.Reader) error { _, err := io.ReadAll(r); return err }, keys, testUpdateVersion)
}

// The rotation window: clients carrying both keys accept releases signed by
// either, so the signing key can change without a flag day.
func TestUpdateRotationWindow(t *testing.T) {
	outgoing, outgoingPub := newSigningKey(t)
	incoming, incomingPub := newSigningKey(t)
	both := outgoingPub + "," + incomingPub

	t.Run("client with both keys accepts the outgoing signature", func(t *testing.T) {
		srv := signedArtifactServer(t, outgoing, []byte("release signed by the outgoing key"))
		if err := tryUpdate(t, srv, both); err != nil {
			t.Fatalf("rotation window rejected the outgoing key: %v", err)
		}
	})

	t.Run("client with both keys accepts the incoming signature", func(t *testing.T) {
		srv := signedArtifactServer(t, incoming, []byte("release signed by the incoming key"))
		if err := tryUpdate(t, srv, both); err != nil {
			t.Fatalf("rotation window rejected the incoming key: %v", err)
		}
	})

	// The problem the window exists to solve: without it, the first release signed
	// by the new key strands every installed client.
	t.Run("client with only the outgoing key rejects the incoming signature", func(t *testing.T) {
		srv := signedArtifactServer(t, incoming, []byte("release signed by the incoming key"))
		if err := tryUpdate(t, srv, outgoingPub); err == nil {
			t.Fatal("expected rejection: this is the flag-day failure the window prevents")
		}
	})

	// Widening the trust root must not weaken it — an unlisted key stays rejected.
	t.Run("an unlisted key is rejected even with several configured", func(t *testing.T) {
		attacker, _ := newSigningKey(t)
		srv := signedArtifactServer(t, attacker, []byte("forged release"))
		err := tryUpdate(t, srv, both)
		if err == nil {
			t.Fatal("a key outside the configured set must never verify")
		}
		if !strings.Contains(err.Error(), "signature verification failed") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestParseUpdatePublicKeys(t *testing.T) {
	_, a := newSigningKey(t)
	_, b := newSigningKey(t)

	t.Run("single key stays supported", func(t *testing.T) {
		keys, err := parseUpdatePublicKeys(a)
		if err != nil || len(keys) != 1 {
			t.Fatalf("keys=%d err=%v", len(keys), err)
		}
	})

	t.Run("surrounding whitespace and empty fields are tolerated", func(t *testing.T) {
		keys, err := parseUpdatePublicKeys("  " + a + " , ," + b + "  ")
		if err != nil || len(keys) != 2 {
			t.Fatalf("keys=%d err=%v", len(keys), err)
		}
	})

	t.Run("one malformed entry fails the whole list", func(t *testing.T) {
		// Silently dropping it would leave a client trusting fewer keys than the
		// operator intended, which surfaces only as failed updates in the field.
		if _, err := parseUpdatePublicKeys(a + ",not-base64"); err == nil {
			t.Fatal("expected a malformed entry to be rejected")
		}
	})

	t.Run("empty configuration is still rejected", func(t *testing.T) {
		for _, in := range []string{"", "   ", ",", " , "} {
			if _, err := parseUpdatePublicKeys(in); err == nil {
				t.Fatalf("expected rejection for %q", in)
			}
		}
	})
}
