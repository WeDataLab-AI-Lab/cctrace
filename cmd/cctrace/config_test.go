package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/profile"
)

func TestFlattenProfileIncludesCodexSyncEnabled(t *testing.T) {
	p := profile.NewDefault()
	p.Options.CodexSyncEnabled = true

	for _, s := range flattenProfile(p) {
		if s.key == "options.codex_sync_enabled" {
			if s.value != "true" {
				t.Fatalf("options.codex_sync_enabled = %q, want true", s.value)
			}
			return
		}
	}

	t.Fatal("options.codex_sync_enabled missing from config settings")
}

func TestSetProfileFieldCodexSyncEnabled(t *testing.T) {
	p := profile.NewDefault()

	if err := setProfileField(p, "options.codex_sync_enabled", "true"); err != nil {
		t.Fatalf("setProfileField: %v", err)
	}
	if !p.Options.CodexSyncEnabled {
		t.Fatal("CodexSyncEnabled = false, want true")
	}
}

func TestFlattenProfileIncludesGjcSyncEnabled(t *testing.T) {
	p := profile.NewDefault()
	p.Options.GjcSyncEnabled = true

	for _, s := range flattenProfile(p) {
		if s.key == "options.gjc_sync_enabled" {
			if s.value != "true" {
				t.Fatalf("options.gjc_sync_enabled = %q, want true", s.value)
			}
			return
		}
	}

	t.Fatal("options.gjc_sync_enabled missing from config settings")
}

func TestSetProfileFieldGjcSyncEnabled(t *testing.T) {
	p := profile.NewDefault()

	if err := setProfileField(p, "options.gjc_sync_enabled", "true"); err != nil {
		t.Fatalf("setProfileField: %v", err)
	}
	if !p.Options.GjcSyncEnabled {
		t.Fatal("GjcSyncEnabled = false, want true")
	}
}

func TestFlattenProfileIncludesOmoSyncEnabled(t *testing.T) {
	p := profile.NewDefault()
	p.Options.OmoSyncEnabled = true

	for _, s := range flattenProfile(p) {
		if s.key == "options.omo_sync_enabled" {
			if s.value != "true" {
				t.Fatalf("options.omo_sync_enabled = %q, want true", s.value)
			}
			return
		}
	}

	t.Fatal("options.omo_sync_enabled missing from config settings")
}

func TestSetProfileFieldOmoSyncEnabled(t *testing.T) {
	p := profile.NewDefault()

	if err := setProfileField(p, "options.omo_sync_enabled", "true"); err != nil {
		t.Fatalf("setProfileField: %v", err)
	}
	if !p.Options.OmoSyncEnabled {
		t.Fatal("OmoSyncEnabled = false, want true")
	}
}

func TestFlattenProfileIncludesGjcDirs(t *testing.T) {
	p := profile.NewDefault()

	for _, s := range flattenProfile(p) {
		if s.key == "options.gjc_dirs" {
			if s.value != "(default only)" {
				t.Fatalf("options.gjc_dirs = %q, want (default only)", s.value)
			}
			p.Options.GjcDirs = []string{"/a", "/b"}
			for _, s2 := range flattenProfile(p) {
				if s2.key == "options.gjc_dirs" && s2.value != "/a,/b" {
					t.Fatalf("options.gjc_dirs = %q, want /a,/b", s2.value)
				}
			}
			return
		}
	}

	t.Fatal("options.gjc_dirs missing from config settings")
}

func TestSetProfileFieldGjcDirs(t *testing.T) {
	p := profile.NewDefault()
	dirA := t.TempDir()
	dirB := t.TempDir()

	if err := setProfileField(p, "options.gjc_dirs", " "+dirA+" , ,"+dirB+" "); err != nil {
		t.Fatalf("setProfileField: %v", err)
	}
	got := p.Options.GjcDirs
	want := []string{dirA, dirB}
	if len(got) != len(want) {
		t.Fatalf("GjcDirs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("GjcDirs = %v, want %v", got, want)
		}
	}

	if err := setProfileField(p, "options.gjc_dirs", ""); err != nil {
		t.Fatalf("setProfileField clear: %v", err)
	}
	if len(p.Options.GjcDirs) != 0 {
		t.Fatalf("GjcDirs = %v, want empty after clearing", p.Options.GjcDirs)
	}
}

