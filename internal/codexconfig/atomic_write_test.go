package codexconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteConfigFileAtomicReplacesContentWithoutTempLeak(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("model = \"old\"\n"), 0644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	if err := writeConfigFileAtomic(path, []byte("model = \"new\"\n"), 0644); err != nil {
		t.Fatalf("writeConfigFileAtomic: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(data) != "model = \"new\"\n" {
		t.Fatalf("config content = %q", data)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") || strings.HasSuffix(entry.Name(), ".bak") {
			t.Fatalf("temporary file leaked: %s", entry.Name())
		}
	}
}

func TestWriteConfigFileAtomicFailsWhenDestinationIsDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatalf("mkdir destination: %v", err)
	}

	if err := writeConfigFileAtomic(path, []byte("model = \"new\"\n"), 0644); err == nil {
		t.Fatal("expected rename over directory to fail")
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("destination directory was not preserved, info=%v err=%v", info, err)
	}
}
