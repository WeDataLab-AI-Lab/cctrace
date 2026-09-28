// Package claudeauth reads the Anthropic account Claude Code records locally.
//
// ~/.claude.json holds a great deal besides the account -- cached experiment
// data, project paths, onboarding state. Only the account uuid ever leaves this
// package: the parsed struct declares nothing else, so no other field is held in
// memory here at all, and callers cannot forward one into sync payloads or
// daemon logs by accident.
package claudeauth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// configFile is the subset of .claude.json this package parses. Everything else
// in the file is deliberately absent.
//
// Each field here has to earn its place, because declaring one is what makes it
// possible for a caller to forward it into a sync payload or a daemon log. The
// three beyond the uuid are all values the server already holds by another
// route -- emailAddress is otel_events.login_email, organizationUuid is
// otel_events.org_id -- so they add a second path to the same datum rather than
// a new exposure. organizationType is what distinguishes a personal plan, where
// accountUuid is the billing unit, from a team one, where it is not.
type configFile struct {
	OAuthAccount struct {
		AccountUUID      string `json:"accountUuid"`
		EmailAddress     string `json:"emailAddress"`
		OrganizationUUID string `json:"organizationUuid"`
		OrganizationType string `json:"organizationType"`
	} `json:"oauthAccount"`
}

// Account is the locally cached identity of the Anthropic account behind one
// Claude home.
//
// The usage API answers with no identity at all -- who is being described is
// implicit in which token asked -- so the reading has to be labelled from here.
type Account struct {
	AccountUUID      string
	EmailAddress     string
	OrganizationUUID string
	OrganizationType string // e.g. "claude_max"
}

// ReadAccount returns the cached account for the Claude home at claudeDir.
//
// Claude Code caches its own profile API response in this file (there is a
// profileFetchedAt beside it), so nothing here calls the network.
//
// A missing file returns a zero Account with no error, for the same reason
// ReadAccountUUID does: a machine that never logged in is an ordinary state.
func ReadAccount(claudeDir string) (Account, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Account{}, fmt.Errorf("resolve home: %w", err)
	}
	cf, err := readConfig(claudeDir, home)
	if err != nil {
		return Account{}, err
	}
	return Account{
		AccountUUID:      cf.OAuthAccount.AccountUUID,
		EmailAddress:     cf.OAuthAccount.EmailAddress,
		OrganizationUUID: cf.OAuthAccount.OrganizationUUID,
		OrganizationType: cf.OAuthAccount.OrganizationType,
	}, nil
}

// ReadAccountUUID returns oauthAccount.accountUuid for the Claude home at
// claudeDir: the account whose usage Anthropic bills, and the value Claude Code
// itself compares to decide that an account switch happened.
//
// A missing file or a missing accountUuid returns an empty string with no error:
// a machine that never logged in is an ordinary state, not a failure.
func ReadAccountUUID(claudeDir string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return readAccountUUID(claudeDir, home)
}

// configPath resolves which .claude.json describes claudeDir.
//
// The default home (~/.claude) keeps its config *beside* the directory, at
// ~/.claude.json. A CLAUDE_CONFIG_DIR home keeps its config inside itself. This
// mirrors how Claude Code itself resolves the file, and the distinction is not
// cosmetic: machines carry a leftover ~/.claude/.claude.json from an older
// layout, and it can name an account the user left long ago.
//
// Which of the two the dir is has to survive the spelling the dir arrives in.
// It reaches this package through a flag that cctrace's own hook writer filled
// in POSIX form and a shell converted back, so on Windows it names the default
// home in a spelling that is not byte-identical to filepath.Join(home,
// ".claude") -- a forward separator, a lowercased drive letter -- and a byte
// comparison read every such machine as a CLAUDE_CONFIG_DIR profile.
//
// Nothing announced that. .credentials.json sits *inside* the dir and is reached
// through filepath.Join, which normalises the difference away, so the token
// resolved and quota snapshots kept arriving; only .claude.json, which sits
// beside the dir and is reachable only through this comparison, went missing.
// The account uuid came back empty, and an empty uuid is dropped silently by
// design (see ReadAccountUUID) because it is an ordinary un-logged-in state --
// which is exactly what made months of missing quota history invisible.
func configPath(claudeDir, home string) string {
	if sameDir(claudeDir, filepath.Join(home, ".claude"), runtime.GOOS == "windows") {
		return filepath.Join(home, ".claude.json")
	}
	return filepath.Join(filepath.Clean(claudeDir), ".claude.json")
}

// sameDir reports whether two paths name the same directory.
//
// caseInsensitive is passed in rather than read from runtime.GOOS here so the
// rule can be tested off Windows, the same reason readAccountUUID takes home.
// It is not a general "be lenient" switch: it is on where the filesystem itself
// is case-insensitive, and folding case where the filesystem does not would
// merge two directories that genuinely differ.
//
// Cleaning is deliberately all the normalising this does. Resolving symlinks
// would touch the filesystem and fail on a path that does not exist, and the
// caller turns a failure here into a silently empty account -- so a defence
// against an unobserved symlink case would be paid for by reintroducing the
// silent loss it is meant to prevent.
func sameDir(a, b string, caseInsensitive bool) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if caseInsensitive {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// readAccountUUID is ReadAccountUUID with the home directory injected, so the
// path rule is testable without touching the real one.
//
// There is deliberately no second candidate to try when the resolved file is
// absent. A missing account is "unknown", which the caller records as blank; the
// leftover in-directory config would instead supply a confident wrong answer,
// and stamping records with a stale account is precisely the mis-attribution
// this exists to prevent.
func readAccountUUID(claudeDir, home string) (string, error) {
	cf, err := readConfig(claudeDir, home)
	if err != nil {
		return "", err
	}
	return cf.OAuthAccount.AccountUUID, nil
}

func readConfig(claudeDir, home string) (configFile, error) {
	path := configPath(claudeDir, home)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return configFile{}, nil
	}
	if err != nil {
		return configFile{}, fmt.Errorf("read %s: %w", path, err)
	}
	var cf configFile
	if err := json.Unmarshal(data, &cf); err != nil {
		// json errors carry an offset and at most one character of input, never
		// a field value, so wrapping is safe.
		return configFile{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return cf, nil
}
