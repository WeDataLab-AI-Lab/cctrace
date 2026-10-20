package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"

	"cctrace/internal/codexconfig"
	"cctrace/internal/configtxn"
	"cctrace/internal/envgen"
	"cctrace/internal/profile"
	"cctrace/internal/serverpolicy"
)

func TestPolicySettingsLocksMissingParentAndExistingCodex(t *testing.T) {
	useTempSyncHome(t)
	p := profile.NewDefault()
	p.ClaudeConfigDir = filepath.Join(t.TempDir(), "new-claude")
	codex := t.TempDir()
	p.Options.CodexDirs = []string{codex}
	if err := os.WriteFile(filepath.Join(codex, "config.toml"), []byte("model='test'"), 0600); err != nil {
		t.Fatal(err)
	}
	release, err := lockPolicySettings(context.Background(), p, false)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err = os.Stat(p.ClaudeConfigDir); !os.IsNotExist(err) {
		t.Fatalf("check created Claude parent: %v", err)
	}
	release, err = lockPolicySettings(context.Background(), p, true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, path := range []string{filepath.Join(p.ClaudeConfigDir, "settings.json.lock"), filepath.Join(codex, "config.toml.lock")} {
		contender := flock.New(path)
		ok, err := contender.TryLock()
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			_ = contender.Unlock()
			t.Fatalf("lock not held: %s", path)
		}
	}
}

func TestPolicySettingsLocksRejectNewlyAppearingHome(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	changes := []configtxn.Change{{Path: path}}
	if err := validatePolicySettingsLocks(changes, map[string]bool{}); err == nil {
		t.Fatal("accepted newly appearing unlocked Codex home")
	}
	if err := validatePolicySettingsLocks(changes, map[string]bool{path: true}); err != nil {
		t.Fatal(err)
	}
	changes[0].Path = filepath.Join(t.TempDir(), "settings.json")
	if err := validatePolicySettingsLocks(changes, map[string]bool{}); err == nil {
		t.Fatal("accepted unlocked Claude settings")
	}
	changes[0].Path = filepath.Join(t.TempDir(), "profile.json")
	if err := validatePolicySettingsLocks(changes, map[string]bool{}); err != nil {
		t.Fatalf("profile is guarded by global lock: %v", err)
	}
}

