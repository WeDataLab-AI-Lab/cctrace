package usage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	// baseKeychainService is the keychain entry for the default Claude home.
	// Any other config dir appends a suffix — see keychainService.
	baseKeychainService = "Claude Code-credentials"

	// keychainSuffixLen is how much of the path digest Claude Code keeps.
	keychainSuffixLen = 8

	// tokenFingerprintLen is how much of the token digest is kept. It only has
	// to distinguish one account's credentials from another's on one machine,
	// and a shorter value is a smaller thing to leak if one ever escapes.
	tokenFingerprintLen = 12
)

type claudeCredentials struct {
	ClaudeAiOauth struct {
		AccessToken      string `json:"accessToken"`
		SubscriptionType string `json:"subscriptionType"`
		RateLimitTier    string `json:"rateLimitTier"`
	} `json:"claudeAiOauth"`
}

// Credentials is what the poller needs from one Claude config dir: the token to
// ask with, and the plan the answer should be weighted by.
//
// SubscriptionType and RateLimitTier were already parsed and then dropped on the
// floor; the price-weighted average needs the plan, so they are returned now.
type Credentials struct {
	AccessToken      string
	SubscriptionType string // "max"
	RateLimitTier    string // "default_claude_max_20x"
}

// fingerprintToken reduces an access token to a short tag that says *which*
// credentials were used without carrying them.
//
// This is the one value derived from the token that is allowed out of this
// file, and it exists because nothing else on the machine links a token to an
// account: see CheckAccountBinding. It is a truncated digest, so it is not
// reversible, and it is still never logged -- knowing that two readings came
// from the same credentials is the whole of what any caller needs.
func fingerprintToken(token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])[:tokenFingerprintLen]
}

// keychainService names the keychain entry holding claudeDir's credentials.
//
// The default home uses the bare service name; any other config dir appends the
// first 8 hex characters of the sha256 of its absolute path. Verified against
// the real keychain: ~/.claude-2 → …-02925ee8, ~/.claude-3 → …-1f3efe9f,
// ~/.claude-4 → …-c9baa23d.
//
// This mapping is why the poller could not stay dir-blind. Reading identity
// from one config dir while reading the token from the bare entry does not
// produce a missing value — it produces a confident wrong one, fetching the org
// account's usage and stamping the personal account's name on it.
func keychainService(claudeDir, home string) string {
	if claudeDir == "" || claudeDir == filepath.Join(home, ".claude") {
		return baseKeychainService
	}
	sum := sha256.Sum256([]byte(claudeDir))
	return baseKeychainService + "-" + hex.EncodeToString(sum[:])[:keychainSuffixLen]
}

// ReadAccessTokenFor returns the credentials for one Claude config dir.
// On macOS the keychain is authoritative; elsewhere, and when the keychain
// lookup fails, the file beside the config dir is used.
func ReadAccessTokenFor(claudeDir string) (Credentials, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Credentials{}, fmt.Errorf("resolve home: %w", err)
	}
	if runtime.GOOS != "darwin" {
		return readFromFile(resolveDir(claudeDir, home))
	}
	return readFromKeychain(claudeDir, home)
}

func resolveDir(claudeDir, home string) string {
	if claudeDir == "" {
		return filepath.Join(home, ".claude")
	}
	return claudeDir
}

// readFromKeychain reads the entry belonging to claudeDir, and only that one.
//
// There is deliberately no fallback to the bare entry. It is tempting — a dir
// with no keychain entry would then still produce a token — but that token
// belongs to a different account, and this poller has no way to tell. The
// result is not a missing value but a confident wrong one: the org account's
// usage reported under the personal profile's name. A blank is recoverable;
// a plausible lie is not.
//
// The one case that fallback would have covered, a CLAUDE_CONFIG_DIR naming the
// default path, is already handled by the path comparison in keychainService.
//
// Falling back to the file in the *same* dir is fine: it is the same account.
func readFromKeychain(claudeDir, home string) (Credentials, error) {
	svc := keychainService(claudeDir, home)
	out, err := exec.Command("security", "find-generic-password", "-s", svc, "-w").Output()
	if err != nil {
		return readFromFile(resolveDir(claudeDir, home))
	}
	return parseCredentials([]byte(strings.TrimSpace(string(out))), "keychain "+svc)
}

func readFromFile(claudeDir string) (Credentials, error) {
	path := filepath.Join(claudeDir, ".credentials.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return Credentials{}, fmt.Errorf("read credentials: %w", err)
	}
	return parseCredentials(data, path)
}

// parseCredentials rejects a payload with no token instead of returning an
// empty one.
//
// The empty string used to pass as success, so the request went out as
// "Authorization: Bearer " and came back 401 — recording the failure as "API
// error" rather than "no token". Combined with the one-minute failure cache,
// that failed quietly and forever. It is not hypothetical: a
// .credentials.json whose only top-level key is mcpOAuth is an MCP OAuth file,
// not an Anthropic one, and it is exactly what the keychain falls back to.
func parseCredentials(data []byte, source string) (Credentials, error) {
	var creds claudeCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return Credentials{}, fmt.Errorf("parse %s: %w", source, err)
	}
	if creds.ClaudeAiOauth.AccessToken == "" {
		return Credentials{}, fmt.Errorf("no Anthropic OAuth token in %s (run 'claude login')", source)
	}
	return Credentials{
		AccessToken:      creds.ClaudeAiOauth.AccessToken,
		SubscriptionType: creds.ClaudeAiOauth.SubscriptionType,
		RateLimitTier:    creds.ClaudeAiOauth.RateLimitTier,
	}, nil
}
