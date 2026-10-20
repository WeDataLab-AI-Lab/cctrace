package profile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRegisterHomeCandidateUsesSharedRegistryWithoutProviderWrites(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := discoveryHome(t, home, ".codex-2", "config.toml")
	r := Registry{Root: filepath.Join(t.TempDir(), "registry")}
	found, err := r.DiscoverHomes(home)
	if err != nil || len(found.Candidates) != 1 {
		t.Fatalf("candidate missing: %+v %v", found, err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	entry, err := r.RegisterHomeCandidate(found.Candidates[0], false)
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "named:codex-2" || entry.Profile.CodexHome != dir || !entry.Profile.RegistrationOnly ||
		entry.Profile.Options.SyncEnabled || entry.Profile.Options.CodexSyncEnabled || !entry.Profile.Options.CodexSyncDeclined {
		t.Fatalf("registration activated an unselected source: %+v", entry.Profile)
	}
	again, err := r.DiscoverHomes(home)
	if err != nil || len(again.Candidates) != 0 {
		t.Fatalf("registered home rediscovered: %+v %v", again, err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	if string(before) != string(after) {
		t.Fatal("registration changed provider settings")
	}
}

func TestRegisterHomeCandidateRejectsImplicitCollection(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	_ = discoveryHome(t, home, ".claude-3", "settings.json")
	r := Registry{Root: filepath.Join(t.TempDir(), "registry")}
	found, err := r.DiscoverHomes(home)
	if err != nil || len(found.Candidates) != 1 {
		t.Fatal(err)
	}
	if _, err := r.RegisterHomeCandidate(found.Candidates[0], true); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("implicit collection accepted: %v", err)
	}
	if _, err := os.Stat(r.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected collection created registry")
	}
}

func TestRegisterHomeCandidateRejectsDirectoryReplacement(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := discoveryHome(t, home, ".claude-3", "settings.json")
	r := Registry{Root: filepath.Join(t.TempDir(), "registry")}
	found, err := r.DiscoverHomes(home)
	if err != nil || len(found.Candidates) != 1 {
		t.Fatalf("candidate missing: %+v %v", found, err)
	}
	if err := os.Rename(dir, dir+"-original"); err != nil {
		t.Fatal(err)
	}
	_ = discoveryHome(t, home, ".claude-3", "settings.json")
	if _, err := r.RegisterHomeCandidate(found.Candidates[0], false); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale directory selection accepted: %v", err)
	}
	entries, err := r.List()
	if err != nil || len(entries) != 0 {
		t.Fatalf("stale candidate mutated registry: %+v %v", entries, err)
	}
}

func discoveryHome(t *testing.T, root, name, marker string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, marker), []byte("synthetic home marker\n"), 0600); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func TestDiscoverHomesFindsBothProvidersWithoutRegistration(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	claude := discoveryHome(t, home, ".claude-3", "settings.json")
	codex := discoveryHome(t, home, ".codex-2", "config.toml")
	_ = discoveryHome(t, home, ".claude-mem", "settings.json")
	_ = discoveryHome(t, home, ".codex-empty", "unrelated.txt")
	r := Registry{Root: filepath.Join(t.TempDir(), "registry")}
	before, _ := os.ReadFile(filepath.Join(claude, "settings.json"))
	result, err := r.DiscoverHomes(home)
	if err != nil || !result.Complete || len(result.Candidates) != 2 {
		t.Fatalf("discovery = %+v, %v", result, err)
	}
	if result.Candidates[0].Provider != "claude" || result.Candidates[0].Home != claude || result.Candidates[0].Name != "claude-3" ||
		result.Candidates[1].Provider != "codex" || result.Candidates[1].Home != codex || result.Candidates[1].Name != "codex-2" {
		t.Fatalf("provider candidates = %+v", result.Candidates)
	}
	if _, err := os.Stat(r.Root); !os.IsNotExist(err) {
		t.Fatal("discovery registered a profile or created storage")
	}
	after, _ := os.ReadFile(filepath.Join(claude, "settings.json"))
	if string(before) != string(after) {
		t.Fatal("discovery changed provider settings")
	}
}

func TestDiscoverHomesSkipsMappedSourcesNotAdditionalScanRoots(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	claude := discoveryHome(t, home, ".claude-3", "settings.json")
	codex := discoveryHome(t, home, ".codex-2", "config.toml")
	r := Registry{Root: privateRegistryRoot(t)}
	entry, err := r.RegisterMapping("mapped", claude, "")
	if err != nil {
		t.Fatal(err)
	}
	entry.Profile.Options.CodexDirs = []string{codex}
	path, _ := r.Path("mapped")
	if err := SaveTo(entry.Profile, path); err != nil {
		t.Fatal(err)
	}
	result, err := r.DiscoverHomes(home)
	if err != nil || !result.Complete || len(result.Candidates) != 1 || result.Candidates[0].Home != codex {
		t.Fatalf("scan roots incorrectly claimed as execution homes: %+v %v", result, err)
	}
}

func TestDiscoverHomesKeepsHealthyCandidateWithMalformedRegistryPeer(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	_ = discoveryHome(t, home, ".claude-3", "settings.json")
	r := Registry{Root: privateRegistryRoot(t)}
	path, _ := r.Path("bad")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := r.DiscoverHomes(home)
	if err != nil || result.Complete || len(result.Candidates) != 1 || len(result.Errors) != 1 {
		t.Fatalf("malformed peer erased valid candidates: %+v %v", result, err)
	}
}

func TestDiscoverHomesRejectsSymlinkAndUnsafeMarkers(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	target := discoveryHome(t, t.TempDir(), ".claude-real", "settings.json")
	if err := os.Symlink(target, filepath.Join(home, ".claude-link")); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".codex-link")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(target, "settings.json"), filepath.Join(dir, "auth.json")); err != nil {
		t.Fatal(err)
	}
	r := Registry{Root: filepath.Join(t.TempDir(), "registry")}
	result, err := r.DiscoverHomes(home)
	if err != nil || len(result.Candidates) != 0 {
		t.Fatalf("unsafe candidate accepted: %+v %v", result, err)
	}
}

