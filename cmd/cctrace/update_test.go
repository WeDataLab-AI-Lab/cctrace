package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"runtime"
	"strings"
	"testing"
)

func TestDownloadAndApplyUpdateRejectsInsecureNonLocalTransport(t *testing.T) {
	endpoints := []string{
		"http://updates.example.com",
		"http://localhost.evil.example",
	}
	for _, endpoint := range endpoints {
		t.Run(endpoint, func(t *testing.T) {
			requested := false
			client := &http.Client{
				Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					requested = true
					return nil, fmt.Errorf("unexpected request")
				}),
			}

			err := downloadAndApplyUpdateWith(
				context.Background(),
				client,
				endpoint,
				func(io.Reader) error { return nil },
				"unused",
				testUpdateVersion,
			)
			if err == nil || !strings.Contains(err.Error(), "HTTPS") {
				t.Fatalf("downloadAndApplyUpdateWith() error = %v, want HTTPS requirement", err)
			}
			if requested {
				t.Fatal("insecure endpoint was requested before transport validation")
			}
		})
	}
}

func TestDownloadAndApplyUpdateVerifiesValidSignatureBeforeApply(t *testing.T) {
	binary := []byte("trusted cctrace binary")
	publicKey, privateKey := newUpdateSigningKey(t)
	srv := signedUpdateServer(t, binary, privateKey, nil)

	var applied bytes.Buffer
	err := downloadAndApplyUpdateWith(
		context.Background(),
		srv.Client(),
		srv.URL,
		func(r io.Reader) error {
			_, err := io.Copy(&applied, r)
			return err
		},
		base64.StdEncoding.EncodeToString(publicKey),
		testUpdateVersion,
	)
	if err != nil {
		t.Fatalf("downloadAndApplyUpdateWith() error = %v", err)
	}
	if !bytes.Equal(applied.Bytes(), binary) {
		t.Fatalf("applied binary = %q, want %q", applied.Bytes(), binary)
	}
}

func TestDownloadAndApplyUpdateRejectsTamperedBinary(t *testing.T) {
	trustedBinary := []byte("trusted cctrace binary")
	tamperedBinary := []byte("tampered cctrace binary")
	publicKey, privateKey := newUpdateSigningKey(t)
	srv := signedUpdateServer(t, tamperedBinary, privateKey, func(manifest *updateManifest) {
		trustedSum := sha256.Sum256(trustedBinary)
		manifest.SHA256 = hex.EncodeToString(trustedSum[:])
		// Both signatures, because the scenario is a fully valid manifest for one
		// artifact served alongside a different one. Leaving v2 stale would make
		// this test pass on the signature check instead of the checksum check,
		// which is a different defence.
		manifest.Signature = signUpdateManifest(privateKey, manifest.Filename, manifest.SHA256)
		manifest.SignatureV2 = signUpdateManifestV2(privateKey, manifest.Filename, manifest.SHA256, manifest.Version)
	})

	applied := false
	err := downloadAndApplyUpdateWith(
		context.Background(),
		srv.Client(),
		srv.URL,
		func(io.Reader) error {
			applied = true
			return nil
		},
		base64.StdEncoding.EncodeToString(publicKey),
		testUpdateVersion,
	)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("downloadAndApplyUpdateWith() error = %v, want checksum mismatch", err)
	}
	if applied {
		t.Fatal("tampered binary was passed to the update applier")
	}
}

func TestDownloadAndApplyUpdateRejectsTamperedManifest(t *testing.T) {
	binary := []byte("trusted cctrace binary")
	publicKey, privateKey := newUpdateSigningKey(t)
	srv := signedUpdateServer(t, binary, privateKey, func(manifest *updateManifest) {
		manifest.SHA256 = strings.Repeat("0", sha256.Size*2)
	})

	applied := false
	err := downloadAndApplyUpdateWith(
		context.Background(),
		srv.Client(),
		srv.URL,
		func(io.Reader) error {
			applied = true
			return nil
		},
		base64.StdEncoding.EncodeToString(publicKey),
		testUpdateVersion,
	)
	if err == nil || !strings.Contains(err.Error(), "manifest signature verification failed") {
		t.Fatalf("downloadAndApplyUpdateWith() error = %v, want signature verification failure", err)
	}
	if applied {
		t.Fatal("binary with tampered manifest was passed to the update applier")
	}
}

func TestDownloadAndApplyUpdateRejectsMissingOrInvalidPublicKey(t *testing.T) {
	tests := []struct {
		name      string
		publicKey string
		wantError string
	}{
		{name: "missing", publicKey: "", wantError: "not configured"},
		{name: "invalid base64", publicKey: "not-base64!", wantError: "invalid update public key"},
		{name: "wrong length", publicKey: base64.StdEncoding.EncodeToString([]byte("short")), wantError: "invalid update public key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requested := false
			client := &http.Client{
				Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					requested = true
					return nil, fmt.Errorf("unexpected request")
				}),
			}

			err := downloadAndApplyUpdateWith(
				context.Background(),
				client,
				"https://updates.example.com",
				func(io.Reader) error { return nil },
				tt.publicKey,
				testUpdateVersion,
			)
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("downloadAndApplyUpdateWith() error = %v, want %q", err, tt.wantError)
			}
			if requested {
				t.Fatal("update was requested before public key validation")
			}
		})
	}
}

func newUpdateSigningKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey() error = %v", err)
	}
	return publicKey, privateKey
}

func signedUpdateServer(
	t *testing.T,
	binary []byte,
	privateKey ed25519.PrivateKey,
	mutateManifest func(*updateManifest),
) *httptest.Server {
	t.Helper()
	filename := updateFilename()
	sum := sha256.Sum256(binary)
	manifest := updateManifest{
		Filename: filename,
		SHA256:   hex.EncodeToString(sum[:]),
	}
	manifest.Version = testUpdateVersion
	manifest.Signature = signUpdateManifest(privateKey, manifest.Filename, manifest.SHA256)
	manifest.SignatureV2 = signUpdateManifestV2(privateKey, manifest.Filename, manifest.SHA256, manifest.Version)
	if mutateManifest != nil {
		mutateManifest(&manifest)
	}
	rawManifest, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch path.Base(r.URL.Path) {
		case filename:
			_, _ = w.Write(binary)
		case filename + ".manifest.json":
			_, _ = w.Write(rawManifest)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func updateFilename() string {
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	return fmt.Sprintf("cctrace-%s-%s%s", runtime.GOOS, runtime.GOARCH, suffix)
}

// testUpdateVersion is what the fake servers advertise and sign for.
const testUpdateVersion = "v9.9.9"

func signUpdateManifest(privateKey ed25519.PrivateKey, filename, checksum string) string {
	signature := ed25519.Sign(privateKey, updateManifestMessage(filename, checksum))
	return base64.StdEncoding.EncodeToString(signature)
}

func signUpdateManifestV2(privateKey ed25519.PrivateKey, filename, checksum, version string) string {
	signature := ed25519.Sign(privateKey, updateManifestMessageV2(filename, checksum, version))
	return base64.StdEncoding.EncodeToString(signature)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
