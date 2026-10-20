package profile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSelectionCASStoresNamesOnlyWithPrivateAtomicFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".cctrace", "next-profile.json")
	first, err := UpdateSelection(path, "codex", "work", nil)
	if err != nil {
		t.Fatalf("first selection: %v", err)
	}
	if first.Version != 1 || first.Revision != 1 || first.Codex != "work" || first.Claude != "" {
		t.Fatalf("first selection = %+v", first)
	}
	second, err := UpdateSelection(path, "claude", "default", &first.Revision)
	if err != nil {
		t.Fatalf("second selection: %v", err)
	}
	if second.Revision != 2 || second.Codex != "work" || second.Claude != "default" {
		t.Fatalf("second selection = %+v", second)
	}
	if _, err := UpdateSelection(path, "codex", "work", &first.Revision); !errors.Is(err, ErrSelectionConflict) {
		t.Fatalf("stale CAS error = %v, want ErrSelectionConflict", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"/Users/", "access_token", "refresh_token", "auth_token"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("selection file contains %q: %s", forbidden, data)
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0600 {
			t.Fatalf("mode = %04o, want 0600", got)
		}
	}
	loaded, err := ReadSelection(path)
	if err != nil || loaded.Revision != 2 || loaded.Claude != "default" || loaded.Codex != "work" {
		t.Fatalf("ReadSelection = %+v, %v", loaded, err)
	}
}

func TestSelectionRejectsInvalidAgentNameAndSymlink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "next-profile.json")
	if _, err := UpdateSelection(path, "gjc", "work", nil); err == nil {
		t.Fatal("accepted unsupported agent")
	}
	if _, err := UpdateSelection(path, "codex", "../outside", nil); err == nil {
		t.Fatal("accepted path-like profile name")
	}
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires platform privileges")
	}
	link := filepath.Join(t.TempDir(), "next-profile.json")
	target := filepath.Join(t.TempDir(), "target.json")
	if err := os.WriteFile(target, []byte(`{"version":1,"revision":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateSelection(link, "codex", "work", nil); err == nil {
		t.Fatal("followed symlink selection file")
	}
}

func TestCodexHomeRejectsGroupWritableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory mode bits do not apply on Windows")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0770); err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizeCodexHome(dir); err == nil {
		t.Fatal("accepted a group-writable Codex home")
	}
}

func TestCodexHomeRejectsReplaceableParentDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory mode bits do not apply on Windows")
	}
	parent := filepath.Join(t.TempDir(), "replaceable")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(parent, "codex")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizeCodexHome(home); err == nil {
		t.Fatal("accepted a Codex home beneath a replaceable parent directory")
	}
}
