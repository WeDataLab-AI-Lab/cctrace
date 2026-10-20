package desktop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHomeOverlapIPCRejectsPhysicalCaseAliases(t *testing.T) {
	for _, kind := range []string{"same", "descendant", "ancestor"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			container := filepath.Join(base, "Mixed Case Container")
			app := filepath.Join(container, "App Support Case")
			service, err := New(Config{AppSupportRoot: app})
			if err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(base, "mixed case container", "app support case")
			original, err := os.Stat(app)
			if err != nil {
				t.Fatal(err)
			}
			aliased, err := os.Stat(alias)
			if os.IsNotExist(err) {
				t.Skip("case-sensitive filesystem: alias witness unavailable")
			}
			if err != nil || !os.SameFile(original, aliased) {
				t.Fatal("alias must identify the same physical directory")
			}
			home := alias
			switch kind {
			case "descendant":
				if err := os.Mkdir(filepath.Join(app, "Mapped Home"), 0700); err != nil {
					t.Fatal(err)
				}
				home = filepath.Join(alias, "Mapped Home")
			case "ancestor":
				home = filepath.Dir(alias)
			}
			sentinel := filepath.Join(home, "credentials-sentinel.json")
			data := []byte("synthetic source: never delete or rewrite")
			if err := os.WriteFile(sentinel, data, 0600); err != nil {
				t.Fatal(err)
			}
			before := sha256.Sum256(data)
			request, err := json.Marshal(map[string]any{"version": 1, "kind": "request", "id": "case-overlap", "op": "profiles.add", "payload": map[string]any{"name": "case-alias", "claude_home": home, "codex_home": ""}})
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := Serve(context.Background(), service, bytes.NewReader(append(request, '\n')), &output); err != nil {
				t.Fatal(err)
			}
			var response struct {
				ID    string       `json:"id"`
				Error *ErrorObject `json:"error"`
			}
			if err := json.Unmarshal(output.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.ID != "case-overlap" || response.Error == nil || response.Error.Code != "invalid_home" {
				t.Errorf("physical overlap accepted by JSONL: %s", output.Bytes())
			}
			if _, err := os.Lstat(filepath.Join(app, "profiles", "case-alias")); !os.IsNotExist(err) {
				t.Error("rejected mapping created profile metadata")
			}
			if _, err := New(Config{AppSupportRoot: app, ProviderMode: true, CodexHome: home}); err == nil {
				t.Error("provider-mode launch accepted physical overlap")
			}
			after, err := os.ReadFile(sentinel)
			if err != nil || sha256.Sum256(after) != before {
				t.Fatal("source sentinel changed")
			}
			t.Logf("source_sha256 before=%x after=%x", before, sha256.Sum256(after))
		})
	}
}

func TestHomeOverlapPreservesDisjointCaseSensitiveDirectories(t *testing.T) {
	base := t.TempDir()
	a, b := filepath.Join(base, "Distinct Home"), filepath.Join(base, "distinct home")
	for _, path := range []string{a, b} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	ai, err := os.Stat(a)
	if err != nil {
		t.Fatal(err)
	}
	bi, err := os.Stat(b)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(ai, bi) {
		t.Skip("case-insensitive filesystem: distinct-case witness unavailable")
	}
	if strings.EqualFold(a, b) && pathsOverlap(a, b) {
		t.Fatal("distinct physical directories treated as overlapping")
	}
}
