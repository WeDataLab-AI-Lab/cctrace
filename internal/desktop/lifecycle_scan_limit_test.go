package desktop

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const lifecycleScanLimitTestEntries = 1024

func TestLifecycleScanLimitReadDirRejectsCapPlusOne(t *testing.T) {
	dir := t.TempDir()
	writeLifecycleScanFiles(t, dir, "direct", lifecycleScanLimitTestEntries+1)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	entries, err := lifecycleReadDir(root)
	if err == nil {
		t.Fatalf("lifecycleReadDir accepted %d entries", lifecycleScanLimitTestEntries+1)
	}
	if len(entries) != 0 {
		t.Fatalf("over-limit lifecycleReadDir returned a partial entry list: %d entries", len(entries))
	}
}

func TestLifecycleScanLimitPerDirectoryFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		fill func(t *testing.T, f lifecycleTestFixture)
	}{
		{
			name: "app_support_root",
			fill: func(t *testing.T, f lifecycleTestFixture) {
				// The fixture already has two root entries: profiles and the unknown sentinel.
				writeLifecycleScanFiles(t, f.appSupport, "root-limit", lifecycleScanLimitTestEntries-1)
			},
		},
		{
			name: "profiles_directory",
			fill: func(t *testing.T, f lifecycleTestFixture) {
				// The fixture already has profiles/work.
				for i := 0; i < lifecycleScanLimitTestEntries; i++ {
					name := fmt.Sprintf("extra-%04d", i)
					if err := os.Mkdir(filepath.Join(f.appSupport, "profiles", name), 0o700); err != nil {
						t.Fatalf("create synthetic profile directory %s: %v", name, err)
					}
				}
			},
		},
		{
			name: "profile_child_directory",
			fill: func(t *testing.T, f lifecycleTestFixture) {
				// profile.json is the first child; these entries make the directory cap+1.
				writeLifecycleScanFiles(t, f.profileDir, "child-limit", lifecycleScanLimitTestEntries)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newLifecycleTestFixture(t)
			tc.fill(t, f)
			plan, lifecycleErr := f.svc.ResetPreview()
			assertLifecycleScanLimitFailure(t, plan, lifecycleErr)
		})
	}
}

func TestLifecycleScanLimitTotalPlanBudgetFailsClosed(t *testing.T) {
	f := newLifecycleTestFixture(t)
	writeLifecycleScanFiles(t, f.appSupport, "aggregate-root", 700)
	profilesDir := filepath.Join(f.appSupport, "profiles")
	for i := 0; i < 400; i++ {
		name := fmt.Sprintf("budget-%04d", i)
		if err := os.Mkdir(filepath.Join(profilesDir, name), 0o700); err != nil {
			t.Fatalf("create synthetic profile directory %s: %v", name, err)
		}
	}

	rootEntries, err := os.ReadDir(f.appSupport)
	if err != nil {
		t.Fatal(err)
	}
	profileEntries, err := os.ReadDir(profilesDir)
	if err != nil {
		t.Fatal(err)
	}
	childEntries, err := os.ReadDir(f.profileDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rootEntries) != 702 || len(profileEntries) != 401 || len(childEntries) != 1 {
		t.Fatalf("synthetic entry distribution = root:%d profiles:%d work-child:%d", len(rootEntries), len(profileEntries), len(childEntries))
	}
	if len(rootEntries) > lifecycleScanLimitTestEntries || len(profileEntries) > lifecycleScanLimitTestEntries || len(childEntries) > lifecycleScanLimitTestEntries {
		t.Fatal("synthetic total-budget case unexpectedly exceeds a per-directory limit")
	}
	// The scanned root, profiles directory, and work child total 1,104 entries.
	plan, lifecycleErr := f.svc.ResetPreview()
	assertLifecycleScanLimitFailure(t, plan, lifecycleErr)
}

func TestLifecycleScanLimitAtBoundaryRetainsUnknownEntries(t *testing.T) {
	base := t.TempDir()
	appSupport := filepath.Join(base, "empty app support")
	svc := newTestService(t, appSupport, "")
	writeLifecycleScanFiles(t, appSupport, "boundary", lifecycleScanLimitTestEntries)

	plan, lifecycleErr := svc.ResetPreview()
	if lifecycleErr != nil {
		t.Fatalf("ResetPreview at exactly %d entries: %+v", lifecycleScanLimitTestEntries, lifecycleErr)
	}
	if plan.Digest == "" || len(plan.Changes) != 0 || len(plan.Retained) != lifecycleScanLimitTestEntries {
		t.Fatalf("exact-boundary plan = digest:%q changes:%d retained:%d", plan.Digest, len(plan.Changes), len(plan.Retained))
	}
	for _, retained := range plan.Retained {
		if retained.Disposition != "manual-repair" {
			t.Fatalf("unknown entry disposition = %q for %q", retained.Disposition, retained.Path)
		}
	}
}

