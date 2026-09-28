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

func TestInsecureEndpointEscapeHatch(t *testing.T) {
	const public = "http://trace.example.com" // rejected by the strict policy

	t.Run("off by default", func(t *testing.T) {
		if err := validateUpdateEndpoint(public); err == nil {
			t.Fatal("the transport check must apply unless explicitly opted out")
		}
	})

	t.Run("opts out when set", func(t *testing.T) {
		t.Setenv(allowInsecureEndpointEnv, "1")
		if err := validateUpdateEndpoint(public); err != nil {
			t.Fatalf("override did not apply: %v", err)
		}
	})

	t.Run("only recognised values opt out", func(t *testing.T) {
		for _, v := range []string{"", "0", "false", "no", "maybe", " "} {
			t.Setenv(allowInsecureEndpointEnv, v)
			if err := validateUpdateEndpoint(public); err == nil {
				t.Errorf("%q must not disable the check", v)
			}
		}
		for _, v := range []string{"1", "true", "TRUE", "yes", " 1 "} {
			t.Setenv(allowInsecureEndpointEnv, v)
			if err := validateUpdateEndpoint(public); err != nil {
				t.Errorf("%q should disable the check: %v", v, err)
			}
		}
	})

	// The point of the whole design: relaxing transport must not relax what stops a
	// forged binary. An attacker who can serve over the opened-up transport still
	// cannot produce a manifest the client accepts.
	t.Run("signature verification stays enforced", func(t *testing.T) {
		t.Setenv(allowInsecureEndpointEnv, "1")

		attacker, _, err := signingPair(t)
		if err != nil {
			t.Fatal(err)
		}
		srv := signedArtifactServer(t, attacker, []byte("forged release"))

		_, trusted, err := signingPair(t) // a key the attacker does not hold
		if err != nil {
			t.Fatal(err)
		}
		err = downloadAndApplyUpdateWith(context.Background(), srv.Client(), srv.URL,
			func(r io.Reader) error { _, e := io.ReadAll(r); return e }, trusted, testUpdateVersion)
		if err == nil {
			t.Fatal("override must not let an unsigned-for-us artifact through")
		}
		if !strings.Contains(err.Error(), "signature verification failed") {
			t.Fatalf("unexpected failure mode: %v", err)
		}
	})
}

// signedArtifactServerWithTamper signs a manifest over one payload but serves a
// different one, which is what a compromised mirror looks like: a valid signature
// that does not describe the bytes actually delivered.
func signedArtifactServerWithTamper(t *testing.T, priv ed25519.PrivateKey, signed, served []byte) *httptest.Server {
	t.Helper()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	name := "cctrace-" + runtime.GOOS + "-" + runtime.GOARCH + suffix

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), served, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(signed)
	checksum := hex.EncodeToString(sum[:])
	blob, err := json.Marshal(updateManifest{
		Filename:    name,
		SHA256:      checksum,
		Signature:   base64.StdEncoding.EncodeToString(ed25519.Sign(priv, updateManifestMessage(name, checksum))),
		Version:     testUpdateVersion,
		SignatureV2: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, updateManifestMessageV2(name, checksum, testUpdateVersion))),
	})
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

func signingPair(t *testing.T) (ed25519.PrivateKey, string, error) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}
	return priv, base64.StdEncoding.EncodeToString(pub), nil
}

// The escape hatch is for transport only — it must never turn into "skip checks".
func TestEscapeHatchDoesNotBypassChecksum(t *testing.T) {
	t.Setenv(allowInsecureEndpointEnv, "1")

	priv, pub, err := signingPair(t)
	if err != nil {
		t.Fatal(err)
	}
	srv := signedArtifactServerWithTamper(t, priv, []byte("signed payload"), []byte("swapped payload"))

	err = downloadAndApplyUpdateWith(context.Background(), srv.Client(), srv.URL,
		func(r io.Reader) error { _, e := io.ReadAll(r); return e }, pub, testUpdateVersion)
	if err == nil {
		t.Fatal("a manifest that does not match the served bytes must be rejected")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("unexpected failure mode: %v", err)
	}
}