func TestFlattenProfileIncludesCodexDirs(t *testing.T) {
	p := profile.NewDefault()

	for _, s := range flattenProfile(p) {
		if s.key == "options.codex_dirs" {
			if s.value != "(default only)" {
				t.Fatalf("options.codex_dirs = %q, want (default only)", s.value)
			}
			p.Options.CodexDirs = []string{"/a", "/b"}
			for _, s2 := range flattenProfile(p) {
				if s2.key == "options.codex_dirs" && s2.value != "/a,/b" {
					t.Fatalf("options.codex_dirs = %q, want /a,/b", s2.value)
				}
			}
			return
		}
	}

	t.Fatal("options.codex_dirs missing from config settings")
}

func TestSetProfileFieldCodexDirs(t *testing.T) {
	p := profile.NewDefault()
	dirA := t.TempDir()
	dirB := t.TempDir()

	if err := setProfileField(p, "options.codex_dirs", " "+dirA+" , ,"+dirB+" "); err != nil {
		t.Fatalf("setProfileField: %v", err)
	}
	got := p.Options.CodexDirs
	want := []string{dirA, dirB}
	if len(got) != len(want) {
		t.Fatalf("CodexDirs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("CodexDirs = %v, want %v", got, want)
		}
	}

	if err := setProfileField(p, "options.codex_dirs", ""); err != nil {
		t.Fatalf("setProfileField clear: %v", err)
	}
	if len(p.Options.CodexDirs) != 0 {
		t.Fatalf("CodexDirs = %v, want empty after clearing", p.Options.CodexDirs)
	}
}

// Regression lock: "~/xhome" used to be stored verbatim and then silently
// dropped at scan time because no relative directory named "~" exists.
func TestSetProfileFieldCodexDirsExpandsTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	extra := filepath.Join(home, "xhome")
	if err := os.MkdirAll(extra, 0o755); err != nil {
		t.Fatal(err)
	}

	p := profile.NewDefault()
	if err := setProfileField(p, "options.codex_dirs", "~/xhome"); err != nil {
		t.Fatalf("setProfileField: %v", err)
	}
	if len(p.Options.CodexDirs) != 1 || p.Options.CodexDirs[0] != extra {
		t.Fatalf("CodexDirs = %v, want [%s]", p.Options.CodexDirs, extra)
	}
}

// Regression lock: "," is the entry separator, so a directory whose name
// contains a comma used to be split into two non-existent paths and skipped in
// silence. It must be rejected with a hint instead.
func TestSetProfileFieldCodexDirsRejectsCommaInPath(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "comma,dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	p := profile.NewDefault()
	err := setProfileField(p, "options.codex_dirs", dir)
	if err == nil {
		t.Fatalf("setProfileField(%q) = nil, want error; CodexDirs = %v", dir, p.Options.CodexDirs)
	}
	if !strings.Contains(err.Error(), "','") {
		t.Fatalf("error = %v, want a hint about the ',' separator", err)
	}
	if len(p.Options.CodexDirs) != 0 {
		t.Fatalf("CodexDirs = %v, want unchanged on error", p.Options.CodexDirs)
	}
}

func TestSetProfileFieldCodexDirsRejectsMissingAndRelative(t *testing.T) {
	p := profile.NewDefault()

	missing := filepath.Join(t.TempDir(), "gone")
	if err := setProfileField(p, "options.codex_dirs", missing); err == nil {
		t.Fatalf("setProfileField(%q) = nil, want error for missing dir", missing)
	}
	if err := setProfileField(p, "options.codex_dirs", "relative/dir"); err == nil {
		t.Fatal("setProfileField(relative/dir) = nil, want error for relative path")
	}

	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := setProfileField(p, "options.codex_dirs", file); err == nil {
		t.Fatalf("setProfileField(%q) = nil, want error for non-directory", file)
	}
}

// options.omo_dirs is deliberately unsupported: omosyncer scans a single home
// and the profile has no such field. It must be rejected like any other
// unknown key, not silently accepted or dropped.
// The example key must be one that will never exist. This test used to reach for
// options.omo_dirs, which was simply missing at the time (#241 added gjc_dirs and
// not its omo twin) -- so the omission became an assertion, and adding the key
// later turned this test red as if the absence had been deliberate. That is what
// made it hard to tell, in #280, whether omo was left out on purpose.
func TestSetProfileFieldUnknownKeyRejected(t *testing.T) {
	p := profile.NewDefault()

	const notAKey = "options.no_such_setting"
	err := setProfileField(p, notAKey, "/tmp")
	if err == nil {
		t.Fatalf("setProfileField(%s) = nil, want error for unsupported key", notAKey)
	}
	if !strings.Contains(err.Error(), "unknown key: "+notAKey) {
		t.Fatalf("error = %v, want it to name the unknown key", err)
	}
}

