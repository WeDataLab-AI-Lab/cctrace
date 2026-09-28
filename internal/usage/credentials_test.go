package usage

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// The keychain holds one entry per Claude config dir. Reading identity from
// $CLAUDE_CONFIG_DIR/.claude.json while reading the token from the bare entry
// mislabels confidently: a personal-account profile's daemon fetches the org
// account's usage and stamps the personal name on it. The value is not missing,
// it is wrong and plausible.
func TestKeychainServiceForConfigDir(t *testing.T) {
	// Paths are full literals from the mirror-approved set; the home is derived
	// rather than spelled out so no new identifier reaches the export. FromSlash
	// keeps that property while making the literal usable on Windows, where
	// keychainService compares against filepath.Join(home, ".claude"): a POSIX
	// literal never matches there, so the default dir reads as a custom one and a
	// hash suffix appears -- a failure about separators, not about the rule this
	// test pins.
	def := filepath.FromSlash("/home/alice/.claude")
	home := filepath.Dir(def)

	if got := keychainService(def, home); got != baseKeychainService {
		t.Errorf("default dir service = %q, want %q", got, baseKeychainService)
	}

	other := filepath.FromSlash("/home/alice/.claude-work")
	sum := sha256.Sum256([]byte(other))
	want := baseKeychainService + "-" + hex.EncodeToString(sum[:])[:keychainSuffixLen]
	if got := keychainService(other, home); got != want {
		t.Errorf("service for %s = %q, want %q", other, got, want)
	}
}

// The mapping was reverse-engineered against three real keychain entries on the
// development machine and matched all three. The mirror export gate forbids the
// real paths from appearing here, so the same rule — first 8 hex characters of
// sha256 of the absolute config dir path — is pinned with neutral paths and
// precomputed digests instead. What this guards is the derivation itself: an
// implementation that hashed a relative path, a different digest, or a
// different prefix length would miss every one of these.
func TestKeychainSuffixMatchesPrecomputedDigests(t *testing.T) {
	for dir, want := range map[string]string{
		"/home/alice/.claude-work": "Claude Code-credentials-f0fc2950",
		"/home/.claude-2":          "Claude Code-credentials-f3dd44f6",
	} {
		if got := keychainService(dir, filepath.Dir(dir)); got != want {
			t.Errorf("service for %s = %q, want %q", dir, got, want)
		}
	}
}

// An empty token used to pass as success: readFromFile returned "" with a nil
// error whenever claudeAiOauth was absent, so the request went out as
// "Authorization: Bearer " and came back 401. The failure was then recorded as
// "API error" rather than "no token", and the one-minute failure cache kept it
// quietly failing.
//
// This is not hypothetical: ~/.claude/.credentials.json on the development
// machine has exactly one top-level key, mcpOAuth. It is an MCP OAuth file, not
// an Anthropic credential file, and that is what macOS falls back to whenever
// the keychain lookup fails.
func TestReadFromFileRejectsCredentialsWithoutToken(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".credentials.json"), `{"mcpOAuth":{"some":"thing"}}`)

	if _, err := readFromFile(dir); err == nil {
		t.Fatal("readFromFile returned no error for a file with no Anthropic token")
	}
}

// The file fallback used to hardcode ~/.claude/.credentials.json, so every
// profile fell back to the same one regardless of which dir it was polling for.
func TestReadFromFileUsesTheGivenConfigDir(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".credentials.json"),
		`{"claudeAiOauth":{"accessToken":"tok-2","subscriptionType":"max","rateLimitTier":"default_claude_max_20x"}}`)

	creds, err := readFromFile(dir)
	if err != nil {
		t.Fatalf("readFromFile: %v", err)
	}
	if creds.AccessToken != "tok-2" {
		t.Errorf("token = %q, want tok-2", creds.AccessToken)
	}
	// Both fields are already parsed today and then thrown away; the weighted
	// average needs the plan, so stop discarding them.
	if creds.SubscriptionType != "max" {
		t.Errorf("subscription = %q, want max", creds.SubscriptionType)
	}
	if creds.RateLimitTier != "default_claude_max_20x" {
		t.Errorf("tier = %q, want default_claude_max_20x", creds.RateLimitTier)
	}
}

// The commoner shape of the same defect: claudeAiOauth is present and
// well-formed, but accessToken is the empty string and expiresAt is 0 — a
// logged-out profile. Two of the four config dirs on the development machine
// are in exactly this state right now, so this is the ordinary case, not the
// edge one.
//
// subscriptionType and rateLimitTier survive the logout, which is why the token
// is what gets checked rather than the block being present.
func TestParseCredentialsRejectsEmptyAccessToken(t *testing.T) {
	loggedOut := `{"claudeAiOauth":{
	  "accessToken":"", "refreshToken":"", "expiresAt":0,
	  "subscriptionType":"max", "rateLimitTier":"default_claude_max_20x"
	}}`

	if _, err := parseCredentials([]byte(loggedOut), "test"); err == nil {
		t.Fatal("no error for a logged-out profile; the request would go out as \"Bearer \" and be logged as an API error")
	}
}

func TestReadFromFileMissingFileIsAnError(t *testing.T) {
	if _, err := readFromFile(t.TempDir()); err == nil {
		t.Fatal("readFromFile returned no error for a missing file")
	}
}

// A dir with no keychain entry must not borrow the default entry's token.
//
// Borrowing looks helpful — a token is produced instead of an error — but that
// token belongs to a different billing account and nothing downstream can tell.
// The poller would then report the default account's usage under this profile's
// name: not a gap, a plausible lie. This is the exact failure the profile-aware
// lookup exists to remove, so the fallback that would reintroduce it is tested
// against rather than left to reviewer memory.
//
// The keychain itself is not reachable from a test, so the rule is asserted on
// the naming function: a non-default dir must never resolve to the bare service.
func TestNonDefaultDirNeverResolvesToBaseService(t *testing.T) {
	home := filepath.Dir("/home/alice/.claude")
	for _, dir := range []string{
		"/home/alice/.claude-work",
		"/home/.claude-never-seen",
		"/opt/tools/my-cctrace",
	} {
		if got := keychainService(dir, home); got == baseKeychainService {
			t.Errorf("keychainService(%q) = %q, want a dir-specific entry", dir, got)
		}
	}
}

// An empty dir means "the default home", which is the bare entry. Callers that
// never set CLAUDE_CONFIG_DIR pass "" and must keep working unchanged.
func TestEmptyDirIsTheDefaultHome(t *testing.T) {
	home := filepath.Dir("/home/alice/.claude")
	if got := keychainService("", home); got != baseKeychainService {
		t.Errorf("keychainService(\"\") = %q, want %q", got, baseKeychainService)
	}
	if got := resolveDir("", home); got != filepath.Join(home, ".claude") {
		t.Errorf("resolveDir(\"\") = %q, want the default home", got)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
