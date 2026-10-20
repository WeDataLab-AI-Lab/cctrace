package profile

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

func TestRegistryPreservesLegacyCollectionWithoutTeam(t *testing.T) {
	r := Registry{Root: privateRegistryRoot(t)}
	p := NewDefault()
	p.User = UserInfo{ID: "fixture", Email: "fixture@example.test"}
	p.Server.SyncEndpoint = "http://localhost:8080"
	path, err := r.Path("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveTo(p, path); err != nil {
		t.Fatal(err)
	}
	entry, err := r.Load("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if !entry.ConfigurationComplete || entry.CollectionState != "enabled" {
		t.Fatalf("legacy transport config disabled by optional display fields: %+v", entry)
	}
	if entry.Profile.User.Team != "" || entry.Profile.User.Name != "" {
		t.Fatal("registry fabricated optional legacy identity fields")
	}
}

func TestRegistryMappingEditAndStaleRevision(t *testing.T) {
	r := Registry{Root: privateRegistryRoot(t)}
	e, err := r.RegisterMapping("work", "/fixture/claude", "/fixture/codex")
	if err != nil {
		t.Fatal(err)
	}
	if e.ID != "named:work" || e.CollectionState != "registered" || e.ConfigurationComplete || e.Profile.Options.SyncEnabled {
		t.Fatalf("unexpected registration: %+v", e)
	}
	updated, err := r.UpdateMapping("work", e.Revision, "/fixture/claude-2", "/fixture/codex-2")
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != e.ID {
		t.Fatal("identity changed")
	}
	if _, err := r.UpdateMapping("work", e.Revision, "/fixture/stale", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale edit: %v", err)
	}
}

func TestRegistryConcurrentRegistrationSingleWinner(t *testing.T) {
	r := Registry{Root: privateRegistryRoot(t)}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := r.RegisterMapping("work", "/fixture/claude", ""); results <- err }()
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrExists) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("winners=%d", winners)
	}
}

