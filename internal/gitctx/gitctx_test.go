package gitctx

import (
	"strings"
	"testing"
)

func TestNormalizeRemoteURL(t *testing.T) {
	tests := map[string]string{
		"git@github.com:org/repo.git":                  "github.com/org/repo",
		"https://github.com/org/repo.git":              "github.com/org/repo",
		"https://github.com/org/repo":                  "github.com/org/repo",
		"https://token@github.com/org/repo.git":        "github.com/org/repo",
		"ssh://git@github.com/org/repo.git":            "github.com/org/repo",
		"ssh://git@git.company.test:2222/org/repo.git": "git.company.test:2222/org/repo",
		"": "",
	}

	for raw, want := range tests {
		if got := NormalizeRemoteURL(raw); got != want {
			t.Fatalf("NormalizeRemoteURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestSanitizeRemoteURL(t *testing.T) {
	tests := map[string]string{
		"https://user:token123@github.com/org/repo.git": "https://github.com/org/repo.git",
		"https://token@github.com/org/repo.git":         "https://github.com/org/repo.git",
		"https://github.com/org/repo.git":               "https://github.com/org/repo.git",
		"ssh://git@github.com/org/repo.git":             "ssh://github.com/org/repo.git",
		"git@github.com:org/repo.git":                   "git@github.com:org/repo.git",
		"git@github-work:org/repo.git":                  "git@github-work:org/repo.git",
		"":                                              "",
	}
	for raw, want := range tests {
		if got := SanitizeRemoteURL(raw); got != want {
			t.Fatalf("SanitizeRemoteURL(%q) = %q, want %q", raw, got, want)
		}
		// Sanitized output must never retain a token-looking secret.
		if strings.Contains(SanitizeRemoteURL(raw), "ghp_") {
			t.Fatalf("SanitizeRemoteURL(%q) leaked credential", raw)
		}
	}
}

func TestLocalFallbackID(t *testing.T) {
	got := LocalFallbackID("cctrace", "/Users/alice/cctrace")
	if got == "" {
		t.Fatal("LocalFallbackID returned empty id")
	}
	if got[:14] != "local:cctrace:" {
		t.Fatalf("LocalFallbackID prefix = %q, want local:cctrace:", got)
	}
}

func TestRepositoryNameFromID(t *testing.T) {
	tests := map[string]string{
		"github.com/ExampleOrg/cctrace": "cctrace",
		"local:other-repo:abcdef":       "other-repo",
		"local:abcdef":                  "", // anonymous hash-only id has no name
		"":                              "",
	}
	for id, want := range tests {
		if got := RepositoryNameFromID(id); got != want {
			t.Fatalf("RepositoryNameFromID(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestAllowsRepository(t *testing.T) {
	exampleOrg := []string{"github.com/ExampleOrg/"}

	tests := []struct {
		name     string
		id       string
		prefixes []string
		want     bool
	}{
		{"empty allowlist collects everything", "github.com/someuser/other", nil, true},
		{"matching org allowed", "github.com/ExampleOrg/cctrace", exampleOrg, true},
		{"case-insensitive match", "github.com/exampleorg/cctrace", exampleOrg, true},
		{"personal repo rejected", "github.com/someuser/other", exampleOrg, false},
		{"self-hosted repo rejected", "git.vhost/svc/git_repo/work_env", exampleOrg, false},
		{"local repo rejected", "local:gai:abcd1234", exampleOrg, false},
		{"blank id rejected when allowlist set", "", exampleOrg, false},
		{"multiple prefixes, second matches", "git.vhost/svc/git_repo/work_env",
			[]string{"github.com/ExampleOrg/", "git.vhost/"}, true},
		{"blank prefixes ignored", "github.com/someuser/other", []string{"", "  "}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := AllowsRepository(tc.id, tc.prefixes); got != tc.want {
				t.Fatalf("AllowsRepository(%q, %v) = %v, want %v", tc.id, tc.prefixes, got, tc.want)
			}
		})
	}
}

func TestAnonymousLocalID(t *testing.T) {
	got := anonymousLocalID("/Users/alice/some-private-project")
	if got == "" {
		t.Fatal("anonymousLocalID returned empty id")
	}
	if got[:6] != "local:" {
		t.Fatalf("anonymousLocalID prefix = %q, want local:", got[:6])
	}
	// Must NOT contain any plaintext from input path.
	for _, leaked := range []string{"alice", "some", "private", "project", "Users"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("anonymousLocalID leaked %q in %q", leaked, got)
		}
	}
}
