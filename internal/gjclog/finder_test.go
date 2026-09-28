package gjclog

import (
	"os"
	"path/filepath"
	"testing"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func TestDefaultGjcDir(t *testing.T) {
	dir := DefaultGjcDir()
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".gjc")
	if dir != want {
		t.Errorf("DefaultGjcDir() = %q, want %q", dir, want)
	}
}

func TestFindSessionFiles(t *testing.T) {
	root := t.TempDir()
	sessDir := filepath.Join(root, "agent", "sessions", "v2-abc123")

	mainFile := filepath.Join(sessDir, "2026-01-01T16-17-22-275Z_019f0000-0000-7000-8000-000000000001.jsonl")
	touch(t, mainFile)

	// subagent transcript nested one level deeper - must not be picked up here
	subFile := filepath.Join(sessDir, "2026-01-01T16-17-22-275Z_019f0000-0000-7000-8000-000000000001", "0-TokenLogProbe.jsonl")
	touch(t, subFile)

	files, err := FindSessionFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 session file, got %d: %v", len(files), files)
	}
	abs, _ := filepath.Abs(mainFile)
	if files[0] != abs {
		t.Errorf("files[0] = %q, want %q", files[0], abs)
	}
}

func TestFindSessionFiles_Empty(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "agent", "sessions"), 0755)

	files, err := FindSessionFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 files, got %d", len(files))
	}
}

func TestFindSubagentFiles(t *testing.T) {
	root := t.TempDir()
	sessDir := filepath.Join(root, "agent", "sessions", "v2-abc123")

	mainFile := filepath.Join(sessDir, "2026-01-01T16-17-22-275Z_019f0000-0000-7000-8000-000000000001.jsonl")
	touch(t, mainFile)

	subFile := filepath.Join(sessDir, "2026-01-01T16-17-22-275Z_019f0000-0000-7000-8000-000000000001", "0-TokenLogProbe.jsonl")
	touch(t, subFile)

	files, err := FindSubagentFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 subagent file, got %d: %v", len(files), files)
	}
	abs, _ := filepath.Abs(subFile)
	if files[0] != abs {
		t.Errorf("files[0] = %q, want %q", files[0], abs)
	}
}

func TestSessionIDFromPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{
			"/home/user/.gjc/agent/sessions/v2-abc/2026-01-01T16-17-22-275Z_019f0000-0000-7000-8000-000000000001.jsonl",
			"019f0000-0000-7000-8000-000000000001",
		},
		{
			"2026-01-01T16-17-22-275Z_019f0000-0000-7000-8000-000000000001.jsonl",
			"019f0000-0000-7000-8000-000000000001",
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
	got := SessionIDFromPath("2026-01-01.jsonl")
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestParentSessionIDFromSubagentPath(t *testing.T) {
	path := "/home/user/.gjc/agent/sessions/v2-abc/2026-01-01T16-17-22-275Z_019f0000-0000-7000-8000-000000000001/0-TokenLogProbe.jsonl"
	want := "019f0000-0000-7000-8000-000000000001"
	got := ParentSessionIDFromSubagentPath(path)
	if got != want {
		t.Errorf("ParentSessionIDFromSubagentPath(%q) = %q, want %q", path, got, want)
	}
}

// The session directory may pair the main session file with either an
// "-artifacts" suffixed directory or a bare "<ts>_<uuid>/" directory (design
// doc section 4.1). Both must resolve to the same parent session id.
func TestParentSessionIDFromSubagentPath_ArtifactsSuffix(t *testing.T) {
	path := "/home/user/.gjc/agent/sessions/v2-abc/2026-01-01T16-17-22-275Z_019f0000-0000-7000-8000-000000000001-artifacts/bash-1.jsonl"
	want := "019f0000-0000-7000-8000-000000000001"
	got := ParentSessionIDFromSubagentPath(path)
	if got != want {
		t.Errorf("ParentSessionIDFromSubagentPath(%q) = %q, want %q", path, got, want)
	}
}

func TestParentSessionIDFromSubagentPath_NoUUID(t *testing.T) {
	got := ParentSessionIDFromSubagentPath("/tmp/notauuid/file.jsonl")
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestTokenLogPath(t *testing.T) {
	got := TokenLogPath("/Users/alice/project", "019f0000-0000-7000-8000-000000000001")
	want := filepath.Join("/Users/alice/project", ".gjc", "_session-019f0000-0000-7000-8000-000000000001", "token-logs", "token-log.jsonl")
	if got != want {
		t.Errorf("TokenLogPath() = %q, want %q", got, want)
	}
}
