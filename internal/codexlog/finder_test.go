package codexlog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultCodexDir(t *testing.T) {
	t.Setenv("CODEX_CONFIG_DIR", "")
	dir := DefaultCodexDir()
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".codex")
	if dir != want {
		t.Errorf("DefaultCodexDir() = %q, want %q", dir, want)
	}
}

func TestDefaultCodexDir_EnvOverride(t *testing.T) {
	t.Setenv("CODEX_CONFIG_DIR", "/custom/codex")
	dir := DefaultCodexDir()
	if dir != "/custom/codex" {
		t.Errorf("DefaultCodexDir() = %q, want /custom/codex", dir)
	}
}

func TestFindJSONLFiles(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")

	// root-level file
	os.MkdirAll(sessions, 0755)
	touch(t, filepath.Join(sessions, "rollout-2025-06-20T13-00-00-abc123.jsonl"))

	// date-nested file
	nested := filepath.Join(sessions, "2026", "04", "23")
	os.MkdirAll(nested, 0755)
	touch(t, filepath.Join(nested, "rollout-2026-04-23T11-30-10-def456.jsonl"))

	// non-matching file (should be ignored)
	touch(t, filepath.Join(sessions, "other.txt"))

	files, err := FindJSONLFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Errorf("expected 2 files, got %d: %v", len(files), files)
	}
}

func TestFindJSONLFiles_Empty(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "sessions"), 0755)

	files, err := FindJSONLFiles(root)
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
			"/home/user/.codex/sessions/rollout-2026-04-23T11-30-10-019db82c-66c5-7160-a6a7-b76dc2dd72c5.jsonl",
			"019db82c-66c5-7160-a6a7-b76dc2dd72c5",
		},
		{
			"/home/user/.codex/sessions/2026/04/23/rollout-2026-04-23T11-30-10-abc12345-1234-5678-abcd-ef0123456789.jsonl",
			"abc12345-1234-5678-abcd-ef0123456789",
		},
		{
			"rollout-2025-06-20T13-30-47-f2b25221-cfe0-415e-bbdc-96bc79d812f6.jsonl",
			"f2b25221-cfe0-415e-bbdc-96bc79d812f6",
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
	got := SessionIDFromPath("rollout-2025-06-20.jsonl")
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
