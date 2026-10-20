package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *lockedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buf.Bytes())
}

func TestLocalServeBootstrapAndCancel(t *testing.T) {
	claude, codex := t.TempDir(), t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output lockedBuffer
	done := make(chan error, 1)
	go func() {
		done <- runLocalServe(ctx, &output, localServeOptions{Address: "127.0.0.1:0", Origins: []string{"http://127.0.0.1:60895"}, ClaudeDirs: []string{claude}, CodexDirs: []string{codex}})
	}()
	// Writer signals readiness without time-based sleeps.
	for !bytes.Contains(output.Bytes(), []byte("\n")) {
		select {
		case err := <-done:
			t.Fatalf("serve stopped early: %v", err)
		default:
		}
	}
	var bootstrap struct {
		Endpoint           string   `json:"endpoint"`
		Token              string   `json:"token"`
		ProfileFingerprint string   `json:"profile_fingerprint"`
		Origins            []string `json:"origins"`
	}
	line := bytes.SplitN(output.Bytes(), []byte("\n"), 2)[0]
	if err := json.Unmarshal(line, &bootstrap); err != nil || !strings.HasPrefix(bootstrap.Endpoint, "http://127.0.0.1:") || len(bootstrap.Token) != 43 || len(bootstrap.ProfileFingerprint) != 32 {
		t.Fatalf("invalid bootstrap: %s (%v)", line, err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestLocalServeRejectsExternalBind(t *testing.T) {
	for _, address := range []string{"0.0.0.0:0", "localhost:0", "192.0.2.1:0"} {
		if err := runLocalServe(context.Background(), &bytes.Buffer{}, localServeOptions{Address: address, Origins: []string{"http://localhost:60895"}, ClaudeDirs: []string{t.TempDir()}, CodexDirs: []string{t.TempDir()}}); err == nil {
			t.Errorf("accepted %s", address)
		}
	}
}
func TestLocalCommandRegistered(t *testing.T) {
	cmd, _, err := newRootCmd().Find([]string{"local", "serve"})
	if err != nil || cmd == nil || cmd.Name() != "serve" {
		t.Fatalf("local serve missing: %v", err)
	}
}
func TestLocalServeNeverWritesProfile(t *testing.T) {
	claude, codex := t.TempDir(), t.TempDir()
	marker := filepath.Join(claude, "profile.json")
	if err := os.WriteFile(marker, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = runLocalServe(ctx, &bytes.Buffer{}, localServeOptions{Address: "127.0.0.1:0", Origins: []string{"http://127.0.0.1:60895"}, ClaudeDirs: []string{claude}, CodexDirs: []string{codex}})
	b, err := os.ReadFile(marker)
	if err != nil || string(b) != "unchanged" {
		t.Fatalf("profile changed: %v", err)
	}
}