// Two paths can name one file: ~/.codex and ~/.CODEX on a case-insensitive disk,
// or, as here where any disk can build it, a link to the directory. flock(2)
// locks belong to the open file, so taking one file's lock through both paths
// waits on itself, with the global policy lock held.
func TestLockPolicyFilesTakesOneLockPerFile(t *testing.T) {
	dir := t.TempDir()
	codex := filepath.Join(dir, "codex")
	link := filepath.Join(dir, "codex-link")
	if err := os.MkdirAll(codex, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(codex, link); err != nil {
		t.Fatal(err)
	}
	targets := []string{filepath.Join(codex, "config.toml"), filepath.Join(link, "config.toml")}
	type locked struct {
		release func()
		held    map[string]bool
		err     error
	}
	done := make(chan locked, 1)
	go func() {
		release, held, err := lockPolicyFiles(context.Background(), targets, true)
		done <- locked{release, held, err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		defer got.release()
		// Both names stay writable under the one lock.
		for _, target := range targets {
			if !got.held[target] {
				t.Errorf("%s not reported as locked", target)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("locking one file through two paths waits on itself")
	}
}

// Two lock files can be one file under two names -- here a hard link, which any
// disk can build -- with each config.toml in its own directory. The second open
// would wait on the first the same way a link to the directory does.
func TestLockPolicyFilesTakesOneLockForHardlinkedLockFiles(t *testing.T) {
	dir := t.TempDir()
	first, second := filepath.Join(dir, "codex-a"), filepath.Join(dir, "codex-b")
	for _, d := range []string{first, second} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(first, "config.toml.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(first, "config.toml.lock"), filepath.Join(second, "config.toml.lock")); err != nil {
		t.Fatal(err)
	}
	targets := []string{filepath.Join(first, "config.toml"), filepath.Join(second, "config.toml")}
	type locked struct {
		release func()
		held    map[string]bool
		err     error
	}
	done := make(chan locked, 1)
	go func() {
		release, held, err := lockPolicyFiles(context.Background(), targets, true)
		done <- locked{release, held, err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		defer got.release()
		for _, target := range targets {
			if !got.held[target] {
				t.Errorf("%s not reported as locked", target)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("locking two names of one lock file waits on itself")
	}
}

// hardlinkedCodexConfigs gives p two Codex folders whose config.toml are one
// file under two names, holding p's block.
func hardlinkedCodexConfigs(t *testing.T, home string, p *profile.Profile) (first, second string) {
	t.Helper()
	first, second = filepath.Join(home, "codex-a"), filepath.Join(home, "codex-b")
	if err := os.MkdirAll(second, 0700); err != nil {
		t.Fatal(err)
	}
	p.Options.CodexDirs = []string{first, second}
	if err := codexconfig.WriteOtelBlock(first, p.Server.Endpoint, p.Server.AuthToken, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(first, "config.toml"), filepath.Join(second, "config.toml")); err != nil {
		t.Fatal(err)
	}
	return first, second
}

// Writing a config replaces it by rename, which leaves a hard link's other name
// on the old content. Two names of one file are therefore two configs to write,
// not one: counted once, the second kept sending to the old server.
func TestPolicyCodexConfigPathsKeepHardlinkedEntries(t *testing.T) {
	home := useTempSyncHome(t)
	p := profile.NewDefault()
	p.Server.Endpoint, p.Server.AuthToken = "http://cctrace.company.example:4317", "fixture-token"
	first, second := hardlinkedCodexConfigs(t, home, p)
	paths, err := policyCodexConfigPaths(p)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, dir := range []string{first, second} {
		path, err := canonicalPolicyPath(filepath.Join(dir, "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, path)
	}
	if !slices.Equal(paths, want) {
		t.Fatalf("hard linked configs counted as %v, want %v", paths, want)
	}
}

func TestServerPolicyWritesEveryHardlinkedCodexConfig(t *testing.T) {
	home := useTempSyncHome(t)
	p := profile.NewDefault()
	p.Server.SyncEndpoint, p.Server.Endpoint = "http://cctrace.company.example:4318", "http://cctrace.company.example:4317"
	p.Server.AuthToken = "fixture-token"
	p.ClaudeConfigDir = filepath.Join(t.TempDir(), "claude")
	first, second := hardlinkedCodexConfigs(t, home, p)
	if err := profile.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := envgen.ApplyToClaudeSettings(p); err != nil {
		t.Fatal(err)
	}
	ca, err := os.ReadFile(writeTestCAFile(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	pol := serverpolicy.Policy{Sequence: 1, CAPEM: string(ca), To: serverpolicy.Endpoints{Sync: "https://cctrace.company.example:8443", OTEL: "https://cctrace.company.example:4318", Protocol: "http/protobuf"}}
	changes, _, err := planServerPolicy(p, "", pol, "digest", serverPolicyState{})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyServerPolicyChanges("", changes); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{first, second} {
		got, err := os.ReadFile(filepath.Join(dir, "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(got), pol.To.OTEL) || strings.Contains(string(got), p.Server.Endpoint) {
			t.Errorf("%s keeps the old server:\n%s", dir, got)
		}
	}
}

// A link to a folder is the same folder, whatever the disk.
func TestPolicyCodexConfigPathsCountLinkedFolderOnce(t *testing.T) {
	home := useTempSyncHome(t)
	codex, link := filepath.Join(home, "codex"), filepath.Join(home, "codex-link")
	if err := os.MkdirAll(codex, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(codex, link); err != nil {
		t.Fatal(err)
	}
	p := profile.NewDefault()
	p.Options.CodexDirs = []string{codex, link}
	paths, err := policyCodexConfigPaths(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 {
		t.Fatalf("one folder counted as %v", paths)
	}
}

// What a case-insensitive disk does, stood in for so that every disk checks it:
// ~/.CODEX is ~/.codex, and a name that is not in the same directory is not the
// same entry.
func TestSamePolicyEntry(t *testing.T) {
	dir := t.TempDir()
	codex, other := filepath.Join(dir, "codex"), filepath.Join(dir, "other")
	for _, d := range []string{codex, other} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(dir, "CODEX")
	original := policyStatFn
	t.Cleanup(func() { policyStatFn = original })
	policyStatFn = func(path string) (os.FileInfo, error) {
		if path == alias {
			path = codex
		}
		return os.Stat(path)
	}
	for _, tc := range []struct {
		name string
		a, b string
		want bool
	}{
		{"same path", filepath.Join(codex, "config.toml"), filepath.Join(codex, "config.toml"), true},
		{"case alias of the folder", filepath.Join(codex, "config.toml"), filepath.Join(alias, "config.toml"), true},
		{"other folder", filepath.Join(codex, "config.toml"), filepath.Join(other, "config.toml"), false},
		{"other name", filepath.Join(codex, "config.toml"), filepath.Join(alias, "settings.json"), false},
		{"folders that do not exist", filepath.Join(dir, "gone-a", "config.toml"), filepath.Join(dir, "gone-b", "config.toml"), false},
	} {
		if got := samePolicyEntry(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: samePolicyEntry = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A sibling whose config is another name of this profile's file shares nothing
// that a write could undo: the rename gives each name its own file.
func TestSharedPolicySettingsDoNotCountHardlinkedConfigsAsShared(t *testing.T) {
	home := useTempSyncHome(t)
	p := profile.NewDefault()
	p.Server.Endpoint, p.Server.AuthToken = "http://cctrace.company.example:4317", "fixture-token"
	p.ClaudeConfigDir = filepath.Join(t.TempDir(), "claude")
	first, second := hardlinkedCodexConfigs(t, home, p)
	p.Options.CodexDirs = []string{first}
	other := profile.NewDefault()
	other.ClaudeConfigDir = filepath.Join(t.TempDir(), "claude")
	other.Options.CodexDirs = []string{second}
	if err := profile.SaveNamed(other, "other"); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureExclusivePolicySettings(p, "", serverpolicy.Policy{}); err != nil {
		t.Fatalf("hard linked configs treated as shared: %v", err)
	}
}

// caseInsensitiveDisk reports whether names that differ only in case are one
// entry under dir: macOS by default, Linux usually not.
func caseInsensitiveDisk(t *testing.T, dir string) bool {
	t.Helper()
	if err := os.Mkdir(filepath.Join(dir, "case-probe"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := os.Stat(filepath.Join(dir, "CASE-PROBE"))
	return err == nil
}

// On a case-insensitive disk ~/.CODEX is ~/.codex. The policy counts that Codex
// config once, in what it locks and in what it plans to write; counted twice it
// was locked twice and written by two changes of one transaction.
func TestPolicySettingsCountCaseAliasOnce(t *testing.T) {
	home := useTempSyncHome(t)
	if !caseInsensitiveDisk(t, home) {
		t.Skip("case-sensitive disk: ~/.CODEX is another directory here; TestLockPolicyFilesTakesOneLockPerFile covers the lock")
	}
	p := profile.NewDefault()
	p.Server.SyncEndpoint, p.Server.Endpoint = "http://cctrace.company.example:4318", "http://cctrace.company.example:4317"
	p.Server.AuthToken = "fixture-token"
	p.ClaudeConfigDir = filepath.Join(t.TempDir(), "claude")
	p.Options.CodexDirs = []string{filepath.Join(home, ".CODEX")}
	if err := profile.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := envgen.ApplyToClaudeSettings(p); err != nil {
		t.Fatal(err)
	}
	if err := codexconfig.WriteOtelBlock(filepath.Join(home, ".codex"), p.Server.Endpoint, p.Server.AuthToken, ""); err != nil {
		t.Fatal(err)
	}
	countCodex := func(paths []string) int {
		n := 0
		for _, path := range paths {
			if filepath.Base(path) == "config.toml" {
				n++
			}
		}
		return n
	}
	locked, _, err := policySettingsPaths(p)
	if err != nil {
		t.Fatal(err)
	}
	pol := serverpolicy.Policy{Sequence: 1, CASHA256: "fixture", To: serverpolicy.Endpoints{Sync: "https://cctrace.company.example:8443", OTEL: "https://cctrace.company.example:4318", Protocol: "http/protobuf"}}
	changes, _, err := planServerPolicy(p, "", pol, "digest", serverPolicyState{})
	if err != nil {
		t.Fatal(err)
	}
	var planned []string
	for _, c := range changes {
		planned = append(planned, c.Path)
	}
	if countCodex(locked) != 1 || countCodex(planned) != 1 {
		t.Fatalf("one Codex config counted as locked %v, planned %v", locked, planned)
	}
}

// Between profiles too: one that lists codex-a and Claude home claude shares
// both with one that lists CODEX-A and CLAUDE.
func TestSharedPolicySettingsSeeCaseAlias(t *testing.T) {
	home := useTempSyncHome(t)
	if !caseInsensitiveDisk(t, home) {
		t.Skip("case-sensitive disk: CODEX-A is another directory here")
	}
	for _, dir := range []string{"codex-a", "claude"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	p := profile.NewDefault()
	p.ClaudeConfigDir = filepath.Join(home, "claude")
	p.Options.CodexDirs = []string{filepath.Join(home, "codex-a")}
	if err := profile.Save(p); err != nil {
		t.Fatal(err)
	}
	other := profile.NewDefault()
	other.ClaudeConfigDir = filepath.Join(home, "CLAUDE")
	other.Options.CodexDirs = []string{filepath.Join(home, "CODEX-A")}
	if err := profile.SaveNamed(other, "home"); err != nil {
		t.Fatal(err)
	}
	_, err := ensureExclusivePolicySettings(p, "", serverpolicy.Policy{})
	if want := `shared settings with profiles "home" (claude), "home" (codex); policy deferred`; err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

// The settings writers a policy run waits for hold their lock for the length of
// one read-modify-write of a small file. policySettingsLockWait is chosen against
// these times, so a writer that came to hold its lock for seconds would show here
// before it made policy runs give up.
func TestPolicySettingsLockHoldTimes(t *testing.T) {
	useTempSyncHome(t)
	p := profile.NewDefault()
	p.ClaudeConfigDir = filepath.Join(t.TempDir(), "claude")
	p.Server.Endpoint = "http://localhost:4317"
	codex := t.TempDir()
	const rounds = 30
	var claude, toml []time.Duration
	for i := 0; i < rounds; i++ {
		p.Server.Endpoint = "http://localhost:" + strconv.Itoa(4000+i)
		start := time.Now()
		if err := envgen.ApplyToClaudeSettings(p); err != nil {
			t.Fatal(err)
		}
		claude = append(claude, time.Since(start))
		start = time.Now()
		if err := codexconfig.WriteOtelBlock(codex, p.Server.Endpoint, "token", ""); err != nil {
			t.Fatal(err)
		}
		toml = append(toml, time.Since(start))
	}
	for name, times := range map[string][]time.Duration{"claude settings": claude, "codex config": toml} {
		slices.Sort(times)
		longest := times[len(times)-1]
		t.Logf("%s: median %v, 95th %v, longest %v of %d writes", name, times[len(times)/2], times[len(times)*95/100], longest, len(times))
		if longest >= policySettingsLockWait/2 {
			t.Errorf("%s write took %v, within a factor of two of the %v a policy run waits for its lock", name, longest, policySettingsLockWait)
		}
	}
}
