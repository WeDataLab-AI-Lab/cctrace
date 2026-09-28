package codexlog

import (
	"os"
	"path/filepath"
	"testing"
)

// existingDir creates a temp directory and returns its absolute path.
func existingDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func TestResolveScanDirs_BaseFromConfigDir(t *testing.T) {
	base := existingDir(t)
	t.Setenv("CODEX_CONFIG_DIR", base)
	t.Setenv("CODEX_HOME", "")

	got := ResolveScanDirs(nil)
	if len(got) != 1 || got[0] != base {
		t.Fatalf("ResolveScanDirs(nil) = %v, want [%s]", got, base)
	}
}

// Regression lock: CODEX_HOME must be added on top of the default home, never
// replace it. Replacing it would silently stop collecting the default home.
func TestResolveScanDirs_CodexHomeDoesNotReplaceBase(t *testing.T) {
	base := existingDir(t)
	other := existingDir(t)
	t.Setenv("CODEX_CONFIG_DIR", base)
	t.Setenv("CODEX_HOME", other)

	got := ResolveScanDirs(nil)
	want := []string{base, other}
	if len(got) != len(want) {
		t.Fatalf("ResolveScanDirs(nil) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ResolveScanDirs(nil) = %v, want %v", got, want)
		}
	}
}

func TestResolveScanDirs_ExtraUnionKeepsOrder(t *testing.T) {
	base := existingDir(t)
	home := existingDir(t)
	extra1 := existingDir(t)
	extra2 := existingDir(t)
	t.Setenv("CODEX_CONFIG_DIR", base)
	t.Setenv("CODEX_HOME", home)

	got := ResolveScanDirs([]string{extra1, extra2})
	want := []string{base, home, extra1, extra2}
	if len(got) != len(want) {
		t.Fatalf("ResolveScanDirs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ResolveScanDirs = %v, want %v", got, want)
		}
	}
}

func TestResolveScanDirs_DeduplicatesRepeatedPaths(t *testing.T) {
	base := existingDir(t)
	t.Setenv("CODEX_CONFIG_DIR", base)
	t.Setenv("CODEX_HOME", base)

	got := ResolveScanDirs([]string{base, base})
	if len(got) != 1 || got[0] != base {
		t.Fatalf("ResolveScanDirs = %v, want [%s]", got, base)
	}
}

func TestResolveScanDirs_SkipsMissingBlankAndNonDir(t *testing.T) {
	base := existingDir(t)
	t.Setenv("CODEX_CONFIG_DIR", base)
	t.Setenv("CODEX_HOME", "")

	missing := filepath.Join(base, "does-not-exist")
	regular := filepath.Join(base, "a-file")
	touch(t, regular)

	got := ResolveScanDirs([]string{"", "   ", missing, regular})
	if len(got) != 1 || got[0] != base {
		t.Fatalf("ResolveScanDirs = %v, want [%s]", got, base)
	}
}

func TestResolveScanDirs_EmptyExtraStillScansBase(t *testing.T) {
	base := existingDir(t)
	t.Setenv("CODEX_CONFIG_DIR", base)
	t.Setenv("CODEX_HOME", "")

	got := ResolveScanDirs([]string{})
	if len(got) != 1 || got[0] != base {
		t.Fatalf("ResolveScanDirs([]) = %v, want [%s]", got, base)
	}
}

func TestResolveScanDirs_NoExistingDirs(t *testing.T) {
	root := existingDir(t)
	t.Setenv("CODEX_CONFIG_DIR", filepath.Join(root, "nope"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "nope-2"))

	if got := ResolveScanDirs([]string{filepath.Join(root, "nope-3")}); len(got) != 0 {
		t.Fatalf("ResolveScanDirs = %v, want empty", got)
	}
}

// Regression lock: a "~/..." entry must be expanded, not treated as a literal
// relative directory named "~" that never exists — that dropped the home
// silently, the exact failure this scan-dir union exists to prevent.
func TestResolveScanDirs_ExpandsTildeExtra(t *testing.T) {
	base := existingDir(t)
	home := existingDir(t)
	t.Setenv("CODEX_CONFIG_DIR", base)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	extra := filepath.Join(home, "xhome")
	if err := os.MkdirAll(extra, 0o755); err != nil {
		t.Fatal(err)
	}

	got := ResolveScanDirs([]string{"~/xhome"})
	want := []string{base, extra}
	if len(got) != len(want) {
		t.Fatalf("ResolveScanDirs(~/xhome) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ResolveScanDirs(~/xhome) = %v, want %v", got, want)
		}
	}
}

func TestExpandHome(t *testing.T) {
	home := existingDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	cases := []struct {
		in   string
		want string
	}{
		{"~", home},
		{"~/xhome", filepath.Join(home, "xhome")},
		{"/abs/path", "/abs/path"},
		{"", ""},
		{"~notahome/x", "~notahome/x"},
		{"a/~/b", "a/~/b"},
	}
	for _, c := range cases {
		if got := ExpandHome(c.in); got != c.want {
			t.Errorf("ExpandHome(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFindJSONLFiles_AcrossResolvedDirs(t *testing.T) {
	base := existingDir(t)
	home := existingDir(t)
	t.Setenv("CODEX_CONFIG_DIR", base)
	t.Setenv("CODEX_HOME", home)

	mkRollout := func(root, name string) string {
		sessions := filepath.Join(root, "sessions")
		if err := os.MkdirAll(sessions, 0755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(sessions, name)
		touch(t, p)
		return p
	}
	want1 := mkRollout(base, "rollout-2026-04-23T11-30-10-019db82c-66c5-7160-a6a7-b76dc2dd72c5.jsonl")
	want2 := mkRollout(home, "rollout-2026-04-23T12-00-00-019db82c-66c5-7160-a6a7-b76dc2dd72c6.jsonl")

	var all []string
	for _, dir := range ResolveScanDirs(nil) {
		files, err := FindJSONLFiles(dir)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, files...)
	}

	found := map[string]bool{}
	for _, f := range all {
		found[f] = true
	}
	if !found[want1] || !found[want2] {
		t.Fatalf("collected %v, want both %s and %s", all, want1, want2)
	}
}