// The "Available keys" hint in the unknown-key error is the only place a user
// sees the full list of settable keys; it must name the new gjc/omo keys.
func TestSetProfileFieldErrorListsGjcOmoKeys(t *testing.T) {
	p := profile.NewDefault()

	err := setProfileField(p, "options.bogus", "x")
	if err == nil {
		t.Fatal("setProfileField(options.bogus) = nil, want error")
	}
	for _, want := range []string{"options.gjc_sync_enabled", "options.omo_sync_enabled", "options.gjc_dirs"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to list %s", err, want)
		}
	}
}

func TestFlattenProfileIncludesNewGjcOmoKeys(t *testing.T) {
	p := profile.NewDefault()

	found := map[string]bool{}
	for _, s := range flattenProfile(p) {
		found[s.key] = true
	}
	for _, want := range []string{"options.gjc_sync_enabled", "options.omo_sync_enabled", "options.gjc_dirs"} {
		if !found[want] {
			t.Fatalf("flattenProfile missing %s; list output must include it", want)
		}
	}
}

func TestSetProfileFieldGjcDirsRejectsCommaInPath(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "comma,dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	p := profile.NewDefault()
	err := setProfileField(p, "options.gjc_dirs", dir)
	if err == nil {
		t.Fatalf("setProfileField(%q) = nil, want error; GjcDirs = %v", dir, p.Options.GjcDirs)
	}
	if !strings.Contains(err.Error(), "','") {
		t.Fatalf("error = %v, want a hint about the ',' separator", err)
	}
	if len(p.Options.GjcDirs) != 0 {
		t.Fatalf("GjcDirs = %v, want unchanged on error", p.Options.GjcDirs)
	}
}

// #716: a person names the billing accounts whose records must never leave this
// machine. An entry is an account id, or provider:account_id when the same id
// could exist under another provider.
func TestSetProfileFieldExcludeAccounts(t *testing.T) {
	p := profile.NewDefault()
	if err := setProfileField(p, "options.exclude_accounts", " acct-1 , ,OpenAI:acct-2 "); err != nil {
		t.Fatalf("setProfileField: %v", err)
	}
	got := strings.Join(p.Options.ExcludeAccounts, ",")
	if got != "acct-1,openai:acct-2" {
		t.Fatalf("ExcludeAccounts = %q, want acct-1,openai:acct-2", got)
	}
	shown := false
	for _, s := range flattenProfile(p) {
		if s.key == "options.exclude_accounts" {
			shown = true
			if s.value != "acct-1,openai:acct-2" {
				t.Fatalf("options.exclude_accounts shown as %q", s.value)
			}
		}
	}
	if !shown {
		t.Fatal("options.exclude_accounts missing from config settings")
	}

	if err := setProfileField(p, "options.exclude_accounts", ""); err != nil {
		t.Fatalf("setProfileField clear: %v", err)
	}
	if len(p.Options.ExcludeAccounts) != 0 {
		t.Fatalf("ExcludeAccounts = %v, want empty after clearing", p.Options.ExcludeAccounts)
	}
}

// Session records carry the billing account id and no login address, so an
// address here would match nothing and the account would keep uploading while
// its owner believes it stopped. Refused rather than stored.
func TestSetProfileFieldExcludeAccountsRejectsAddresses(t *testing.T) {
	p := profile.NewDefault()
	err := setProfileField(p, "options.exclude_accounts", "acct-1,personal@example.test")
	if err == nil {
		t.Fatalf("an address was accepted; ExcludeAccounts = %v", p.Options.ExcludeAccounts)
	}
	if len(p.Options.ExcludeAccounts) != 0 {
		t.Fatalf("a refused value was partly stored: %v", p.Options.ExcludeAccounts)
	}
}