func TestDiscoverHomesRegistrationDoesNotClaimUnmappedProvider(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	_ = discoveryHome(t, home, ".claude-3", "settings.json")
	_ = discoveryHome(t, home, ".codex", "config.toml")
	r := Registry{Root: filepath.Join(t.TempDir(), "registry")}
	found, err := r.DiscoverHomes(home)
	if err != nil || len(found.Candidates) != 2 {
		t.Fatalf("initial candidates %+v %v", found, err)
	}
	if _, err := r.RegisterHomeCandidate(found.Candidates[0], false); err != nil {
		t.Fatal(err)
	}
	remaining, err := r.DiscoverHomes(home)
	if err != nil || len(remaining.Candidates) != 1 || remaining.Candidates[0].Provider != "codex" {
		t.Fatalf("registration claimed absent provider mapping: %+v %v", remaining, err)
	}
}

func TestRegisterHomeCandidateRejectsProviderStorageOverlap(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := discoveryHome(t, home, ".codex-2", "config.toml")
	r := Registry{Root: filepath.Join(dir, "cctrace")}
	found, err := r.DiscoverHomes(home)
	if err != nil || len(found.Candidates) != 1 {
		t.Fatalf("missing candidate %+v %v", found, err)
	}
	if _, err := r.RegisterHomeCandidate(found.Candidates[0], false); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("provider storage overlap accepted: %v", err)
	}
	if _, err := os.Stat(r.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("registration wrote provider files")
	}
}

func TestHomeExclusionPersistsAndGuardsStaleRegistration(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := discoveryHome(t, home, ".codex-2", "config.toml")
	_ = discoveryHome(t, home, ".claude-3", "settings.json")
	r := Registry{Root: filepath.Join(t.TempDir(), "registry")}
	found, err := r.DiscoverHomes(home)
	if err != nil {
		t.Fatal(err)
	}
	selected := found.Candidates[1]
	before, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err := r.SetHomeExcluded(selected, true); err != nil {
		t.Fatal(err)
	}
	r = Registry{Root: r.Root}
	found, err = r.DiscoverHomes(home)
	if err != nil || len(found.Candidates) != 2 || !found.Candidates[1].Excluded || found.Candidates[0].Excluded {
		t.Fatalf("exclusions lost: %+v %v", found, err)
	}
	if _, err := r.RegisterHomeCandidate(selected, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("excluded stale registration: %v", err)
	}
	if err := r.SetHomeExcluded(found.Candidates[1], false); err != nil {
		t.Fatal(err)
	}
	entries, err := r.List()
	if err != nil || len(entries) != 0 {
		t.Fatalf("restore registered: %+v %v", entries, err)
	}
	if _, err := r.RegisterHomeCandidate(selected, false); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	if string(before) != string(after) {
		t.Fatal("provider modified")
	}
}

func TestHomeExclusionRejectsUnsafeMetadata(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"symlink", "shared", "oversized", "malformed", "null"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			_ = discoveryHome(t, home, ".codex-2", "config.toml")
			r := Registry{Root: privateRegistryRoot(t)}
			found, err := r.DiscoverHomes(home)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(r.Root, "home-discovery-exclusions.json")
			data := []byte("{")
			switch kind {
			case "symlink":
				target := filepath.Join(t.TempDir(), "target")
				_ = os.WriteFile(target, []byte("{}"), 0600)
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "shared":
				_ = os.WriteFile(path, []byte("{}"), 0644)
			case "oversized":
				_ = os.WriteFile(path, make([]byte, (1<<20)+1), 0600)
			case "null":
				_ = os.WriteFile(path, []byte("null"), 0600)
			default:
				_ = os.WriteFile(path, data, 0600)
			}
			if _, err := r.DiscoverHomes(home); err == nil {
				t.Fatal("unsafe discovery accepted")
			}
			if err := r.SetHomeExcluded(found.Candidates[0], true); err == nil {
				t.Fatal("unsafe metadata overwritten")
			}
			if _, err := r.RegisterHomeCandidate(found.Candidates[0], false); err == nil {
				t.Fatal("unsafe metadata bypassed")
			}
		})
	}
}

