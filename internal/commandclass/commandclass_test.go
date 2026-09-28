package commandclass

import (
	"os"
	"path/filepath"
	"testing"
)

// build lays out the directories Claude Code actually uses, so the classifier is
// tested against the shapes it will meet rather than a simplified stand-in.
func build(t *testing.T) (claudeDir, projectDir string) {
	t.Helper()
	claudeDir = t.TempDir()
	projectDir = t.TempDir()

	mk := func(parts ...string) {
		t.Helper()
		p := filepath.Join(parts...)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Plugin commands live one level deeper than plugin roots: an index that stops
	// at marketplaces/<mp>/<plugin> misses these and reports them as builtins.
	mk(claudeDir, "plugins", "marketplaces", "example-marketplace", "plugins", "git-tools", "commands", "commit.md")
	mk(claudeDir, "plugins", "marketplaces", "example-marketplace", "plugins", "demo-plugin", "skills", "ralph", "SKILL.md")
	// A plugin that ships a command whose name is also a Claude Code builtin.
	mk(claudeDir, "plugins", "marketplaces", "example-marketplace", "plugins", "demo-plugin", "commands", "compact.md")
	// Versioned cache copies of the same plugin.
	mk(claudeDir, "plugins", "cache", "example-marketplace", "demo-plugin", "4.15.10", "skills", "ralph", "SKILL.md")
	mk(claudeDir, "commands", "scribe.md")
	mk(claudeDir, "skills", "weekly-wrapup", "SKILL.md")
	mk(projectDir, ".claude", "commands", "patch.md")
	mk(projectDir, ".claude", "skills", "deploy", "SKILL.md")
	return claudeDir, projectDir
}

func TestClassifySeparatesSourceFromKind(t *testing.T) {
	claudeDir, projectDir := build(t)
	idx := NewIndex(claudeDir, projectDir)

	cases := []struct {
		name   string
		cmd    string
		source Source
		kind   Kind
	}{
		{"namespaced plugin skill", "demo-plugin:ralph", SourcePlugin, KindSkill},
		{"namespaced plugin command", "git-tools:commit", SourcePlugin, KindCommand},
		{"bare name that a plugin provides", "commit", SourcePlugin, KindCommand},
		{"project command", "patch", SourceProject, KindCommand},
		{"project skill", "deploy", SourceProject, KindSkill},
		{"user command", "scribe", SourceUser, KindCommand},
		{"user skill", "weekly-wrapup", SourceUser, KindSkill},
		{"a builtin nothing else provides", "clear", SourceBuiltin, KindCommand},
		{"unknown name is not called a builtin", "since-uninstalled", SourceUnknown, KindUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := idx.Classify(c.cmd)
			if got.Source != c.source || got.Kind != c.kind {
				t.Errorf("Classify(%q) = %s/%s, want %s/%s", c.cmd, got.Source, got.Kind, c.source, c.kind)
			}
		})
	}
}

// A name that exists as both a builtin and a plugin command cannot be resolved by
// name. Guessing either way is wrong for the other half, so the classifier must say
// it does not know rather than pick. `compact` is a real instance of this.
func TestClassifyRefusesToGuessOnCollision(t *testing.T) {
	claudeDir, projectDir := build(t)
	idx := NewIndex(claudeDir, projectDir)

	got := idx.Classify("compact")
	if got.Source != SourceAmbiguous {
		t.Errorf("Classify(\"compact\") source = %s, want %s", got.Source, SourceAmbiguous)
	}
	// The namespaced form names its origin, so it is not ambiguous.
	if ns := idx.Classify("demo-plugin:compact"); ns.Source != SourcePlugin {
		t.Errorf("namespaced form source = %s, want %s", ns.Source, SourcePlugin)
	}
}

// Plugins get uninstalled and upgraded. A name the machine no longer carries must
// not be reported as a builtin -- that is how 80 real invocations would be
// silently relabelled.
func TestClassifyMarksUnknownNamespacedPluginsAsPlugin(t *testing.T) {
	claudeDir, projectDir := build(t)
	idx := NewIndex(claudeDir, projectDir)

	got := idx.Classify("some-uninstalled-plugin:whatever")
	if got.Source != SourcePlugin {
		t.Errorf("source = %s, want %s (the namespace states the origin)", got.Source, SourcePlugin)
	}
	if got.Kind != KindUnknown {
		t.Errorf("kind = %s, want %s (nothing on disk to read it from)", got.Kind, KindUnknown)
	}
}

func TestClassifyEmpty(t *testing.T) {
	idx := NewIndex(t.TempDir(), t.TempDir())
	if got := idx.Classify(""); got.Source != SourceUnknown || got.Kind != KindUnknown {
		t.Errorf("empty = %s/%s, want unknown/unknown", got.Source, got.Kind)
	}
}

// The plugin tree is the expensive half and does not change during a run, so the
// cache must not rebuild it per session.
func TestCacheReusesIndexPerProject(t *testing.T) {
	claudeDir, projectDir := build(t)
	c := NewCache(claudeDir)

	if got := c.Classify(projectDir, "patch"); got.Source != SourceProject {
		t.Fatalf("first call = %s, want %s", got.Source, SourceProject)
	}
	if got := c.Classify(projectDir, "patch"); got.Source != SourceProject {
		t.Errorf("second call = %s, want same", got.Source)
	}
	if len(c.byProject) != 1 {
		t.Errorf("built %d indexes for one project, want 1", len(c.byProject))
	}
	if got := c.Classify(t.TempDir(), "patch"); got.Source == SourceProject {
		t.Error("a different project must not see this project's commands")
	}
}