// writeTestCAFile writes a freshly generated self-signed CA certificate as PEM,
// the shape Caddy's `tls internal` root has, and returns its path.
func writeTestCAFile(t *testing.T, dir string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "cctrace test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "root.crt")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// server.ca_cert_file is read by every later run, from whatever directory that
// run starts in, so it is stored absolute with "~" expanded.
func TestSetProfileFieldCACertFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	ca := writeTestCAFile(t, home)

	p := profile.NewDefault()
	if err := setProfileField(p, "server.ca_cert_file", "~/root.crt"); err != nil {
		t.Fatalf("setProfileField: %v", err)
	}
	if p.Server.CACertFile != ca {
		t.Fatalf("CACertFile = %q, want %q", p.Server.CACertFile, ca)
	}

	t.Chdir(home)
	p = profile.NewDefault()
	if err := setProfileField(p, "server.ca_cert_file", "root.crt"); err != nil {
		t.Fatalf("setProfileField(relative): %v", err)
	}
	if !filepath.IsAbs(p.Server.CACertFile) {
		t.Fatalf("CACertFile = %q, want an absolute path", p.Server.CACertFile)
	}

	shown := false
	for _, s := range flattenProfile(p) {
		if s.key == "server.ca_cert_file" {
			shown = true
			if s.value != p.Server.CACertFile {
				t.Fatalf("server.ca_cert_file shown as %q, want %q", s.value, p.Server.CACertFile)
			}
		}
	}
	if !shown {
		t.Fatal("server.ca_cert_file missing from config settings")
	}

	if err := setProfileField(p, "server.ca_cert_file", ""); err != nil {
		t.Fatalf("setProfileField clear: %v", err)
	}
	if p.Server.CACertFile != "" {
		t.Fatalf("CACertFile = %q, want empty after clearing", p.Server.CACertFile)
	}
}

// A path that is not a certificate would be stored and then fail every
// connection later, far from the command that put it there.
func TestSetProfileFieldCACertFileRejectsMissingAndNonPEM(t *testing.T) {
	dir := t.TempDir()
	notPEM := filepath.Join(dir, "not.pem")
	if err := os.WriteFile(notPEM, []byte("not a certificate\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, value := range []string{filepath.Join(dir, "missing.crt"), notPEM} {
		p := profile.NewDefault()
		if err := setProfileField(p, "server.ca_cert_file", value); err == nil {
			t.Fatalf("setProfileField(%q) = nil, want error", value)
		}
		if p.Server.CACertFile != "" {
			t.Fatalf("CACertFile = %q, want unchanged on error", p.Server.CACertFile)
		}
	}
}

// The path is echoed to the terminal and written into Claude Code's and Codex's
// configuration. A control character there is never a real file name someone
// meant, and printed back it can rewrite the terminal line it appears on.
func TestSetProfileFieldCACertFileRejectsControlCharacters(t *testing.T) {
	for _, value := range []string{"/tmp/root\n.crt", "/tmp/root\x1b[2K.crt", "/tmp/\troot.crt"} {
		p := profile.NewDefault()
		err := setProfileField(p, "server.ca_cert_file", value)
		if err == nil || !strings.Contains(err.Error(), "control character") {
			t.Fatalf("setProfileField(%q) = %v, want a control character error", value, err)
		}
	}
}

// config set echoes what was stored, which for this key is the expanded absolute
// path, not what was typed: the stored form is the one every later process reads.
func TestConfigSetPrintsStoredCACertPath(t *testing.T) {
	home := setupTestHome(t)
	ca := writeTestCAFile(t, home)
	stdout, _ := captureOutput(t, func() {
		if err := runConfigSet("", "server.ca_cert_file", "~/root.crt"); err != nil {
			t.Errorf("runConfigSet: %v", err)
		}
	})
	if !strings.Contains(stdout, "server.ca_cert_file = "+ca) {
		t.Fatalf("output does not show the stored path %s:\n%s", ca, stdout)
	}
}

// A running sync daemon built its HTTP client from the CA it started with, so
// saving a new CA, or clearing it, does not reach that daemon. The command says
// so instead of letting the saved value read as applied.
func TestConfigSetCACertTellsToRestartTheDaemon(t *testing.T) {
	home := setupTestHome(t)
	writeTestCAFile(t, home)
	for _, value := range []string{"~/root.crt", ""} {
		stdout, _ := captureOutput(t, func() {
			if err := runConfigSet("", "server.ca_cert_file", value); err != nil {
				t.Errorf("runConfigSet(%q): %v", value, err)
			}
		})
		if !strings.Contains(stdout, "cctrace sync --stop") {
			t.Fatalf("config set server.ca_cert_file %q did not mention restarting the daemon:\n%s", value, stdout)
		}
		// Codex's block is rewritten by the sync, not by config set.
		if !strings.Contains(stdout, "Codex's [otel] block") {
			t.Fatalf("config set server.ca_cert_file %q did not say when Codex's config follows:\n%s", value, stdout)
		}
	}
}
