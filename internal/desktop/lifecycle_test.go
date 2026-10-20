package desktop

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	profilepkg "cctrace/internal/profile"
)

type lifecycleTestFixture struct {
	svc         *Service
	appSupport  string
	profileDir  string
	profileFile string
	claudeHome  string
	codexHome   string
	unknown     string
	sourceFiles map[string]string
}

func newLifecycleTestFixture(t *testing.T) lifecycleTestFixture {
	t.Helper()
	base := t.TempDir()
	f := lifecycleTestFixture{
		appSupport: filepath.Join(base, "cctrace App Support 東京"),
		claudeHome: filepath.Join(base, "Claude Home 日本語"),
		codexHome:  filepath.Join(base, "Codex Home 资料"),
	}
	for _, dir := range []string{f.claudeHome, f.codexHome} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f.sourceFiles = map[string]string{
		"claude.credentials": filepath.Join(f.claudeHome, "credentials", "fixture.json"),
		"claude.settings":    filepath.Join(f.claudeHome, "settings.json"),
		"claude.history":     filepath.Join(f.claudeHome, "projects", "session history.jsonl"),
		"codex.credentials":  filepath.Join(f.codexHome, "auth.json"),
		"codex.settings":     filepath.Join(f.codexHome, "config.toml"),
		"codex.history":      filepath.Join(f.codexHome, "sessions", "history.jsonl"),
	}
	for name, path := range f.sourceFiles {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("synthetic-"+name+"-marker\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	claudeAlias := filepath.Join(base, "Claude Alias 링크")
	if err := os.Symlink(f.claudeHome, claudeAlias); err != nil && runtime.GOOS != "windows" {
		t.Fatalf("create synthetic Claude home alias: %v", err)
	}
	if runtime.GOOS == "windows" {
		claudeAlias = f.claudeHome
	}
	f.svc = newTestService(t, f.appSupport, "")
	added := call(t, f.svc, "life-add", "profiles.add", map[string]string{
		"name":        "work",
		"claude_home": claudeAlias,
		"codex_home":  f.codexHome,
	})
	if added.Error != nil {
		t.Fatalf("profiles.add: %+v", added.Error)
	}
	f.profileDir = filepath.Join(f.appSupport, "profiles", "work")
	f.profileFile = filepath.Join(f.profileDir, "profile.json")
	f.unknown = filepath.Join(f.appSupport, "unattributed state.json")
	if err := os.WriteFile(f.unknown, []byte("synthetic unknown app-support data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func logLifecycleSourceHashes(t *testing.T, f lifecycleTestFixture, stage string) map[string]string {
	t.Helper()
	hashes := make(map[string]string, len(f.sourceFiles))
	names := make([]string, 0, len(f.sourceFiles))
	for name := range f.sourceFiles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := f.sourceFiles[name]
		hashes[name] = fileDigest(t, path)
		t.Logf("source_sha256 stage=%s source=%s sha256=%s", stage, name, hashes[name])
	}
	return hashes
}

func assertLifecycleSourceHashes(t *testing.T, f lifecycleTestFixture, before map[string]string) {
	t.Helper()
	names := make([]string, 0, len(f.sourceFiles))
	for name := range f.sourceFiles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := f.sourceFiles[name]
		after := fileDigest(t, path)
		t.Logf("source_sha256 stage=after source=%s sha256=%s", name, after)
		if after != before[name] {
			t.Errorf("source %s changed: before=%s after=%s", name, before[name], after)
		}
	}
}

func lifecycleHasChange(plan LifecyclePlan, path, action string) bool {
	for _, change := range plan.Changes {
		if change.Path == path && change.Action == action {
			return true
		}
	}
	return false
}

func lifecycleHasRetained(plan LifecyclePlan, path, disposition string) bool {
	for _, retained := range plan.Retained {
		if retained.Path == path && retained.Disposition == disposition {
			return true
		}
	}
	return false
}

func TestDesktopLifecycleResetApplyRemovesOwnedRecordAndRetainsSources(t *testing.T) {
	f := newLifecycleTestFixture(t)
	before := logLifecycleSourceHashes(t, f, "before-reset")

	plan, lifecycleErr := f.svc.ResetPreview()
	if lifecycleErr != nil {
		t.Fatalf("ResetPreview: %+v", lifecycleErr)
	}
	if plan.Operation != "reset" || len(plan.Changes) != 2 {
		t.Fatalf("reset preview = operation %q changes %+v", plan.Operation, plan.Changes)
	}
	profilePath := filepath.Join("profiles", "work", "profile.json")
	profileDir := filepath.Join("profiles", "work")
	if !lifecycleHasChange(plan, profilePath, "remove_file") || !lifecycleHasChange(plan, profileDir, "remove_empty_directory") {
		t.Fatalf("reset preview omitted exact owned changes: %+v", plan.Changes)
	}
	if !lifecycleHasRetained(plan, filepath.Join("unattributed state.json"), "manual-repair") {
		t.Fatalf("unattributed app-support entry lacks manual-repair disposition: %+v", plan.Retained)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var planEnvelope map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &planEnvelope); err != nil {
		t.Fatal(err)
	}
	var planDigest string
	if err := json.Unmarshal(planEnvelope["plan_digest"], &planDigest); err != nil || planDigest != plan.Digest {
		t.Fatalf("preview plan_digest = %q, err=%v", planDigest, err)
	}
	if _, ok := planEnvelope["digest"]; ok {
		t.Fatalf("preview used the uncontracted digest key: %s", encoded)
	}
	if strings.Contains(string(encoded), f.claudeHome) || strings.Contains(string(encoded), f.codexHome) {
		t.Fatalf("preview exposed mapped home path: %s", encoded)
	}
	if _, lifecycleErr := f.svc.ResetApply(plan, false); lifecycleErr == nil || lifecycleErr.Code != "confirmation_required" {
		t.Fatalf("unconfirmed reset error = %+v", lifecycleErr)
	}
	result, lifecycleErr := f.svc.ResetApply(plan, true)
	if lifecycleErr != nil {
		t.Fatalf("ResetApply: %+v", lifecycleErr)
	}
	if len(result.Removed) != 2 {
		t.Fatalf("reset removed paths = %v", result.Removed)
	}
	encodedResult, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var resultEnvelope map[string]json.RawMessage
	if err := json.Unmarshal(encodedResult, &resultEnvelope); err != nil {
		t.Fatal(err)
	}
	if _, ok := resultEnvelope["plan_digest"]; !ok {
		t.Fatalf("apply result omitted plan_digest: %s", encodedResult)
	}
	if _, ok := resultEnvelope["digest"]; ok {
		t.Fatalf("apply result used the uncontracted digest key: %s", encodedResult)
	}
	if _, err := os.Lstat(f.profileFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned profile record remains after apply; no-op apply must fail this assertion: %v", err)
	}
	if _, err := os.Lstat(f.profileDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty owned profile directory remains: %v", err)
	}
	assertLifecycleSourceHashes(t, f, before)
	if _, err := os.Stat(f.unknown); err != nil {
		t.Fatalf("unattributed app-support entry was removed: %v", err)
	}
}

func TestDesktopLifecycleApplyReturnsPartialRemovalOnFailure(t *testing.T) {
	f := newLifecycleTestFixture(t)
	before := logLifecycleSourceHashes(t, f, "before-partial-reset")
	plan, lifecycleErr := f.svc.ResetPreview()
	if lifecycleErr != nil {
		t.Fatalf("ResetPreview: %+v", lifecycleErr)
	}
	profilesRoot := filepath.Join(f.appSupport, "profiles")
	if err := os.Chmod(profilesRoot, 0o500); err != nil {
		t.Fatalf("restrict profile root: %v", err)
	}
	defer func() { _ = os.Chmod(profilesRoot, 0o700) }()

	result, lifecycleErr := f.svc.ResetApply(plan, true)
	if lifecycleErr == nil || lifecycleErr.Code != "storage_error" {
		t.Fatalf("ResetApply error = %+v, want storage_error", lifecycleErr)
	}
	profilePath := filepath.Join("profiles", "work", "profile.json")
	if len(result.Removed) != 1 || result.Removed[0] != profilePath {
		t.Fatalf("partial reset result = %+v, want removed path %q", result, profilePath)
	}
	if result.Operation != "reset" || result.Digest != plan.Digest {
		t.Fatalf("partial reset result lost plan identity: %+v", result)
	}
	if _, err := os.Lstat(f.profileFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned profile metadata should have been removed before the directory failure: %v", err)
	}
	if _, err := os.Stat(f.profileDir); err != nil {
		t.Fatalf("failed directory removal should leave its directory visible: %v", err)
	}
	assertLifecycleSourceHashes(t, f, before)
}

func TestDesktopLifecycleUninstallDefaultPreservesMetadata(t *testing.T) {
	f := newLifecycleTestFixture(t)
	before := logLifecycleSourceHashes(t, f, "before-default-uninstall")

	plan, lifecycleErr := f.svc.UninstallPreview(false)
	if lifecycleErr != nil {
		t.Fatalf("UninstallPreview(false): %+v", lifecycleErr)
	}
	if plan.Operation != "uninstall" || plan.EraseAppData || len(plan.Changes) != 0 {
		t.Fatalf("default uninstall preview must preserve app data: %+v", plan)
	}
	if !lifecycleHasRetained(plan, filepath.Join("profiles", "work", "profile.json"), "preserved") {
		t.Fatalf("default uninstall did not name preserved app metadata: %+v", plan.Retained)
	}
	if _, lifecycleErr := f.svc.UninstallApply(plan, true); lifecycleErr != nil {
		t.Fatalf("UninstallApply(default): %+v", lifecycleErr)
	}
	if _, err := os.Stat(f.profileFile); err != nil {
		t.Fatalf("default uninstall removed app metadata: %v", err)
	}
	if _, err := os.Stat(f.unknown); err != nil {
		t.Fatalf("default uninstall removed unknown app-support state: %v", err)
	}
	assertLifecycleSourceHashes(t, f, before)
}

func TestDesktopLifecycleUninstallEraseRequiresCurrentExplicitPlan(t *testing.T) {
	f := newLifecycleTestFixture(t)
	before := logLifecycleSourceHashes(t, f, "before-explicit-uninstall")

	plan, lifecycleErr := f.svc.UninstallPreview(true)
	if lifecycleErr != nil {
		t.Fatalf("UninstallPreview(true): %+v", lifecycleErr)
	}
	if !plan.EraseAppData || len(plan.Changes) != 2 {
		t.Fatalf("explicit uninstall preview = %+v", plan)
	}
	tampered := plan
	tampered.EraseAppData = false
	if _, lifecycleErr := f.svc.UninstallApply(tampered, true); lifecycleErr == nil || lifecycleErr.Code != "invalid_plan" {
		t.Fatalf("erase flag tampering error = %+v", lifecycleErr)
	}
	if err := os.WriteFile(filepath.Join(f.profileDir, "new unknown entry.txt"), []byte("synthetic changed plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, lifecycleErr := f.svc.UninstallApply(plan, true); lifecycleErr == nil || lifecycleErr.Code != "plan_changed" {
		t.Fatalf("changed uninstall plan error = %+v", lifecycleErr)
	}
	if _, err := os.Stat(f.profileFile); err != nil {
		t.Fatalf("changed plan removed profile metadata: %v", err)
	}
	assertLifecycleSourceHashes(t, f, before)

	plan, lifecycleErr = f.svc.UninstallPreview(true)
	if lifecycleErr != nil {
		t.Fatalf("fresh UninstallPreview(true): %+v", lifecycleErr)
	}
	result, lifecycleErr := f.svc.UninstallApply(plan, true)
	if lifecycleErr != nil {
		t.Fatalf("UninstallApply(explicit): %+v", lifecycleErr)
	}
	if len(result.Removed) != 1 {
		t.Fatalf("unknown child should keep its parent directory: removed=%v", result.Removed)
	}
	if _, err := os.Lstat(f.profileFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("explicit erase left owned profile record: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.profileDir, "new unknown entry.txt")); err != nil {
		t.Fatalf("explicit erase removed unknown entry: %v", err)
	}
	assertLifecycleSourceHashes(t, f, before)
	if _, err := os.Stat(f.unknown); err != nil {
		t.Fatalf("unsafe root-wide cleanup removed unattributed data: %v", err)
	}
}

func TestDesktopLifecycleUnknownAndNestedMappedRootsRequireManualRepair(t *testing.T) {
	f := newLifecycleTestFixture(t)
	before := logLifecycleSourceHashes(t, f, "before-unknown-child-reset")
	unknownChild := filepath.Join(f.profileDir, "local state unknown.txt")
	if err := os.WriteFile(unknownChild, []byte("synthetic user-owned local state\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, lifecycleErr := f.svc.ResetPreview()
	if lifecycleErr != nil {
		t.Fatalf("ResetPreview with unknown child: %+v", lifecycleErr)
	}
	if len(plan.Changes) != 1 || !lifecycleHasChange(plan, filepath.Join("profiles", "work", "profile.json"), "remove_file") {
		t.Fatalf("preview included unknown profile child: %+v", plan.Changes)
	}
	if !lifecycleHasRetained(plan, filepath.Join("profiles", "work", "local state unknown.txt"), "manual-repair") {
		t.Fatalf("unknown child lacks manual-repair disposition: %+v", plan.Retained)
	}
	if _, lifecycleErr := f.svc.ResetApply(plan, true); lifecycleErr != nil {
		t.Fatalf("ResetApply with unknown child: %+v", lifecycleErr)
	}
	assertLifecycleSourceHashes(t, f, before)
	if _, err := os.Stat(unknownChild); err != nil {
		t.Fatalf("unknown child was deleted: %v", err)
	}

	// A mapping nested under the explicit app-support root makes ownership ambiguous.
	newFixture := newLifecycleTestFixture(t)
	nestedHomePath := filepath.Join(newFixture.appSupport, "nested mapped home")
	if err := os.MkdirAll(nestedHomePath, 0o700); err != nil {
		t.Fatal(err)
	}
	nestedHome, err := filepath.EvalSymlinks(nestedHomePath)
	if err != nil {
		t.Fatal(err)
	}
	nestedSentinel := filepath.Join(nestedHome, "credential marker.json")
	if err := os.WriteFile(nestedSentinel, []byte("synthetic nested source marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stored, err := profilepkg.LoadFrom(newFixture.profileFile)
	if err != nil {
		t.Fatal(err)
	}
	stored.ClaudeConfigDir = nestedHome
	if err := profilepkg.SaveTo(stored, newFixture.profileFile); err != nil {
		t.Fatal(err)
	}
	saved, err := profilepkg.LoadFrom(newFixture.profileFile)
	if err != nil {
		t.Fatalf("reload synthetic nested home: %v", err)
	}
	if saved.ClaudeConfigDir != nestedHome {
		t.Fatalf("synthetic nested home was not stored: path=%q", saved.ClaudeConfigDir)
	}
	nestedBefore := fileDigest(t, nestedSentinel)
	nestedPlan, lifecycleErr := newFixture.svc.ResetPreview()
	if lifecycleErr != nil {
		t.Fatalf("ResetPreview with nested mapped home: %+v", lifecycleErr)
	}
	if len(nestedPlan.Changes) != 0 || !lifecycleHasRetained(nestedPlan, filepath.Join("profiles", "work", "profile.json"), "manual-repair") {
		t.Fatalf("nested mapped home must fail closed: %+v", nestedPlan)
	}
	if _, lifecycleErr := newFixture.svc.ResetApply(nestedPlan, true); lifecycleErr != nil {
		t.Fatalf("ResetApply for retained nested mapping: %+v", lifecycleErr)
	}
	if fileDigest(t, nestedSentinel) != nestedBefore {
		t.Fatal("nested source sentinel changed")
	}
	if _, err := os.Stat(newFixture.profileFile); err != nil {
		t.Fatalf("ambiguous nested mapping metadata was removed: %v", err)
	}
}

func TestDesktopLifecycleSymlinkedRootFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory symlink creation may require elevated privileges on Windows")
	}
	base := t.TempDir()
	appSupport := filepath.Join(base, "cctrace support")
	svc := newTestService(t, appSupport, "")
	outside := filepath.Join(base, "outside root")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	outsideSentinel := filepath.Join(outside, "do not remove.txt")
	if err := os.WriteFile(outsideSentinel, []byte("synthetic symlink target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	movedRoot := filepath.Join(base, "moved support")
	if err := os.Rename(appSupport, movedRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, appSupport); err != nil {
		t.Fatal(err)
	}
	if _, lifecycleErr := svc.ResetPreview(); lifecycleErr == nil || lifecycleErr.Code != "unsafe_storage" {
		t.Fatalf("symlinked app-support root error = %+v", lifecycleErr)
	}
	if _, err := os.Stat(outsideSentinel); err != nil {
		t.Fatalf("symlink target was touched: %v", err)
	}
}

func TestDesktopLifecycleRejectsUnownedProfileJSON(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, original []byte) []byte
	}{
		{name: "null", mutate: func(t *testing.T, _ []byte) []byte { return []byte("null\n") }},
		{name: "empty object", mutate: func(t *testing.T, _ []byte) []byte { return []byte("{}\n") }},
		{name: "unrelated object", mutate: func(t *testing.T, _ []byte) []byte { return []byte(`{"unrelated":"synthetic value"}`) }},
		{name: "unknown top-level field", mutate: func(t *testing.T, original []byte) []byte {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(original, &fields); err != nil {
				t.Fatal(err)
			}
			fields["synthetic_owner"] = json.RawMessage(`"unattributed"`)
			encoded, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			return encoded
		}},
		{name: "unknown nested field", mutate: func(t *testing.T, original []byte) []byte {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(original, &fields); err != nil {
				t.Fatal(err)
			}
			var options map[string]json.RawMessage
			if err := json.Unmarshal(fields["options"], &options); err != nil {
				t.Fatal(err)
			}
			options["synthetic_shared_setting"] = json.RawMessage(`true`)
			fields["options"], _ = json.Marshal(options)
			encoded, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			return encoded
		}},
		{name: "shared profile credential", mutate: func(t *testing.T, original []byte) []byte {
			var stored profilepkg.Profile
			if err := json.Unmarshal(original, &stored); err != nil {
				t.Fatal(err)
			}
			stored.Server.AuthToken = "synthetic-cli-credential"
			encoded, err := json.Marshal(stored)
			if err != nil {
				t.Fatal(err)
			}
			return encoded
		}},
		{name: "shared user identity", mutate: func(t *testing.T, original []byte) []byte {
			var stored profilepkg.Profile
			if err := json.Unmarshal(original, &stored); err != nil {
				t.Fatal(err)
			}
			stored.User.Email = "synthetic@example.test"
			encoded, err := json.Marshal(stored)
			if err != nil {
				t.Fatal(err)
			}
			return encoded
		}},
		{name: "duplicate shared user identity", mutate: func(t *testing.T, original []byte) []byte {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(original, &fields); err != nil {
				t.Fatal(err)
			}
			neutralUser := fields["user"]
			delete(fields, "user")
			sharedUser, err := json.Marshal(profilepkg.UserInfo{Email: "synthetic@example.test"})
			if err != nil {
				t.Fatal(err)
			}
			rest, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			prefix := []byte{'{', '"', 'u', 's', 'e', 'r', '"', ':'}
			prefix = append(prefix, sharedUser...)
			prefix = append(prefix, ',', '"', 'u', 's', 'e', 'r', '"', ':')
			prefix = append(prefix, neutralUser...)
			prefix = append(prefix, ',')
			return append(prefix, rest[1:]...)
		}},
		{name: "duplicate shared server credential", mutate: func(t *testing.T, original []byte) []byte {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(original, &fields); err != nil {
				t.Fatal(err)
			}
			neutralServer := fields["server"]
			delete(fields, "server")
			sharedServer := []byte{'{', '"', 'p', 'r', 'o', 't', 'o', 'c', 'o', 'l', '"', ':', '"', 'g', 'r', 'p', 'c', '"', ',', '"', 'a', 'u', 't', 'h', '_', 't', 'o', 'k', 'e', 'n', '"', ':', '"'}
			sharedServer = append(sharedServer, []byte("synthetic-cli-credential")...)
			sharedServer = append(sharedServer, '"', '}')
			rest, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			prefix := []byte{'{', '"', 's', 'e', 'r', 'v', 'e', 'r', '"', ':'}
			prefix = append(prefix, sharedServer...)
			prefix = append(prefix, ',', '"', 's', 'e', 'r', 'v', 'e', 'r', '"', ':')
			prefix = append(prefix, neutralServer...)
			prefix = append(prefix, ',')
			return append(prefix, rest[1:]...)
		}},
		{name: "sync enabled", mutate: func(t *testing.T, original []byte) []byte {
			var stored profilepkg.Profile
			if err := json.Unmarshal(original, &stored); err != nil {
				t.Fatal(err)
			}
			stored.Options.SyncEnabled = true
			encoded, err := json.Marshal(stored)
			if err != nil {
				t.Fatal(err)
			}
			return encoded
		}},
		{name: "null sync flag", mutate: func(t *testing.T, original []byte) []byte {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(original, &fields); err != nil {
				t.Fatal(err)
			}
			var options map[string]json.RawMessage
			if err := json.Unmarshal(fields["options"], &options); err != nil {
				t.Fatal(err)
			}
			options["sync_enabled"] = json.RawMessage("null")
			encodedOptions, err := json.Marshal(options)
			if err != nil {
				t.Fatal(err)
			}
			fields["options"] = encodedOptions
			encoded, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			return encoded
		}},
		{name: "null user identity", mutate: func(t *testing.T, original []byte) []byte {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(original, &fields); err != nil {
				t.Fatal(err)
			}
			var user map[string]json.RawMessage
			if err := json.Unmarshal(fields["user"], &user); err != nil {
				t.Fatal(err)
			}
			user["email"] = json.RawMessage("null")
			encodedUser, err := json.Marshal(user)
			if err != nil {
				t.Fatal(err)
			}
			fields["user"] = encodedUser
			encoded, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			return encoded
		}},
		{name: "empty codex home", mutate: func(t *testing.T, original []byte) []byte {
			var stored profilepkg.Profile
			if err := json.Unmarshal(original, &stored); err != nil {
				t.Fatal(err)
			}
			stored.Options.CodexDirs = []string{""}
			encoded, err := json.Marshal(stored)
			if err != nil {
				t.Fatal(err)
			}
			return encoded
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newLifecycleTestFixture(t)
			original, err := os.ReadFile(f.profileFile)
			if err != nil {
				t.Fatal(err)
			}
			unowned := tc.mutate(t, original)
			if err := os.WriteFile(f.profileFile, unowned, 0o600); err != nil {
				t.Fatal(err)
			}
			before := logLifecycleSourceHashes(t, f, "before-unowned-"+tc.name)

			plan, lifecycleErr := f.svc.ResetPreview()
			if lifecycleErr != nil {
				t.Fatalf("ResetPreview: %+v", lifecycleErr)
			}
			profilePath := filepath.Join("profiles", "work", "profile.json")
			if len(plan.Changes) != 0 || !lifecycleHasRetained(plan, profilePath, "manual-repair") {
				t.Fatalf("unowned metadata must be retained for manual repair: changes=%+v retained=%+v", plan.Changes, plan.Retained)
			}
			if _, lifecycleErr := f.svc.ResetApply(plan, true); lifecycleErr != nil {
				t.Fatalf("ResetApply of retained record: %+v", lifecycleErr)
			}
			after, err := os.ReadFile(f.profileFile)
			if err != nil {
				t.Fatalf("retained profile metadata was removed: %v", err)
			}
			if string(after) != string(unowned) {
				t.Fatal("retained profile metadata bytes changed")
			}
			assertLifecycleSourceHashes(t, f, before)
		})
	}
}

func TestDesktopLifecycleOversizedProfileMetadataRequiresManualRepair(t *testing.T) {
	f := newLifecycleTestFixture(t)
	oversized := []byte(strings.Repeat("x", maxLifecycleProfileBytes+1))
	if err := os.WriteFile(f.profileFile, oversized, 0o600); err != nil {
		t.Fatal(err)
	}
	before := logLifecycleSourceHashes(t, f, "before-oversized-profile")

	plan, lifecycleErr := f.svc.ResetPreview()
	if lifecycleErr != nil {
		t.Fatalf("ResetPreview: %+v", lifecycleErr)
	}
	profilePath := filepath.Join("profiles", "work", "profile.json")
	if len(plan.Changes) != 0 || !lifecycleHasRetained(plan, profilePath, "manual-repair") {
		t.Fatalf("oversized profile must be retained for manual repair: changes=%+v retained=%+v", plan.Changes, plan.Retained)
	}
	if _, lifecycleErr := f.svc.ResetApply(plan, true); lifecycleErr != nil {
		t.Fatalf("ResetApply of retained oversized profile: %+v", lifecycleErr)
	}
	after, err := os.ReadFile(f.profileFile)
	if err != nil || string(after) != string(oversized) {
		t.Fatalf("oversized profile bytes changed: read error=%v", err)
	}
	assertLifecycleSourceHashes(t, f, before)
}

func TestDesktopLifecycleResolvesMappedHomeBeforeOwnershipDecision(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory symlink creation may require elevated privileges on Windows")
	}
	f := newLifecycleTestFixture(t)
	nestedHome := filepath.Join(f.appSupport, "nested mapped home")
	if err := os.MkdirAll(nestedHome, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(f.appSupport), "mapped home alias")
	if err := os.Symlink(nestedHome, alias); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(nestedHome, "credential marker.json")
	if err := os.WriteFile(sentinel, []byte("synthetic nested credential marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stored, err := profilepkg.LoadFrom(f.profileFile)
	if err != nil {
		t.Fatal(err)
	}
	stored.ClaudeConfigDir = alias
	if err := profilepkg.SaveTo(stored, f.profileFile); err != nil {
		t.Fatal(err)
	}
	profileBytes, err := os.ReadFile(f.profileFile)
	if err != nil {
		t.Fatal(err)
	}
	sentinelBefore := fileDigest(t, sentinel)
	sourceBefore := logLifecycleSourceHashes(t, f, "before-canonical-home-check")

	plan, lifecycleErr := f.svc.ResetPreview()
	if lifecycleErr != nil {
		t.Fatalf("ResetPreview: %+v", lifecycleErr)
	}
	profilePath := filepath.Join("profiles", "work", "profile.json")
	if len(plan.Changes) != 0 || !lifecycleHasRetained(plan, profilePath, "manual-repair") {
		t.Fatalf("mapped path resolving inside app-support must be retained: changes=%+v retained=%+v", plan.Changes, plan.Retained)
	}
	if _, lifecycleErr := f.svc.ResetApply(plan, true); lifecycleErr != nil {
		t.Fatalf("ResetApply of retained mapping: %+v", lifecycleErr)
	}
	after, err := os.ReadFile(f.profileFile)
	if err != nil || string(after) != string(profileBytes) {
		t.Fatalf("ambiguous mapping metadata changed: read error=%v", err)
	}
	if fileDigest(t, sentinel) != sentinelBefore {
		t.Fatal("nested mapped source sentinel changed")
	}
	assertLifecycleSourceHashes(t, f, sourceBefore)
}
