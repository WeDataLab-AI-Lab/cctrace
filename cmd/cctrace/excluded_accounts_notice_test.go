package main

import (
	"strings"
	"testing"
	"time"
)

// Records of an excluded account are consumed without being sent, which is
// silent by design. Status is where a person can see that it is happening, and
// which accounts it is happening to (#716).
func TestExcludedAccountsNotice(t *testing.T) {
	if got := excludedAccountsNotice(nil, nil, time.Now()); got != "" {
		t.Errorf("nothing excluded produced %q, want silence", got)
	}

	got := excludedAccountsNotice([]string{"openai:acct-local"},
		map[string]time.Time{"anthropic:acct-server": time.Now()}, time.Now())
	for _, want := range []string{"openai:acct-local", "anthropic:acct-server", "not uploaded"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing from:\n%s", want, got)
		}
	}
	// The two sources are undone in different places -- one in this profile, one
	// by whoever excluded it on the server -- so the reader has to know which.
	if !strings.Contains(got, "options.exclude_accounts") || !strings.Contains(got, "server") {
		t.Errorf("the notice does not say where each exclusion comes from:\n%s", got)
	}
}

// An account both listed locally and excluded on the server is named once.
func TestExcludedAccountsNoticeNamesAnAccountOnce(t *testing.T) {
	got := excludedAccountsNotice([]string{"openai:acct-both"},
		map[string]time.Time{"openai:acct-both": time.Now()}, time.Now())
	if n := strings.Count(got, "openai:acct-both"); n != 1 {
		t.Errorf("named %d times:\n%s", n, got)
	}
}

// A local entry without a provider names the same account the server reports
// as provider:id; it is one account and is named once.
func TestExcludedAccountsNoticeMatchesABareLocalID(t *testing.T) {
	got := excludedAccountsNotice([]string{"acct-both"},
		map[string]time.Time{"openai:acct-both": time.Now()}, time.Now())
	if n := strings.Count(got, "acct-both"); n != 1 {
		t.Errorf("named %d times:\n%s", n, got)
	}
}

// The server's answer is only refreshed for accounts a pass still sees, so an
// account no longer used here would otherwise stay listed for good. After a
// week without an answer it is no longer reported.
func TestExcludedAccountsNoticeDropsStaleServerEntries(t *testing.T) {
	now := time.Now()
	got := excludedAccountsNotice(nil, map[string]time.Time{
		"openai:acct-recent": now.Add(-time.Hour),
		"openai:acct-old":    now.Add(-serverExclusionShownFor - time.Hour),
	}, now)
	if !strings.Contains(got, "openai:acct-recent") {
		t.Errorf("a current server exclusion is missing:\n%s", got)
	}
	if strings.Contains(got, "openai:acct-old") {
		t.Errorf("a stale server exclusion is still reported:\n%s", got)
	}
}
