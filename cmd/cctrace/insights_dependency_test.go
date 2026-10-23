package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runInsights(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := insightsCmd()
	var out strings.Builder
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&strings.Builder{})
	err := cmd.Execute()
	return out.String(), err
}

func TestInsightsDependencyRequiresAnAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := runInsights(t, "dependency"); err == nil || !strings.Contains(err.Error(), "agent") {
		t.Fatalf("missing --agent: err = %v", err)
	}
	if _, err := runInsights(t, "dependency", "--agent", "gemini"); err == nil || !strings.Contains(err.Error(), "--agent") {
		t.Fatalf("unknown --agent: err = %v", err)
	}
}

// The command reads the local homes the local API reads and prints indented
// JSON; without --json it is the same document, pretty-printed.
func TestInsightsDependencyReadsLocalHomes(t *testing.T) {
	home := t.TempDir()
	claude := filepath.Join(home, ".claude")
	dir := filepath.Join(claude, "projects", "-work-alpha")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(`{"type":"user","uuid":"u1","timestamp":"2099-01-01T00:00:00Z","sessionId":"s","promptSource":"typed","message":{"role":"user","content":"hello"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	t.Setenv("CODEX_CONFIG_DIR", filepath.Join(home, "no-codex"))
	out, err := runInsights(t, "dependency", "--agent", "claude", "--since", "30d")
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Window struct {
			Agent string `json:"agent"`
		} `json:"window"`
		Coverage struct {
			Homes []struct {
				Agent string `json:"agent"`
				Files int    `json:"files"`
			} `json:"homes"`
		} `json:"coverage"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || !strings.Contains(out, "\n  \"window\"") {
		t.Fatalf("output is not indented JSON: %v\n%s", err, out)
	}
	if res.Window.Agent != "claude" || len(res.Coverage.Homes) != 1 || res.Coverage.Homes[0].Files != 1 {
		t.Fatalf("output = %s", out)
	}
}
