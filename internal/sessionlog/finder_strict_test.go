package sessionlog

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// layoutFixture builds a Claude home holding a session file in every layout
// FindJSONLFiles covers, next to entries it must pass over.
func layoutFixture(t *testing.T) string {
	t.Helper()
	claudeDir := t.TempDir()
	for _, rel := range []string{
		"projects/-proj-a/main.jsonl",
		"projects/-proj-a/notes.txt",
		"projects/-proj-a/subagents/agent.jsonl",
		"projects/-proj-a/session-one/subagents/nested.jsonl",
		"projects/-proj-a/session-one/not-collected.jsonl",
		"projects/-proj-a/session-two/tool-results/out.jsonl",
		"projects/-proj-b/other.jsonl",
		"projects/stray.jsonl",
	} {
		p := filepath.Join(claudeDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	return claudeDir
}

func sorted(files []string) []string {
	out := slices.Clone(files)
	slices.Sort(out)
	return out
}

// The strict listing exists to be trusted as complete, so on a tree it can
// read it must return exactly what FindJSONLFiles returns -- no layout missed,
// nothing extra.
func TestFindJSONLFilesStrict_MatchesFindJSONLFiles(t *testing.T) {
	claudeDir := layoutFixture(t)
	want, err := FindJSONLFiles(claudeDir)
	if err != nil {
		t.Fatalf("FindJSONLFiles: %v", err)
	}
	if len(want) != 4 {
		t.Fatalf("fixture lists %d files through FindJSONLFiles, want 4: %v", len(want), want)
	}
	got, err := FindJSONLFilesStrict(claudeDir)
	if err != nil {
		t.Fatalf("FindJSONLFilesStrict: %v", err)
	}
	if !slices.Equal(sorted(got), sorted(want)) {
		t.Fatalf("strict listing = %v\nwant           %v", sorted(got), sorted(want))
	}
}

// A Claude home with no projects directory has no session files, and that is
// an answer, not a failure.
func TestFindJSONLFilesStrict_NoProjectsDirectory(t *testing.T) {
	got, err := FindJSONLFilesStrict(t.TempDir())
	if err != nil || len(got) != 0 {
		t.Fatalf("FindJSONLFilesStrict = %v, %v; want none, nil", got, err)
	}
}

// A symlink that leads nowhere -- to itself, or to a target that is gone --
// cannot be a directory holding session files, whatever it is called. It is
// passed over like any other entry that is not a directory: one stray link
// beside a session file must not make every listing fail.
func TestFindJSONLFilesStrict_PassesOverLinksThatLeadNowhere(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	claudeDir := layoutFixture(t)
	project := filepath.Join(claudeDir, "projects", "-proj-a")
	for name, target := range map[string]string{
		"self.txt":      "self.txt",
		"loop":          "loop",
		"dangling":      "gone",
		"dangling.link": filepath.Join("gone", "deeper"),
	} {
		if err := os.Symlink(target, filepath.Join(project, name)); err != nil {
			t.Fatalf("symlink %s: %v", name, err)
		}
	}
	// And one inside a session directory, where "subagents" is looked for.
	if err := os.Symlink("subagents", filepath.Join(project, "session-two", "subagents")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	want, err := FindJSONLFiles(claudeDir)
	if err != nil {
		t.Fatalf("FindJSONLFiles: %v", err)
	}
	got, err := FindJSONLFilesStrict(claudeDir)
	if err != nil {
		t.Fatalf("FindJSONLFilesStrict: %v", err)
	}
	if len(want) != 4 || !slices.Equal(sorted(got), sorted(want)) {
		t.Fatalf("strict listing = %v\nwant           %v", sorted(got), sorted(want))
	}
}

// A link that cannot be followed for want of permission is not one that leads
// nowhere: what it points at may well be a directory of session files. That
// stays an error.
func TestFindJSONLFilesStrict_ReportsLinkThatCannotBeFollowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions are POSIX")
	}
	if os.Geteuid() == 0 {
		t.Skip("root traverses directories whatever their mode")
	}
	claudeDir := layoutFixture(t)
	locked := filepath.Join(t.TempDir(), "locked")
	if err := os.MkdirAll(filepath.Join(locked, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(filepath.Join(locked, "sessions"), filepath.Join(claudeDir, "projects", "-proj-linked")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	if got, err := FindJSONLFilesStrict(claudeDir); err == nil {
		t.Fatalf("FindJSONLFilesStrict returned %v without an error", got)
	}
}

// FindJSONLFiles leaves out what it cannot read and reports nothing: glob
// ignores directory read errors. The strict listing reports the directory, at
// each depth a session file can sit.
func TestFindJSONLFilesStrict_ReportsUnreadableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions are POSIX")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads directories whatever their mode")
	}
	for _, rel := range []string{
		"projects",
		"projects/-proj-b",
		"projects/-proj-a/subagents",
		"projects/-proj-a/session-one",
		"projects/-proj-a/session-one/subagents",
	} {
		t.Run(rel, func(t *testing.T) {
			claudeDir := layoutFixture(t)
			dir := filepath.Join(claudeDir, filepath.FromSlash(rel))
			if err := os.Chmod(dir, 0); err != nil {
				t.Fatalf("chmod: %v", err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

			loose, err := FindJSONLFiles(claudeDir)
			if err != nil || len(loose) >= 4 {
				t.Fatalf("FindJSONLFiles = %d files, %v; the fixture expects it to drop some silently", len(loose), err)
			}
			if got, err := FindJSONLFilesStrict(claudeDir); err == nil {
				t.Fatalf("FindJSONLFilesStrict returned %v without an error", got)
			}
		})
	}
}