func TestLifecycleScanLimitPlanRevalidationDoesNotDelete(t *testing.T) {
	f := newLifecycleTestFixture(t)
	sourceBefore := logLifecycleSourceHashes(t, f, "before-over-limit-revalidation")
	profileBefore := fileDigest(t, f.profileFile)
	unknownBefore := fileDigest(t, f.unknown)
	plan, lifecycleErr := f.svc.ResetPreview()
	if lifecycleErr != nil {
		t.Fatalf("initial ResetPreview: %+v", lifecycleErr)
	}
	writeLifecycleScanFiles(t, f.profileDir, "late-child", lifecycleScanLimitTestEntries)

	result, applyErr := f.svc.ResetApply(plan, true)
	if applyErr == nil || applyErr.Code != "storage_error" {
		t.Fatalf("ResetApply after directory grew past cap = result:%+v error:%+v", result, applyErr)
	}
	if result.Digest != "" || len(result.Removed) != 0 {
		t.Fatalf("over-limit revalidation returned an actionable partial result: %+v", result)
	}
	if got := fileDigest(t, f.profileFile); got != profileBefore {
		t.Fatalf("known profile record changed: before=%s after=%s", profileBefore, got)
	}
	if got := fileDigest(t, f.unknown); got != unknownBefore {
		t.Fatalf("unknown root sentinel changed: before=%s after=%s", unknownBefore, got)
	}
	for i := 0; i < lifecycleScanLimitTestEntries; i++ {
		path := filepath.Join(f.profileDir, fmt.Sprintf("late-child-%05d", i))
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("synthetic late child %d was not preserved: %v", i, err)
		}
	}
	assertLifecycleSourceHashes(t, f, sourceBefore)
}

func TestLifecycleScanLimitProfileRemovePreservesOversizedDirectory(t *testing.T) {
	f := newLifecycleTestFixture(t)
	sourceBefore := logLifecycleSourceHashes(t, f, "before-over-limit-profile-remove")
	profileBefore := fileDigest(t, f.profileFile)
	unknownBefore := fileDigest(t, f.unknown)
	writeLifecycleScanFiles(t, f.profileDir, "remove-child", lifecycleScanLimitTestEntries)

	result := call(t, f.svc, "scan-limit-remove", "profiles.remove", map[string]string{"name": "work"})
	if result.Error == nil || result.Error.Code != "unsafe_storage" {
		t.Fatalf("profiles.remove on oversized directory = %+v", result)
	}
	if got := fileDigest(t, f.profileFile); got != profileBefore {
		t.Fatalf("known profile record changed: before=%s after=%s", profileBefore, got)
	}
	if got := fileDigest(t, f.unknown); got != unknownBefore {
		t.Fatalf("unknown root sentinel changed: before=%s after=%s", unknownBefore, got)
	}
	for i := 0; i < lifecycleScanLimitTestEntries; i++ {
		path := filepath.Join(f.profileDir, fmt.Sprintf("remove-child-%05d", i))
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("synthetic profile child %d was not preserved: %v", i, err)
		}
	}
	assertLifecycleSourceHashes(t, f, sourceBefore)
}

func assertLifecycleScanLimitFailure(t *testing.T, plan LifecyclePlan, lifecycleErr *ErrorObject) {
	t.Helper()
	if lifecycleErr == nil || lifecycleErr.Code != "storage_error" {
		t.Fatalf("over-limit preview error = %+v, want storage_error", lifecycleErr)
	}
	if plan.Digest != "" || len(plan.Changes) != 0 || len(plan.Retained) != 0 {
		t.Fatalf("over-limit preview returned a partial plan: %+v", plan)
	}
}

func writeLifecycleScanFiles(t *testing.T, dir, prefix string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("%s-%05d", prefix, i)
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatalf("create synthetic scan entry %s: %v", name, err)
		}
	}
}
