package claudeauth

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// The default home keeps its config beside the directory, not inside it. Both
// files exist on real machines -- the in-directory one is a leftover from an
// older layout -- and on the machine this was written against the leftover was a
// month stale and named a *different* account. Reading it would stamp records
// with an account the user had already left, which is the mis-attribution this
// package exists to prevent, so there is deliberately no fallback to it.
func TestConfigPathForDefaultHomeIsBesideTheDirectory(t *testing.T) {
	home := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")

	got := configPath(claudeDir, home)
	want := filepath.Join(home, ".claude.json")
	if got != want {
		t.Fatalf("configPath = %q, want %q", got, want)
	}
}

// A CLAUDE_CONFIG_DIR home keeps its config inside the directory. That is the
// whole point of the separate home: a second account with its own config.
func TestConfigPathForAlternateHomeIsInsideTheDirectory(t *testing.T) {
	home := t.TempDir()
	alt := filepath.Join(home, ".claude-2")

	got := configPath(alt, home)
	want := filepath.Join(alt, ".claude.json")
	if got != want {
		t.Fatalf("configPath = %q, want %q", got, want)
	}
}

func TestReadAccountUUIDReturnsAccountUUID(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, filepath.Join(home, ".claude.json"),
		`{"oauthAccount":{"accountUuid":"019db82c-66c5-7160-a6a7-b76dc2dd72c5","emailAddress":"someone@example.com"}}`)

	got, err := readAccountUUID(filepath.Join(home, ".claude"), home)
	if err != nil {
		t.Fatalf("readAccountUUID: %v", err)
	}
	if got != "019db82c-66c5-7160-a6a7-b76dc2dd72c5" {
		t.Fatalf("account uuid = %q", got)
	}
}

// The stale in-directory config must not be consulted even when the correct
// file is missing: an absent account is "unknown", which the caller leaves
// blank, while a stale one is a confident wrong answer.
func TestReadAccountUUIDIgnoresInDirectoryConfig(t *testing.T) {
	home := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	writeConfig(t, filepath.Join(claudeDir, ".claude.json"),
		`{"oauthAccount":{"accountUuid":"stale-account-uuid"}}`)

	got, err := readAccountUUID(claudeDir, home)
	if err != nil {
		t.Fatalf("readAccountUUID: %v", err)
	}
	if got != "" {
		t.Fatalf("account uuid = %q, want empty (must not read the in-directory config)", got)
	}
}

// A machine that never logged in is an ordinary state, not a failure the sync
// pass should log every cycle.
func TestReadAccountUUIDMissingFileIsEmptyNotError(t *testing.T) {
	home := t.TempDir()
	got, err := readAccountUUID(filepath.Join(home, ".claude"), home)
	if err != nil {
		t.Fatalf("readAccountUUID: %v", err)
	}
	if got != "" {
		t.Fatalf("account uuid = %q, want empty", got)
	}
}

func TestReadAccountUUIDMissingFieldIsEmptyNotError(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, filepath.Join(home, ".claude.json"), `{"hasCompletedOnboarding":true}`)

	got, err := readAccountUUID(filepath.Join(home, ".claude"), home)
	if err != nil {
		t.Fatalf("readAccountUUID: %v", err)
	}
	if got != "" {
		t.Fatalf("account uuid = %q, want empty", got)
	}
}

func TestReadAccountUUIDMalformedIsError(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, filepath.Join(home, ".claude.json"), `{"oauthAccount":`)

	if _, err := readAccountUUID(filepath.Join(home, ".claude"), home); err == nil {
		t.Fatal("malformed config returned no error")
	}
}

// The default home has to be recognised through the cosmetic differences a path
// picks up in transit, because on Windows it always arrives with some.
//
// cctrace's own hook writer converts the config dir to a POSIX form before
// baking it into settings.json (internal/envgen/hooks.go), the shell converts it
// back on the way into the process, and what the flag finally holds names the
// right directory in a spelling filepath.Join normalises away but == does not.
// That asymmetry is the whole bug: .credentials.json lives *inside* the dir and
// was reached through Join, so the token kept working and quota snapshots kept
// arriving, while .claude.json lives *beside* it and was reachable only through
// this comparison -- so the account uuid came back empty and the quota history
// was dropped without a log line for every Windows profile, for months.
func TestConfigPathRecognisesDefaultHomeSpelledUncleanly(t *testing.T) {
	home := t.TempDir()
	unclean := filepath.Join(home, ".claude") + string(filepath.Separator)

	got := configPath(unclean, home)
	want := filepath.Join(home, ".claude.json")
	if got != want {
		t.Fatalf("configPath(%q) = %q, want %q", unclean, got, want)
	}
}

// An alternate home spelled uncleanly is still an alternate home. Normalising
// the comparison must not start pulling CLAUDE_CONFIG_DIR profiles onto the
// default account's config.
func TestConfigPathKeepsAlternateHomeInsideWhenSpelledUncleanly(t *testing.T) {
	home := t.TempDir()
	alt := filepath.Join(home, ".claude-2") + string(filepath.Separator)

	got := configPath(alt, home)
	want := filepath.Join(filepath.Clean(alt), ".claude.json")
	if got != want {
		t.Fatalf("configPath(%q) = %q, want %q", alt, got, want)
	}
}

// Windows compares paths case-insensitively and cctrace's hook writer lowercases
// the drive letter on purpose (hooks.go), so the flag can name the default home
// as c:\... while os.UserHomeDir reports C:\.... Case folding is therefore not a
// hardening measure here, it is the second half of the same defect.
//
// caseInsensitive is a parameter rather than a runtime.GOOS read inside sameDir
// so the rule is testable on the machines that actually run this suite, which
// are not Windows. The same reason readAccountUUID takes home.
func TestSameDirFoldsCaseOnlyWhenAsked(t *testing.T) {
	for _, tc := range []struct {
		name            string
		a, b            string
		caseInsensitive bool
		want            bool
	}{
		{"identical", "/x/.claude", "/x/.claude", false, true},
		{"unclean trailing separator", "/x/.claude/", "/x/.claude", false, true},
		{"differing case, folding off", "/X/.Claude", "/x/.claude", false, false},
		{"differing case, folding on", "/X/.Claude", "/x/.claude", true, true},
		{"different directory, folding on", "/x/.claude-2", "/x/.claude", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameDir(tc.a, tc.b, tc.caseInsensitive); got != tc.want {
				t.Fatalf("sameDir(%q, %q, %v) = %v, want %v", tc.a, tc.b, tc.caseInsensitive, got, tc.want)
			}
		})
	}
}
