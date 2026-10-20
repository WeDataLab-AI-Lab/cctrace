package localdaemon

import "testing"

func TestNoLooserRepositoryPrefixes(t *testing.T) {
	tests := []struct {
		name        string
		old, target []string
		want        bool
	}{
		{"both collect everything", nil, nil, true},
		{"old collects everything, target is limited", nil, []string{"host/org/"}, true},
		{"target would collect everything the old list limits", []string{"host/org/"}, nil, false},
		{"target is the same prefix", []string{"host/org/"}, []string{"host/org/"}, true},
		{"target is a repository under the prefix", []string{"host/org/"}, []string{"host/org/repo"}, true},
		{"a target with a slash lies under an old prefix without one", []string{"host/org"}, []string{"host/org/repo/"}, true},
		{"a target with a slash is the old prefix without one, narrowed", []string{"host/org"}, []string{"host/org/"}, true},
		// The collector matches the prefix as written: without its slash the target
		// also takes host/organization/..., which the old prefix did not.
		{"a target that drops the old prefix's slash is wider", []string{"github.com/org/"}, []string{"github.com/org"}, false},
		{"target is wider than the prefix", []string{"host/org/repo"}, []string{"host/org/"}, false},
		{"target is another organization", []string{"host/org/"}, []string{"host/other/"}, false},
		{"a longer name is not under the prefix", []string{"host/org"}, []string{"host/organization/"}, false},
		{"every target prefix must be under some old prefix", []string{"host/org/"}, []string{"host/org/a", "host/other/b"}, false},
		{"each target prefix may use a different old prefix", []string{"host/one/", "host/two/"}, []string{"host/one/a", "host/two/b"}, true},
		{"case is ignored as the collector ignores it", []string{"Host/Org/"}, []string{"host/org/repo"}, true},
		{"surrounding space is ignored", []string{" host/org/ "}, []string{"host/org/repo"}, true},
		{"a blank old list collects nothing and only nothing is no looser", []string{""}, []string{"host/org/"}, false},
		{"a blank target list collects nothing", []string{"host/org/"}, []string{""}, true},
		{"blank entries beside real ones are ignored", []string{"host/org/"}, []string{"", "host/org/repo"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := noLooser(Filters{RepoPrefixes: tc.old}, Filters{RepoPrefixes: tc.target}); got != tc.want {
				t.Fatalf("noLooser(old %q, target %q) = %v, want %v", tc.old, tc.target, got, tc.want)
			}
		})
	}
}

func TestNoLooserRedaction(t *testing.T) {
	tests := []struct {
		name        string
		old, target Filters
		want        bool
	}{
		{"both off", Filters{}, Filters{}, true},
		{"target redacts what the old did not", Filters{}, Filters{RedactPrompts: true, RedactTools: true}, true},
		{"prompts: old redacts, target does not", Filters{RedactPrompts: true}, Filters{}, false},
		{"prompts: both redact", Filters{RedactPrompts: true}, Filters{RedactPrompts: true}, true},
		{"tools: old redacts, target does not", Filters{RedactTools: true}, Filters{}, false},
		{"tools: both redact", Filters{RedactTools: true}, Filters{RedactTools: true}, true},
		{"prompts and tools are separate", Filters{RedactPrompts: true}, Filters{RedactTools: true}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := noLooser(tc.old, tc.target); got != tc.want {
				t.Fatalf("noLooser = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNoLooserExcludedAccounts(t *testing.T) {
	tests := []struct {
		name        string
		old, target []string
		want        bool
	}{
		{"old excludes none", nil, []string{"a1"}, true},
		{"target drops an exclusion", []string{"a1"}, nil, false},
		{"target keeps and adds", []string{"a1"}, []string{"a1", "a2"}, true},
		{"target keeps only some", []string{"a1", "a2"}, []string{"a1"}, false},
		{"a bare id covers a provider entry for it", []string{"codex:a1"}, []string{"a1"}, true},
		{"a provider entry does not cover a bare id", []string{"a1"}, []string{"codex:a1"}, false},
		{"another provider's entry does not cover", []string{"codex:a1"}, []string{"claude:a1"}, false},
		{"the provider name is case-insensitive", []string{"Codex:a1"}, []string{"codex:a1"}, true},
		{"surrounding space is ignored", []string{" a1 "}, []string{"a1"}, true},
		{"blank entries are ignored", []string{" "}, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := noLooser(Filters{ExcludeAccounts: tc.old}, Filters{ExcludeAccounts: tc.target}); got != tc.want {
				t.Fatalf("noLooser(old %q, target %q) = %v, want %v", tc.old, tc.target, got, tc.want)
			}
		})
	}
}

func TestNoLooserNeedsEveryFilter(t *testing.T) {
	old := Filters{RepoPrefixes: []string{"host/org/"}, RedactPrompts: true, ExcludeAccounts: []string{"a1"}}
	ok := Filters{RepoPrefixes: []string{"host/org/repo"}, RedactPrompts: true, RedactTools: true, ExcludeAccounts: []string{"a1", "a2"}}
	if !noLooser(old, ok) {
		t.Fatal("a target that is no looser on every filter qualifies")
	}
	for name, bad := range map[string]Filters{
		"repositories": {RedactPrompts: true, ExcludeAccounts: []string{"a1"}},
		"redaction":    {RepoPrefixes: []string{"host/org/repo"}, ExcludeAccounts: []string{"a1"}},
		"accounts":     {RepoPrefixes: []string{"host/org/repo"}, RedactPrompts: true},
	} {
		if noLooser(old, bad) {
			t.Errorf("a target looser on %s must not qualify", name)
		}
	}
}
