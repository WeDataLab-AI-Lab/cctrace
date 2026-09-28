package omolog

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultOmoDir(t *testing.T) {
	dir := DefaultOmoDir()
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".omo")
	if dir != want {
		t.Errorf("DefaultOmoDir() = %q, want %q", dir, want)
	}
}

func TestFindSessionFiles(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions", "--Users-example-projects-demo--")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(sessionDir, "2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-000000000101.jsonl"))
	touch(t, filepath.Join(sessionDir, "2026-01-01T00-00-01-000Z_019f0000-0000-7000-8000-000000000102.jsonl"))

	// Artifact subdirectory sibling to a session file - its jsonl must not be
	// picked up since it is not a top-level file inside --*--/.
	artifactDir := filepath.Join(sessionDir, "2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-000000000101-artifacts")
	if err := os.MkdirAll(artifactDir, 0755); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(artifactDir, "nested.jsonl"))

	// Non-matching file.
	touch(t, filepath.Join(sessionDir, "other.txt"))

	files, err := FindSessionFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d: %v", len(files), files)
	}
	for _, f := range files {
		if filepath.Base(f) == "nested.jsonl" {
			t.Errorf("artifact subdirectory jsonl must not be picked up: %v", files)
		}
	}
}

func TestFindSessionFiles_BothRoots(t *testing.T) {
	root := t.TempDir()

	humanDir := filepath.Join(root, "sessions", "--Users-example-projects-demo--")
	if err := os.MkdirAll(humanDir, 0755); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(humanDir, "2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-000000000101.jsonl"))

	agentDir := filepath.Join(root, "agent", "sessions", "--Users-example-projects-demo--")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(agentDir, "2026-01-01T00-00-02-000Z_019f0000-0000-7000-8000-000000000201.jsonl"))

	// Artifact subdirectory under the agent root must also be excluded.
	agentArtifactDir := filepath.Join(agentDir, "2026-01-01T00-00-02-000Z_019f0000-0000-7000-8000-000000000201-artifacts")
	if err := os.MkdirAll(agentArtifactDir, 0755); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(agentArtifactDir, "nested.jsonl"))

	files, err := FindSessionFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files across both roots, got %d: %v", len(files), files)
	}

	var sawHuman, sawAgent bool
	for _, f := range files {
		switch filepath.Base(f) {
		case "2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-000000000101.jsonl":
			sawHuman = true
			if IsAgentSessionPath(f) {
				t.Errorf("%q should not be classified as an agent session path", f)
			}
		case "2026-01-01T00-00-02-000Z_019f0000-0000-7000-8000-000000000201.jsonl":
			sawAgent = true
			if !IsAgentSessionPath(f) {
				t.Errorf("%q should be classified as an agent session path", f)
			}
		case "nested.jsonl":
			t.Errorf("artifact subdirectory jsonl under agent root must not be picked up: %v", files)
		}
	}
	if !sawHuman || !sawAgent {
		t.Fatalf("expected files from both roots, got %v", files)
	}
}

func TestFindSessionFiles_DedupSamePathAcrossCalls(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions", "--Users-example-projects-demo--")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(sessionDir, "2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-000000000101.jsonl"))

	files1, err := FindSessionFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	files2, err := FindSessionFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(files1) != 1 || len(files2) != 1 || files1[0] != files2[0] {
		t.Fatalf("expected identical single-file result across calls, got %v and %v", files1, files2)
	}
}

func TestIsAgentSessionPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{
			"/home/user/.omo/agent/sessions/--Users-example-projects-demo--/2026-01-01T00-00-02-000Z_019f0000-0000-7000-8000-000000000201.jsonl",
			true,
		},
		{
			"/home/user/.omo/sessions/--Users-example-projects-demo--/2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-000000000101.jsonl",
			false,
		},
		{
			"/home/user/.omo/sessions/--Users-example-projects-demo--/2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-000000000101-artifacts/nested.jsonl",
			false,
		},
	}
	for _, c := range cases {
		got := IsAgentSessionPath(c.path)
		if got != c.want {
			t.Errorf("IsAgentSessionPath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

// A malformed omoDir (one containing glob metacharacters, e.g. an unclosed
// "[") makes filepath.Glob return filepath.ErrBadPattern for the pattern
// built from it. FindSessionFiles must surface that error rather than
// swallow it: omoDir is a literal prefix shared by every root's pattern
// (sessions/... and agent/sessions/... both start with it), so a bad omoDir
// fails every root identically - there is no "other root" that could still
// be scanned. Swallowing the error here would silently return zero files,
// indistinguishable from "this user has no omo sessions yet", which is
// exactly the failure mode (collection going missing with nothing to notice)
// this package exists to fix. See internal/omosyncer/syncer.go, which logs
// the error on a non-nil return but only "scan 0 file(s)" on (nil, nil).
//
// This cannot isolate the failure to a single root: filepath.Glob's
// recursive resolution bottoms out at the first fully-literal directory
// prefix, which for both root patterns is the same call, matching the same
// malformed omoDir path segment against the same parent directory's
// entries. Verified empirically: both root patterns return the identical
// filepath.ErrBadPattern for the same malformed omoDir, so this test cannot
// construct "one root globs fine, the other is malformed" from omoDir alone
// - the only caller-controlled input - and does not attempt to.
func TestFindSessionFiles_MalformedOmoDirReturnsError(t *testing.T) {
	root := t.TempDir()
	// "[" is unclosed, which filepath.Match (used internally by Glob) treats
	// as a syntax error - but only once Glob actually calls Match against a
	// directory entry, so the malformed segment needs at least one sibling
	// entry to compare against.
	omoDir := filepath.Join(root, "bad[dir")
	if err := os.MkdirAll(filepath.Join(omoDir, "sessions", "--slug--"), 0755); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(omoDir, "sessions", "--slug--", "2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-000000000301.jsonl"))
	// A sibling directory entry next to "bad[dir" is required so Glob's
	// internal ReadDir has something to run the malformed pattern against.
	if err := os.MkdirAll(filepath.Join(root, "sibling"), 0755); err != nil {
		t.Fatal(err)
	}

	// Confirm the premise still holds on this platform before asserting on
	// FindSessionFiles's handling of it: both root patterns must actually
	// hit filepath.ErrBadPattern given this omoDir.
	for _, r := range []string{"sessions", filepath.Join("agent", "sessions")} {
		pattern := filepath.Join(omoDir, r, "--*--", "*.jsonl")
		if _, err := filepath.Glob(pattern); err == nil {
			t.Fatalf("premise check failed: filepath.Glob(%q) returned no error; this test no longer exercises the bad-pattern branch", pattern)
		}
	}

	files, err := FindSessionFiles(omoDir)
	if err == nil {
		t.Fatal("FindSessionFiles must report a malformed omoDir, got nil error")
	}
	if !errors.Is(err, filepath.ErrBadPattern) {
		t.Errorf("err = %v, want it to be (or wrap) filepath.ErrBadPattern", err)
	}
	if files != nil {
		t.Errorf("expected no files on error, got %v", files)
	}
}

func TestFindSessionFiles_Empty(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "sessions"), 0755)

	files, err := FindSessionFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 files, got %d", len(files))
	}
}

func TestSessionIDFromPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{
			"/home/user/.omo/sessions/--Users-example-projects-demo--/2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-000000000101.jsonl",
			"019f0000-0000-7000-8000-000000000101",
		},
		{
			"2026-01-01T00-00-01-000Z_019f0000-0000-7000-8000-000000000102.jsonl",
			"019f0000-0000-7000-8000-000000000102",
		},
	}
	for _, c := range cases {
		got := SessionIDFromPath(c.path)
		if got != c.want {
			t.Errorf("SessionIDFromPath(%q)\n  got  %q\n  want %q", c.path, got, c.want)
		}
	}
}

func TestSessionIDFromPath_NoUUID(t *testing.T) {
	got := SessionIDFromPath("2026-08-12T03-36-05-127Z.jsonl")
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func touch(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
}