func TestHomeExclusionRejectsReplacementAndPreservesPeer(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	first := discoveryHome(t, home, ".claude-2", "settings.json")
	_ = discoveryHome(t, home, ".codex-2", "config.toml")
	r := Registry{Root: filepath.Join(t.TempDir(), "registry")}
	found, err := r.DiscoverHomes(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SetHomeExcluded(found.Candidates[1], true); err != nil {
		t.Fatal(err)
	}
	if err := r.SetHomeExcluded(found.Candidates[0], true); err != nil {
		t.Fatal(err)
	}
	if err := r.SetHomeExcluded(found.Candidates[0], false); err != nil {
		t.Fatal(err)
	}
	next, err := r.DiscoverHomes(home)
	if err != nil || next.Candidates[0].Excluded || !next.Candidates[1].Excluded {
		t.Fatalf("peer lost %+v %v", next, err)
	}
	if err := os.Rename(first, first+"-old"); err != nil {
		t.Fatal(err)
	}
	_ = discoveryHome(t, home, ".claude-2", "settings.json")
	if err := r.SetHomeExcluded(found.Candidates[0], true); !errors.Is(err, ErrConflict) {
		t.Fatalf("replacement accepted %v", err)
	}
}

func TestHomeExclusionPreservesProfileAndConcurrentChoices(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	mapped := discoveryHome(t, home, ".claude-mapped", "settings.json")
	_ = discoveryHome(t, home, ".claude-2", "settings.json")
	_ = discoveryHome(t, home, ".codex-2", "config.toml")
	r := Registry{Root: privateRegistryRoot(t)}
	entry, err := r.RegisterMapping("existing", mapped, "")
	if err != nil {
		t.Fatal(err)
	}
	entry.Profile.Options.ExcludeAccounts = []string{"synthetic-account"}
	path, _ := r.Path("existing")
	if err := SaveTo(entry.Profile, path); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	found, err := r.DiscoverHomes(home)
	if err != nil || len(found.Candidates) != 2 {
		t.Fatalf("candidates %+v %v", found, err)
	}
	results := make(chan error, 2)
	for _, candidate := range found.Candidates {
		go func(candidate HomeCandidate) { results <- r.SetHomeExcluded(candidate, true) }(candidate)
	}
	for range found.Candidates {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	next, err := r.DiscoverHomes(home)
	if err != nil || len(next.Candidates) != 2 || !next.Candidates[0].Excluded || !next.Candidates[1].Excluded {
		t.Fatalf("lost choices %+v %v", next, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("existing profile/account exclusion changed")
	}
	exclusions := filepath.Join(r.Root, homeExclusionsFile)
	info, err := os.Lstat(exclusions)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("not private %v %v", info, err)
	}
	before, err = os.ReadFile(exclusions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.DiscoverHomes(home); err != nil {
		t.Fatal(err)
	}
	after, err = os.ReadFile(exclusions)
	later, statErr := os.Lstat(exclusions)
	if err != nil || statErr != nil || !os.SameFile(info, later) || string(before) != string(after) {
		t.Fatal("discovery rewrote metadata")
	}
}

func TestHomeExclusionRejectsUnsafeAndNoLongerEligibleCandidates(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := discoveryHome(t, home, ".codex-2", "config.toml")
	r := Registry{Root: filepath.Join(t.TempDir(), "registry")}
	found, err := r.DiscoverHomes(home)
	if err != nil || len(found.Candidates) != 1 {
		t.Fatalf("discovery %+v %v", found, err)
	}
	candidate := found.Candidates[0]
	if err := r.SetHomeExcluded(HomeCandidate{Provider: "codex", Name: "codex-2", Home: dir}, true); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("fabricated candidate accepted %v", err)
	}
	overlap := Registry{Root: filepath.Join(dir, "cctrace")}
	if err := overlap.SetHomeExcluded(candidate, true); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("provider overlap accepted %v", err)
	}
	if _, err := os.Stat(overlap.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("wrote provider directory")
	}
	if err := os.Remove(filepath.Join(dir, "config.toml")); err != nil {
		t.Fatal(err)
	}
	if err := r.SetHomeExcluded(candidate, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("ineligible candidate accepted %v", err)
	}
}
