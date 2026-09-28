package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// cctraced accepted no flag but --stop: --help and --version both fell through to
// the normal boot path and died on "JWT_SECRET is required". An operator holding
// the binary had no way to ask what it takes or which build it is, and a deployed
// server's version was only visible through its image tag.
func TestUsageTextCoversEveryEnvKeyTheDaemonReads(t *testing.T) {
	documented := map[string]bool{}
	for _, e := range envDocs {
		documented[e.key] = true
	}

	help := usageText()
	for _, e := range envDocs {
		if !strings.Contains(help, e.key) {
			t.Errorf("envDocs lists %s but the help text does not print it", e.key)
		}
	}

	// The set that must be covered is what the daemon actually reads, not a second
	// hand-written list. Keys read elsewhere in the server (internal/api) may be
	// documented here too, so extras are allowed -- omissions are not.
	for key := range envKeysReadBy(t, "cmd/cctraced") {
		if !documented[key] {
			t.Errorf("cctraced reads %s but --help never mentions it", key)
		}
	}
}

func TestVersionTextNamesTheBinaryAndBuild(t *testing.T) {
	got := versionText()
	if !strings.Contains(got, "cctraced") {
		t.Errorf("version line should name the binary; got %q", got)
	}
	if !strings.Contains(got, version) {
		t.Errorf("version line should carry the build stamp %q; got %q", version, got)
	}
}

// TestHelpIsRequiredEnvFirst pins the ordering that makes the text usable: an
// operator scanning it must hit the variables without which the server refuses to
// start before the optional ones.
func TestHelpIsRequiredEnvFirst(t *testing.T) {
	var seenOptional bool
	for _, e := range envDocs {
		if e.required {
			if seenOptional {
				t.Errorf("required key %s is listed after optional ones", e.key)
			}
			continue
		}
		seenOptional = true
	}
}

var envKeyRe = regexp.MustCompile(`(?:envOr|os\.Getenv|envIntOpt|os\.LookupEnv)\("([A-Z][A-Z0-9_]*)"`)

// envDefault takes two names -- the current one and the deprecated spelling it
// still accepts -- and the daemon reads both, so both must be documented.
var envDefaultRe = regexp.MustCompile(`envDefault\("([A-Z][A-Z0-9_]*)",\s*"([A-Z][A-Z0-9_]*)"\)`)

// envKeysReadBy scans a package's non-test sources for env lookups.
func envKeysReadBy(t *testing.T, dir string) map[string]bool {
	t.Helper()
	root := filepath.Join("..", "..", dir)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading %s: %v", root, err)
	}
	keys := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		for _, m := range envKeyRe.FindAllStringSubmatch(string(raw), -1) {
			keys[m[1]] = true
		}
		for _, m := range envDefaultRe.FindAllStringSubmatch(string(raw), -1) {
			keys[m[1]], keys[m[2]] = true, true
		}
	}
	if len(keys) == 0 {
		t.Fatalf("%s: found no env lookups -- the scan pattern has drifted", root)
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	t.Logf("env keys read by %s: %s", dir, strings.Join(sorted, " "))
	return keys
}

// The access logger used to be a package-level var initializer, so it ran before
// main: every invocation -- including --help, --version and --stop -- created
// /data/logs and could print an open-failure warning ahead of the output the
// operator asked for. Binding the path at call time is what makes those commands
// answerable without touching the filesystem.
func TestAccessLoggerBindsPathWhenCalled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "access.log")
	logger, closer := newAccessLogger(path)
	if closer == nil {
		t.Fatal("want a file-backed logger, got the stdout fallback")
	}
	// Close before the TempDir cleanup runs: Windows refuses to delete a file
	// that still has an open handle, and t.Cleanup runs after this function.
	defer closer.Close()
	logger.Print("hello")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("want the log file created at call time: %v", err)
	}
}
