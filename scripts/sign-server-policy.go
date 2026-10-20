//go:build ignore

// sign-server-policy signs a bounded server migration policy with the same
// base64 Ed25519 secret-file format used by sign-update-manifest.
package main

import (
	"bytes"
	"cctrace/internal/serverpolicy"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	secret := flag.String("private-key", "", "path to base64 Ed25519 private key")
	public := flag.String("public-key", "", "comma-separated base64 public keys trusted by clients")
	output := flag.String("out", "", "output signed bundle path")
	flag.Parse()
	if *secret == "" || *public == "" || *output == "" || flag.NArg() != 1 {
		fatal("usage: go run ./scripts/sign-server-policy.go -private-key <secret-file> -public-key <base64-public-keys> -out <bundle.json> <policy.json>")
	}
	encoded, err := os.ReadFile(*secret)
	if err != nil {
		fatal("read private key: %v", err)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil || len(key) != ed25519.PrivateKeySize {
		fatal("private key must be standard-base64 encoded Ed25519 private key (%d bytes)", ed25519.PrivateKeySize)
	}
	privateKey := ed25519.PrivateKey(key)
	trusted := false
	for _, field := range strings.Split(*public, ",") {
		if strings.TrimSpace(field) == "" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(field))
		if err != nil || len(raw) != ed25519.PublicKeySize {
			fatal("invalid public key")
		}
		if bytes.Equal(raw, privateKey.Public().(ed25519.PublicKey)) {
			trusted = true
		}
	}
	if !trusted {
		fatal("signing private key is not among declared policy public keys")
	}
	f, err := os.Open(flag.Arg(0))
	if err != nil {
		fatal("read policy: %v", err)
	}
	raw, err := io.ReadAll(io.LimitReader(f, serverpolicy.MaxBundleBytes+1))
	f.Close()
	if err != nil {
		fatal("read policy: %v", err)
	}
	if len(raw) > serverpolicy.MaxBundleBytes {
		fatal("policy exceeds size limit")
	}
	var p serverpolicy.Policy
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		fatal("decode policy: %v", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		fatal("policy contains trailing JSON")
	}
	bundle, err := serverpolicy.Sign(p, privateKey)
	if err != nil {
		fatal("sign policy: %v", err)
	}
	if err := os.WriteFile(*output, append(bundle, '\n'), 0600); err != nil {
		fatal("write bundle: %v", err)
	}
}
func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "sign-server-policy: "+format+"\n", args...)
	os.Exit(1)
}
