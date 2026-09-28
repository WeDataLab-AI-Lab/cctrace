package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cctrace/internal/profile"
)

// One sync that rewrites several homes is one decision about one endpoint; the
// warning says so once, not once per home.
func TestHTTPSCodexWarningIsOncePerSync(t *testing.T) {
	home := useTempSyncHome(t)
	envHome := t.TempDir()
	t.Setenv("CODEX_HOME", envHome)
	legacy := "[otel]\nendpoint = \"https://cctrace.example.com:4318\"\n"
	for _, dir := range []string{filepath.Join(home, ".codex"), envHome} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(legacy), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p := profile.NewDefault()
	p.Server.AuthToken = "tok"
	p.Options.CodexSyncEnabled = true
	stdout, _ := captureOutput(t, func() { autoMigrateCodex(p, "", "https://cctrace.example.com:4317") })

	if strings.Count(stdout, "otel 블록을 현재 형식으로") != 2 {
		t.Fatalf("both homes should have been rewritten:\n%s", stdout)
	}
	if n := strings.Count(stdout, "#644"); n != 1 {
		t.Fatalf("https warning printed %d times, want once:\n%s", n, stdout)
	}
}

// #644: Codex opens no connection to an https:// OTLP metrics endpoint and logs
// nothing. Writing such a block must say so, or Codex tool metrics go missing
// without a word from either side.
func TestWritingAnHTTPSCodexEndpointWarns(t *testing.T) {
	cases := map[string]func(p *profile.Profile, endpoint string){
		"init codex patch": func(p *profile.Profile, endpoint string) {
			p.Server.Endpoint = endpoint
			if err := runCodexPatch(p, false, ""); err != nil {
				t.Fatalf("runCodexPatch: %v", err)
			}
		},
		"sync auto-migration": func(p *profile.Profile, endpoint string) {
			autoMigrateCodex(p, "", endpoint)
		},
	}
	for name, write := range cases {
		t.Run(name, func(t *testing.T) {
			run := func(endpoint string) string {
				home := useTempSyncHome(t)
				if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
					t.Fatal(err)
				}
				p := profile.NewDefault()
				p.Server.AuthToken = "tok"
				stdout, _ := captureOutput(t, func() { write(p, endpoint) })
				return stdout
			}
			if out := run("https://cctrace.example.com:4317"); !strings.Contains(out, "#644") {
				t.Fatalf("no https warning in output:\n%s", out)
			}
			if out := run("http://192.168.0.10:18080"); strings.Contains(out, "#644") {
				t.Fatalf("plain http endpoint warned:\n%s", out)
			}
		})
	}
}
