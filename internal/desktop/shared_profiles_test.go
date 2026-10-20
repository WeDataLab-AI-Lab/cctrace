package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"cctrace/internal/profile"
	"cctrace/internal/usage"
)

func TestSharedRegistryCLIVisibleAndGUIRegistrationDisabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := profile.DefaultDir()
	p := profile.NewDefault()
	p.User = profile.UserInfo{Name: "Fixture", Email: "fixture@example.invalid", Team: "QA"}
	p.Server.SyncEndpoint = "https://fixture.invalid"
	p.Server.AuthToken = "synthetic-upload"
	p.Server.ReadToken = "synthetic-read"
	p.ClaudeConfigDir = makeQuotaHome(t, "cli-home")
	p.Options.CodexDirs = []string{makeQuotaHome(t, "codex-one"), makeQuotaHome(t, "codex-two")}
	if err := profile.SaveNamed(p, "cli"); err != nil {
		t.Fatal(err)
	}
	svc, err := New(Config{AppSupportRoot: t.TempDir(), ProfileRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	result := call(t, svc, "list", "profiles.list", map[string]any{})
	var listed profileListResult
	if err := decodePayload(result.Result, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Profiles) != 1 || listed.Profiles[0].Name != "cli" {
		t.Fatalf("CLI profile missing from desktop: %s", result.Result)
	}
	guiHome := makeQuotaHome(t, "gui-home")
	marker := filepath.Join(guiHome, "auth.json")
	if err := os.WriteFile(marker, []byte("synthetic-provider-marker"), 0600); err != nil {
		t.Fatal(err)
	}
	added := call(t, svc, "add", "profiles.add", map[string]any{"name": "gui", "claude_home": guiHome, "codex_home": ""})
	if added.Error != nil {
		t.Fatal(added.Error)
	}
	gui, err := profile.LoadNamed("gui")
	if err != nil {
		t.Fatalf("GUI registration invisible to CLI: %v", err)
	}
	if gui.Options.SyncEnabled || gui.Server.AuthToken != "" || gui.Server.ReadToken != "" {
		t.Fatal("GUI registration enabled collection or copied authentication")
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "synthetic-provider-marker" {
		t.Fatal("provider modified")
	}
	cli, err := profile.LoadNamed("cli")
	if err != nil || cli.Server != p.Server || len(cli.Options.CodexDirs) != 2 {
		t.Fatal("existing CLI configuration lost")
	}
}

func TestSharedRegistryEditPreservesCLIConfigAndExtraCodexRoots(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := profile.DefaultDir()
	p := profile.NewDefault()
	p.User = profile.UserInfo{Name: "Fixture", Email: "fixture@example.invalid", Team: "QA"}
	p.Server.SyncEndpoint = "https://fixture.invalid"
	p.Server.AuthToken = "synthetic-upload"
	p.Server.ReadToken = "synthetic-read"
	p.ClaudeConfigDir = makeQuotaHome(t, "first")
	p.CodexHome = makeQuotaHome(t, "codex-execution")
	p.Options.CodexDirs = []string{makeQuotaHome(t, "extra-one"), makeQuotaHome(t, "extra-two")}
	if err := profile.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := profile.SaveNamed(p, "work"); err != nil {
		t.Fatal(err)
	}
	svc, err := New(Config{AppSupportRoot: t.TempDir(), ProfileRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	listed := call(t, svc, "list", "profiles.list", map[string]any{})
	var list struct {
		Profiles []struct{ Name, ID, Revision string }
	}
	if err := json.Unmarshal(listed.Result, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Profiles) != 2 || list.Profiles[0].ID != "default" || list.Profiles[1].ID != "named:work" {
		t.Fatalf("identity: %s", listed.Result)
	}
	home := makeQuotaHome(t, "edited")
	edit := call(t, svc, "edit", "profiles.update", map[string]any{"name": "work", "revision": list.Profiles[1].Revision, "claude_home": home, "codex_home": p.CodexHome})
	if edit.Error != nil {
		t.Fatal(edit.Error)
	}
	stale := call(t, svc, "stale", "profiles.update", map[string]any{"name": "work", "revision": list.Profiles[1].Revision, "claude_home": p.ClaudeConfigDir, "codex_home": ""})
	if stale.Error == nil || stale.Error.Code != "profile_conflict" {
		t.Fatalf("stale edit: %s %+v", stale.Result, stale.Error)
	}
	stored, err := profile.LoadNamed("work")
	if err != nil || stored.ClaudeConfigDir != home || stored.Server != p.Server || !reflect.DeepEqual(stored.Options.CodexDirs, p.Options.CodexDirs) {
		t.Fatal("shared edit lost CLI fields")
	}
	defaultProfile, _ := profile.Load()
	if defaultProfile.ClaudeConfigDir != p.ClaudeConfigDir {
		t.Fatal("named edit mutated default")
	}
	removed := call(t, svc, "remove", "profiles.remove", map[string]any{"name": "work", "revision": storedRevision(t, root, "work"), "confirm": true})
	if removed.Error == nil || removed.Error.Code != "unsafe_storage" {
		t.Fatal("GUI removed configured CLI directory")
	}
}

func storedRevision(t *testing.T, root, name string) string {
	t.Helper()
	entry, err := (profile.Registry{Root: root}).Load(name)
	if err != nil {
		t.Fatal(err)
	}
	return entry.Revision
}

func TestSharedRegistryExternalEditInvalidatesSelectedQuotaIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := profile.DefaultDir()
	p := profile.NewDefault()
	p.ClaudeConfigDir = makeQuotaHome(t, "selected-one")
	if err := profile.SaveNamed(p, "work"); err != nil {
		t.Fatal(err)
	}
	svc, err := New(Config{AppSupportRoot: t.TempDir(), ProfileRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	selected := call(t, svc, "select", "quota.selection.set", map[string]any{"provider": "claude", "profile": "work", "enabled": true})
	if selected.Error != nil {
		t.Fatal(selected.Error)
	}
	old := svc.quotaSelections["claude"].SelectionRef
	generation := svc.quotaSelections["claude"].Generation
	loaded, _ := profile.LoadNamed("work")
	loaded.ClaudeConfigDir = makeQuotaHome(t, "selected-next")
	if err := profile.SaveNamed(loaded, "work"); err != nil {
		t.Fatal(err)
	}
	stale := call(t, svc, "stale-quota", "quota.read", map[string]any{"provider": "claude", "selection_ref": old, "generation": generation})
	if stale.Error == nil || stale.Error.Code != "selection_mismatch" {
		t.Fatalf("changed definition reused identity: %+v %s", stale.Error, stale.Result)
	}
	current := call(t, svc, "selection", "quota.selection.read", map[string]any{})
	if current.Error != nil || svc.quotaSelections["claude"].SelectionRef == old {
		t.Fatal("identity did not rotate")
	}
}

func TestSharedRegistryMigrationConflictsRequireSelection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := profile.DefaultDir()
	support := t.TempDir()
	svc, err := New(Config{AppSupportRoot: support, ProfileRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	cli := profile.NewDefault()
	cli.ClaudeConfigDir = makeQuotaHome(t, "conflict-cli")
	if err := profile.SaveNamed(cli, "work"); err != nil {
		t.Fatal(err)
	}
	gui := profile.NewDefault()
	gui.Options.SyncEnabled = false
	gui.ClaudeConfigDir = makeQuotaHome(t, "conflict-gui")
	legacyPath := filepath.Join(support, "profiles", "work", "profile.json")
	if err := profile.SaveTo(gui, legacyPath); err != nil {
		t.Fatal(err)
	}
	originalCLI, _ := os.ReadFile(filepath.Join(root, "profiles", "work", "profile.json"))
	originalGUI, _ := os.ReadFile(legacyPath)
	listed := call(t, svc, "list", "profiles.list", map[string]any{})
	var list profileListResult
	if err := json.Unmarshal(listed.Result, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Conflicts) != 1 {
		t.Fatalf("conflict missing: %s", listed.Result)
	}
	blocked := call(t, svc, "select", "quota.selection.set", map[string]any{"provider": "claude", "profile": "work", "enabled": true})
	if blocked.Error == nil || blocked.Error.Code != "migration_conflict" {
		t.Fatal("ambiguous home selected")
	}
	c := list.Conflicts[0]
	resolve := call(t, svc, "resolve", "profiles.resolve", map[string]any{"name": "work", "source": "cli", "cli_revision": c.CLIRevision, "desktop_revision": c.DesktopRevision})
	if resolve.Error != nil {
		t.Fatal(resolve.Error)
	}
	currentCLI, _ := os.ReadFile(filepath.Join(root, "profiles", "work", "profile.json"))
	currentGUI, _ := os.ReadFile(legacyPath)
	if !bytes.Equal(originalCLI, currentCLI) || !bytes.Equal(originalGUI, currentGUI) {
		t.Fatal("CLI conflict choice mutated originals")
	}
}

func TestSharedRegistryMutationsRequireNegotiatedCapability(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	svc, err := New(Config{AppSupportRoot: t.TempDir(), ProfileRoot: profile.DefaultDir()})
	if err != nil {
		t.Fatal(err)
	}
	var input bytes.Buffer
	for _, request := range []Request{
		makeRequest(t, "before", "profiles.update", map[string]any{}),
		makeRequest(t, "registry", "hello", map[string]any{"profile_registry": true}),
		makeRequest(t, "after", "profiles.update", map[string]any{}),
	} {
		data, _ := json.Marshal(request)
		input.Write(data)
		input.WriteByte('\n')
	}
	var output bytes.Buffer
	if err := Serve(context.Background(), svc, &input, &output); err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
	var first, after Response
	if err := json.Unmarshal(lines[0], &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(lines[2], &after); err != nil {
		t.Fatal(err)
	}
	if first.Error == nil || first.Error.Code != "capability_not_negotiated" {
		t.Fatalf("mutation before negotiation: %+v", first.Error)
	}
	if after.Error == nil || after.Error.Code != "invalid_payload" {
		t.Fatalf("negotiation did not enable operation: %+v", after.Error)
	}
}

func TestSharedRegistryRemovalPreviewRetainsRuntimeAndProviderData(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := profile.DefaultDir()
	svc, err := New(Config{AppSupportRoot: t.TempDir(), ProfileRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	home := makeQuotaHome(t, "remove-home")
	sentinel := filepath.Join(home, "auth.json")
	if err := os.WriteFile(sentinel, []byte("synthetic-marker"), 0600); err != nil {
		t.Fatal(err)
	}
	added := call(t, svc, "add", "profiles.add", map[string]any{"name": "gui", "claude_home": home, "codex_home": ""})
	if added.Error != nil {
		t.Fatal(added.Error)
	}
	var result profileAddResult
	if err := json.Unmarshal(added.Result, &result); err != nil {
		t.Fatal(err)
	}
	runtimePath := filepath.Join(root, "profiles", "gui", "sync.lock")
	if err := os.WriteFile(runtimePath, []byte("synthetic-runtime"), 0600); err != nil {
		t.Fatal(err)
	}
	preview := call(t, svc, "preview", "profiles.remove.preview", map[string]any{"name": "gui"})
	var shown struct{ Profile profileMapping }
	if err := json.Unmarshal(preview.Result, &shown); err != nil {
		t.Fatal(err)
	}
	if shown.Profile.Removable == nil || *shown.Profile.Removable {
		t.Fatal("preview offered removal with runtime data")
	}
	rejected := call(t, svc, "remove", "profiles.remove", map[string]any{"name": "gui", "revision": result.Profile.Revision, "confirm": true})
	if rejected.Error == nil || rejected.Error.Code != "unsafe_storage" {
		t.Fatal("runtime metadata removed")
	}
	if err := os.Remove(runtimePath); err != nil {
		t.Fatal(err)
	}
	preview = call(t, svc, "preview-safe", "profiles.remove.preview", map[string]any{"name": "gui"})
	if err := json.Unmarshal(preview.Result, &shown); err != nil {
		t.Fatal(err)
	}
	removed := call(t, svc, "remove-safe", "profiles.remove", map[string]any{"name": "gui", "revision": shown.Profile.Revision, "confirm": true})
	if removed.Error != nil {
		t.Fatal(removed.Error)
	}
	after, err := os.ReadFile(sentinel)
	if err != nil || string(after) != "synthetic-marker" {
		t.Fatal("provider data changed")
	}
}

func TestSharedRegistryRejectsLateQuotaAfterExternalCLIEdit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := profile.DefaultDir()
	p := profile.NewDefault()
	p.ClaudeConfigDir = makeQuotaHome(t, "late-first")
	if err := profile.SaveNamed(p, "work"); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	now := time.Now().UTC()
	reader := &quotaSelectionReader{claude: func(ctx context.Context, home string) (*usage.Response, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &usage.Response{FetchedAt: now, FiveHour: &usage.UsageWindow{Utilization: 10, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)}}, nil
	}}
	svc, err := New(Config{AppSupportRoot: t.TempDir(), ProfileRoot: root, ProviderMode: true, ProviderReader: reader, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	selected := call(t, svc, "select", "quota.selection.set", map[string]any{"provider": "claude", "profile": "work", "enabled": true})
	if selected.Error != nil {
		t.Fatal(selected.Error)
	}
	old := svc.quotaSelections["claude"].SelectionRef
	generation := svc.quotaSelections["claude"].Generation
	request := makeRequest(t, "late", "quota.read", map[string]any{"provider": "claude", "selection_ref": old, "generation": generation})
	result := make(chan Response, 1)
	go func() { result <- svc.Handle(context.Background(), request) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider fixture did not start")
	}
	edited, _ := profile.LoadNamed("work")
	edited.ClaudeConfigDir = makeQuotaHome(t, "late-next")
	if err := profile.SaveNamed(edited, "work"); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case response := <-result:
		if response.Error == nil {
			t.Fatal("old home usage was published after CLI edit")
		}
	case <-time.After(time.Second):
		t.Fatal("late provider result did not finish")
	}
	current := call(t, svc, "current", "quota.selection.read", map[string]any{})
	if current.Error != nil || svc.quotaSelections["claude"].SelectionRef == old {
		t.Fatal("external edit did not rotate identity")
	}
}
