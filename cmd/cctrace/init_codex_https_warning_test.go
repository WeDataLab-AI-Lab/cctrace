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

// Codex sends nothing to an https:// OTLP endpoint behind a private CA unless
// the block carries that CA, and it logs nothing either way (#644; measured on
// 0.160.0). Writing an https block without server.ca_cert_file must say so, or
// Codex tool metrics go missing without a word from either side. With the CA
// set the block carries it, and there is nothing to warn about.
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
			var codexDir string
			run := func(endpoint, caFile string) string {
				home := useTempSyncHome(t)
				codexDir = filepath.Join(home, ".codex")
				if err := os.MkdirAll(codexDir, 0o755); err != nil {
					t.Fatal(err)
				}
				p := profile.NewDefault()
				p.Server.AuthToken = "tok"
				p.Server.CACertFile = caFile
				stdout, _ := captureOutput(t, func() { write(p, endpoint) })
				return stdout
			}
			if out := run("https://cctrace.example.com:4317", ""); !strings.Contains(out, "#644") || !strings.Contains(out, "server.ca_cert_file") {
				t.Fatalf("no https-without-CA warning in output:\n%s", out)
			}
			if out := run("http://192.168.0.10:18080", ""); strings.Contains(out, "#644") {
				t.Fatalf("plain http endpoint warned:\n%s", out)
			}
			ca := writeTestCAFile(t, t.TempDir())
			if out := run("https://cctrace.example.com:4317", ca); strings.Contains(out, "#644") {
				t.Fatalf("https endpoint with a CA warned:\n%s", out)
			}
			config, err := os.ReadFile(filepath.Join(codexDir, "config.toml"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(config), "ca-certificate") {
				t.Fatalf("the profile CA did not reach the Codex block:\n%s", config)
			}
		})
	}
}
