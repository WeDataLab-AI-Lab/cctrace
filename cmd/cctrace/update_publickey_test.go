package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// deploy/update-public-key.txt is the trust root the repository declares: the
// Makefile injects it into every local build and deploy/push.sh refuses to release
// against a different key. A malformed value would not fail the build — it would
// produce clients that reject every update, which only shows up in the field.
//
// The open-source mirror carries no trust root on purpose: nobody outside this
// repository holds the matching signing key, and a client built with an empty
// public key refuses automatic updates outright rather than trusting an
// unsigned manifest. So an absent file is only legitimate there, and "there" is
// identified by deploy/push.sh — the release path, which is never exported.
// Missing key plus present release path is a broken internal checkout, and that
// still fails.
func TestRepoUpdatePublicKeyIsUsable(t *testing.T) {
	path := filepath.Join("..", "..", "deploy", "update-public-key.txt")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if _, statErr := os.Stat(filepath.Join("..", "..", "deploy", "push.sh")); statErr == nil {
			t.Fatalf("%s is missing but deploy/push.sh is present: the release path would sign against a trust root this repository no longer declares", path)
		}
		t.Skipf("%s absent and no deploy/push.sh: mirror checkout, which declares no trust root by design", path)
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	encoded := strings.TrimSpace(string(raw))
	if encoded == "" {
		t.Fatalf("%s is empty; locally built clients would fail every self-update", path)
	}
	// The same parser the client uses, so the file cannot drift from what the
	// binary will accept. A comma-separated list is valid — that is the rotation
	// window (see parseUpdatePublicKeys) — and every entry must parse.
	keys, err := parseUpdatePublicKeys(encoded)
	if err != nil {
		t.Fatalf("%s is not a usable Ed25519 public key set: %v", path, err)
	}
	t.Logf("%s declares %d trust root(s)", path, len(keys))
}