func TestProfileSavePreservesUnknownAndRejectsLostUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.json")
	raw := []byte(`{"version":1,"user":{"id":"fixture","email":"fixture@example.invalid","future_identity":{"x":1}},"server":{"auth_token":"synthetic-upload","read_token":"synthetic-read","future_secret":"synthetic-future"},"options":{"sync_enabled":true,"codex_dirs":["/fixture/a","/fixture/b"],"future_option":{"x":2}},"future_metadata":{"x":3}}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	a, _ := LoadFrom(path)
	b, _ := LoadFrom(path)
	a.ClaudeConfigDir = "/fixture/new"
	if err := SaveTo(a, path); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	for _, key := range []string{"future_identity", "future_secret", "future_option", "future_metadata", "synthetic-upload", "synthetic-read", "/fixture/b"} {
		if !bytes.Contains(after, []byte(key)) {
			t.Fatalf("lost %s", key)
		}
	}
	b.User.Team = "stale"
	if err := SaveTo(b, path); !errors.Is(err, ErrConflict) {
		t.Fatalf("lost update not detected: %v", err)
	}
}

func TestRegistryMigrationConflictPreservesBothOriginals(t *testing.T) {
	r := Registry{Root: privateRegistryRoot(t)}
	legacy := privateRegistryRoot(t)
	cli := NewDefault()
	cli.ClaudeConfigDir = "/fixture/cli"
	cli.Server.AuthToken = "synthetic-upload"
	cliPath := filepath.Join(r.Root, "profiles", "work", "profile.json")
	if err := SaveTo(cli, cliPath); err != nil {
		t.Fatal(err)
	}
	gui := NewDefault()
	gui.Options.SyncEnabled = false
	gui.ClaudeConfigDir = "/fixture/gui"
	legacyPath := filepath.Join(legacy, "profiles", "work", "profile.json")
	if err := SaveTo(gui, legacyPath); err != nil {
		t.Fatal(err)
	}
	beforeCLI, _ := os.ReadFile(cliPath)
	beforeGUI, _ := os.ReadFile(legacyPath)
	conflicts, err := r.MigrateDesktop(legacy)
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("conflicts=%+v err=%v", conflicts, err)
	}
	afterCLI, _ := os.ReadFile(cliPath)
	afterGUI, _ := os.ReadFile(legacyPath)
	if !bytes.Equal(beforeCLI, afterCLI) || !bytes.Equal(beforeGUI, afterGUI) {
		t.Fatal("migration overwrote original")
	}
	if err := r.ResolveMigration(legacy, "work", "cli", conflicts[0].CLIRevision, conflicts[0].DesktopRevision); err != nil {
		t.Fatal(err)
	}
	conflicts, err = r.MigrateDesktop(legacy)
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("resolved conflict remained: %v %v", conflicts, err)
	}
}

func privateRegistryRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRegistryEnabledIncludesEveryConfiguredCollectionMode(t *testing.T) {
	for _, mode := range []string{"claude", "codex", "gjc", "omo", "local"} {
		t.Run(mode, func(t *testing.T) {
			r := Registry{Root: privateRegistryRoot(t)}
			p := NewDefault()
			p.User = UserInfo{Name: "Fixture", Email: "fixture@example.invalid", Team: "QA"}
			p.Server.Endpoint = "https://fixture.invalid"
			p.Options.SyncEnabled = false
			switch mode {
			case "claude":
				p.Options.SyncEnabled = true
			case "codex":
				p.Options.CodexSyncEnabled = true
			case "gjc":
				p.Options.GjcSyncEnabled = true
			case "omo":
				p.Options.OmoSyncEnabled = true
			case "local":
				p.Options.LocalAnalysisEnabled = true
				p.Server.Endpoint = ""
			}
			path, _ := r.Path("work")
			if err := SaveTo(p, path); err != nil {
				t.Fatal(err)
			}
			entries, err := r.Enabled()
			if err != nil || len(entries) != 1 {
				t.Fatalf("active %s disappeared: %v %v", mode, entries, err)
			}
		})
	}
}

func TestRegistryRefusesSymlinkStorageBeforeWriting(t *testing.T) {
	root := privateRegistryRoot(t)
	outside := privateRegistryRoot(t)
	if err := os.Symlink(outside, filepath.Join(root, "profiles")); err != nil {
		t.Skip(err)
	}
	_, err := (Registry{Root: root}).RegisterMapping("escape", "/fixture/home", "")
	if err == nil {
		t.Fatal("symlink storage accepted")
	}
	entries, e := os.ReadDir(outside)
	if e != nil || len(entries) != 0 {
		t.Fatal("unsafe registration wrote outside registry")
	}
}

func TestProfileSourceMatchesRejectsUnsavedAndRawChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.json")
	p := NewDefault()
	if err := SaveTo(p, path); err != nil {
		t.Fatal(err)
	}
	source, _ := os.ReadFile(path)
	if !p.SourceMatches(source) {
		t.Fatal("unchanged source rejected")
	}
	p.User.Team = "edited"
	if p.SourceMatches(source) {
		t.Fatal("unsaved definition change accepted")
	}
	loaded, _ := LoadFrom(path)
	if loaded.SourceMatches(append(source, ' ')) {
		t.Fatal("raw concurrent change accepted")
	}
}

func TestRegistryDisabledMigrationAndRemovalDoNotReimport(t *testing.T) {
	r := Registry{Root: privateRegistryRoot(t)}
	legacy := privateRegistryRoot(t)
	p := NewDefault()
	p.Options.SyncEnabled = false
	p.ClaudeConfigDir = "/fixture/claude"
	p.Options.CodexDirs = []string{"/fixture/codex", "/fixture/extra"}
	path := filepath.Join(legacy, "profiles", "gui", "profile.json")
	if err := SaveTo(p, path); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(path)
	if conflicts, err := r.MigrateDesktop(legacy); err != nil || len(conflicts) != 0 {
		t.Fatal(conflicts, err)
	}
	entry, err := r.Load("gui")
	if err != nil {
		t.Fatal(err)
	}
	if !entry.Profile.RegistrationOnly || entry.Profile.Options.SyncEnabled || entry.Profile.CodexHome != "/fixture/codex" || len(entry.Profile.Options.CodexDirs) != 2 {
		t.Fatal("migration enabled sync or lost homes")
	}
	if err := r.RemoveMapping("gui", entry.Revision); err != nil {
		t.Fatal(err)
	}
	if conflicts, err := r.MigrateDesktop(legacy); err != nil || len(conflicts) != 0 {
		t.Fatal(conflicts, err)
	}
	entries, err := r.List()
	if err != nil || len(entries) != 0 {
		t.Fatal("removed mapping recreated")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(original, after) {
		t.Fatal("legacy original changed")
	}
}

func TestRegistrySeparateProcessesRejectStaleWriter(t *testing.T) {
	root := privateRegistryRoot(t)
	r := Registry{Root: root}
	entry, err := r.RegisterMapping("work", "/fixture/start", "")
	if err != nil {
		t.Fatal(err)
	}
	commands := []*exec.Cmd{}
	for i := 0; i < 2; i++ {
		command := exec.Command(os.Args[0], "-test.run=^TestRegistryProcessCASWorker$")
		command.Env = []string{"CCTRACE_REGISTRY_TEST_ROOT=" + root, "CCTRACE_REGISTRY_TEST_REVISION=" + entry.Revision, "HOME=" + t.TempDir(), "USERPROFILE=" + t.TempDir()}
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, command)
	}
	winners, conflicts := 0, 0
	for _, command := range commands {
		err := command.Wait()
		if err == nil {
			winners++
		} else if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 23 {
			conflicts++
		} else {
			t.Fatalf("writer failed: %v", err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("winners=%d conflicts=%d", winners, conflicts)
	}
}
func TestRegistryProcessCASWorker(t *testing.T) {
	root := os.Getenv("CCTRACE_REGISTRY_TEST_ROOT")
	if root == "" {
		t.Skip("subprocess only")
	}
	_, err := (Registry{Root: root}).UpdateMapping("work", os.Getenv("CCTRACE_REGISTRY_TEST_REVISION"), "/fixture/changed", "")
	if errors.Is(err, ErrConflict) {
		os.Exit(23)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestRegistryMigrationDoesNotUseCLIScanRootAsExecutionHome(t *testing.T) {
	r := Registry{Root: privateRegistryRoot(t)}
	legacy := privateRegistryRoot(t)
	cli := NewDefault()
	cli.ClaudeConfigDir = "/fixture/claude"
	cli.Options.CodexDirs = []string{"/fixture/extra"}
	path, _ := r.Path("work")
	if err := SaveTo(cli, path); err != nil {
		t.Fatal(err)
	}
	gui := NewDefault()
	gui.Options.SyncEnabled = false
	gui.ClaudeConfigDir = cli.ClaudeConfigDir
	gui.Options.CodexDirs = []string{"/fixture/extra"}
	if err := SaveTo(gui, filepath.Join(legacy, "profiles", "work", "profile.json")); err != nil {
		t.Fatal(err)
	}
	conflicts, err := r.MigrateDesktop(legacy)
	if err != nil || len(conflicts) != 1 {
		t.Fatal("CLI additional scan folder incorrectly treated as execution home", conflicts, err)
	}
}
