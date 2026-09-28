package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The troubleshooting skill lists the config keys a user may set, and users read
// that list instead of the source. It has drifted twice.
//
// The second drift was the expensive one: `options.log_user_prompts` stayed in the
// list after the key stopped existing, so the document advertised a privacy control
// that `config set` would reject -- the same shape as the defect that removed the
// key in the first place (#264). A document naming a protection that is not there is
// worse than silence, because the reader stops looking.
//
// The first drift was quieter and lasted longer: the gjc/omo/codex sync toggles were
// never added, so the only written answer to "how do I turn on gjc collection" was
// absent while the keys existed.
//
// Both directions matter, so this compares the sets rather than checking membership.
const troubleshootDoc = "../../.claude/skills/troubleshoot.md"

// The comparison target is the `unknown key` help text, not a hand-written list here.
// A third list would just be a third thing to drift.
var availableKeysRe = regexp.MustCompile(`Available keys: ([^"]+)"`)

func documentedConfigKeys(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(troubleshootDoc)
	if err != nil {
		t.Fatalf("reading %s: %v", troubleshootDoc, err)
	}
	// Anchored on the ASCII prefix of the heading. The heading itself is Korean, and
	// this file is exported to the mirror, where the gate rejects non-ASCII source.
	_, after, found := strings.Cut(string(raw), "## CLI config")
	if !found {
		t.Fatalf("%s: the config key section is gone -- if it was renamed, rename it here too", troubleshootDoc)
	}
	parts := strings.Split(after, "```")
	if len(parts) < 2 {
		t.Fatalf("%s: no fenced block under the config key section", troubleshootDoc)
	}
	keys := map[string]bool{}
	for _, field := range strings.FieldsFunc(parts[1], func(r rune) bool {
		return r == '/' || r == '\n' || r == ' '
	}) {
		if field = strings.TrimSpace(field); field != "" {
			keys[field] = true
		}
	}
	return keys
}

func implementedConfigKeys(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatalf("reading config.go: %v", err)
	}
	m := availableKeysRe.FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("config.go: the `Available keys:` help text is gone -- this test reads it as the source of truth")
	}
	keys := map[string]bool{}
	for _, k := range strings.Split(m[1], ",") {
		if k = strings.TrimSpace(k); k != "" {
			keys[k] = true
		}
	}
	return keys
}

func TestTroubleshootDocListsEveryConfigKey(t *testing.T) {
	doc := documentedConfigKeys(t)
	code := implementedConfigKeys(t)

	if missing := difference(code, doc); len(missing) > 0 {
		t.Errorf("keys a user can set but the document never mentions: %v\n"+
			"Undocumented settings are settings nobody finds. Add them to %s.", missing, troubleshootDoc)
	}
	if stale := difference(doc, code); len(stale) > 0 {
		t.Errorf("keys the document advertises that `config set` rejects: %v\n"+
			"This is the #264 shape -- the document promises a setting that does not exist. "+
			"Remove them from %s, or restore the key.", stale, troubleshootDoc)
	}
}

func difference(a, b map[string]bool) []string {
	var only []string
	for k := range a {
		if !b[k] {
			only = append(only, k)
		}
	}
	sort.Strings(only)
	return only
}
