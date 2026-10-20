package profile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRemovePreservesSourceAlias(t *testing.T) {
	for _, provider := range []string{"claude", "codex", "extra-codex", "gjc", "omo"} {
		t.Run(provider, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			dir, err := NamedDir("safe")
			if err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(dir, "provider")
			if err := os.MkdirAll(source, 0700); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(source, "session.jsonl")
			if err := os.WriteFile(sentinel, []byte("synthetic session"), 0600); err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(home, "provider-alias")
			if err := os.Symlink(source, alias); err != nil {
				t.Fatal(err)
			}
			p := NewDefault()
			switch provider {
			case "claude":
				p.ClaudeConfigDir = alias
			case "codex":
				p.CodexHome = alias
			case "extra-codex":
				p.Options.CodexDirs = []string{alias}
			case "gjc":
				p.Options.GjcDirs = []string{alias}
			case "omo":
				p.Options.OmoDirs = []string{alias}
			}
			if err := SaveNamed(p, "safe"); err != nil {
				t.Fatal(err)
			}
			if err := RemoveNamed("safe"); !errors.Is(err, ErrUnsafe) {
				t.Errorf("source deletion was not refused: %v", err)
			}
			if got, err := os.ReadFile(sentinel); err != nil || string(got) != "synthetic session" {
				t.Errorf("source data changed: %q, %v", got, err)
			}
		})
	}
}

func TestRemoveRejectsUnknownSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	p := NewDefault()
	p.ClaudeConfigDir = filepath.Join(home, "provider-loop")
	if err := os.Symlink(p.ClaudeConfigDir, p.ClaudeConfigDir); err != nil {
		t.Fatal(err)
	}
	if err := SaveNamed(p, "safe"); err != nil {
		t.Fatal(err)
	}
	path, _ := NamedPath("safe")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := RemoveNamed("safe"); !errors.Is(err, ErrUnsafe) {
		t.Errorf("unresolvable source accepted for removal: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(before) {
		t.Errorf("profile changed after rejected removal: %v", err)
	}
}

func TestRemoveKeepsInvalidMetadata(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := SaveNamed(NewDefault(), "safe"); err != nil {
		t.Fatal(err)
	}
	path, _ := NamedPath("safe")
	if err := os.WriteFile(path, []byte("{invalid profile"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveNamed("safe"); !errors.Is(err, ErrUnsafe) {
		t.Errorf("unknown provider metadata accepted for removal: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "{invalid profile" {
		t.Errorf("invalid metadata changed: %q, %v", got, err)
	}
}
