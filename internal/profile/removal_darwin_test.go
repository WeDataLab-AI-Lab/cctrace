package profile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveKeepsCaseAlias(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	source := filepath.Join(DefaultDir(), "profiles", "safe", "provider")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(DefaultDir(), "profiles", "SAFE", "provider")
	original, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	aliased, err := os.Stat(alias)
	if err != nil || !os.SameFile(original, aliased) {
		t.Skip("fixture filesystem has case-sensitive directory names")
	}
	sentinel := filepath.Join(source, "session.jsonl")
	if err := os.WriteFile(sentinel, []byte("synthetic session"), 0600); err != nil {
		t.Fatal(err)
	}
	p := NewDefault()
	p.ClaudeConfigDir = alias
	if err := SaveNamed(p, "safe"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveNamed("safe"); !errors.Is(err, ErrUnsafe) {
		t.Errorf("physical alias accepted for removal: %v", err)
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "synthetic session" {
		t.Errorf("source changed: %q, %v", got, err)
	}
}
