//go:build ignore

// sign-update-manifest creates an Ed25519-signed manifest beside each update
// artifact. The private-key file must contain the standard-base64 encoding of a
// 64-byte Ed25519 private key. It is intended to be mounted as a BuildKit secret,
// never copied into the build context or image.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// manifest carries two signatures on purpose.
//
// Signature covers filename and checksum only, which is what every client built
// before this change verifies. Dropping it would make this release uninstallable
// for all of them -- and a client that cannot install an update cannot install
// the fix for not being able to install updates (#458, #623).
//
// SignatureV2 adds the version, which is the field that makes a manifest belong
// to one release rather than to any release. Without it, whoever controls the
// update response can serve a genuinely signed older artifact alongside a newer
// advertised version and the client accepts it (#561).
type manifest struct {
	Filename    string `json:"filename"`
	SHA256      string `json:"sha256"`
	Signature   string `json:"signature"`
	Version     string `json:"version"`
	SignatureV2 string `json:"signature_v2"`
}

func main() {
	privateKeyPath := flag.String("private-key", "", "path to a base64-encoded Ed25519 private key")
	publicKeyBase64 := flag.String("public-key", "", "base64-encoded Ed25519 public key embedded in the artifacts")
	version := flag.String("version", "", "release version these artifacts carry, bound into the v2 signature")
	flag.Parse()
	if *privateKeyPath == "" || *publicKeyBase64 == "" || *version == "" || flag.NArg() == 0 {
		fatalf("usage: go run ./scripts/sign-update-manifest.go -private-key <secret-file> -public-key <base64-public-key> -version <version> <artifact> [...]")
	}

	privateKey := readPrivateKey(*privateKeyPath)
	// The client trusts a set of keys (see parseUpdatePublicKeys in cmd/cctrace)
	// so a signing key can be rotated without a flag day. The signing key must be
	// one of them: signing with a key no client trusts produces a release nobody
	// can install, and that only surfaces after it ships.
	signingKey := privateKey.Public().(ed25519.PublicKey)
	if !containsPublicKey(readPublicKeys(*publicKeyBase64), signingKey) {
		fatalf("signing private key is not among the declared update public keys")
	}
	for _, artifactPath := range flag.Args() {
		if err := signArtifact(privateKey, *version, artifactPath); err != nil {
			fatalf("%s: %v", artifactPath, err)
		}
	}
}

func readPrivateKey(path string) ed25519.PrivateKey {
	encoded, err := os.ReadFile(path)
	if err != nil {
		fatalf("read private key: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		fatalf("private key must be standard-base64 encoded Ed25519 private key (%d bytes)", ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(raw)
}

// readPublicKeys parses the comma-separated trust root the clients are built with.
func readPublicKeys(encoded string) []ed25519.PublicKey {
	var keys []ed25519.PublicKey
	for _, field := range strings.Split(encoded, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(field)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			fatalf("public key %q must be standard-base64 encoded Ed25519 public key (%d bytes)", field, ed25519.PublicKeySize)
		}
		keys = append(keys, ed25519.PublicKey(raw))
	}
	if len(keys) == 0 {
		fatalf("no update public key supplied")
	}
	return keys
}

func containsPublicKey(keys []ed25519.PublicKey, want ed25519.PublicKey) bool {
	for _, k := range keys {
		if bytes.Equal(k, want) {
			return true
		}
	}
	return false
}

func signArtifact(privateKey ed25519.PrivateKey, version, artifactPath string) error {
	artifact, err := os.ReadFile(artifactPath)
	if err != nil {
		return fmt.Errorf("read artifact: %w", err)
	}
	sum := sha256.Sum256(artifact)
	checksum := hex.EncodeToString(sum[:])
	filename := filepath.Base(artifactPath)
	signature := ed25519.Sign(privateKey, manifestMessage(filename, checksum))
	signatureV2 := ed25519.Sign(privateKey, manifestMessageV2(filename, checksum, version))

	raw, err := json.Marshal(manifest{
		Filename:    filename,
		SHA256:      checksum,
		Signature:   base64.StdEncoding.EncodeToString(signature),
		Version:     version,
		SignatureV2: base64.StdEncoding.EncodeToString(signatureV2),
	})
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	if err := os.WriteFile(artifactPath+".manifest.json", append(raw, '\n'), 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}

func manifestMessage(filename, checksum string) []byte {
	return []byte(filename + "\n" + checksum + "\n")
}

// manifestMessageV2 appends the version. Appending rather than replacing keeps
// the two messages distinct, so a v1 signature can never be presented as a v2
// one: they are signatures over different byte strings.
func manifestMessageV2(filename, checksum, version string) []byte {
	return []byte(filename + "\n" + checksum + "\n" + version + "\n")
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "sign-update-manifest: "+format+"\n", args...)
	os.Exit(1)
}
