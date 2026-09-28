package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/usage"
)

// An unknown account is a real state -- a home that never logged in has none --
// but it is indistinguishable from a broken one in the log, because until now it
// produced no log at all.
//
// The line above it, the ReadAccount error, has always been reported. This
// branch is the one that stayed quiet, and it is the branch that actually fired:
// on every Windows profile the config path comparison missed, the token still
// resolved and quota snapshots still arrived, so the only externally visible
// symptom was history that never appeared. `tail` showed nothing to explain it.
func TestSendQuotaSamplesReportsAnUnknownAccount(t *testing.T) {
	useProgressTTY(t, false)
	quotaSamplesUnsupported.Store(false)
	t.Cleanup(func() { quotaSamplesUnsupported.Store(false) })

	// A directory with no .claude.json beside or inside it: ReadAccount returns
	// a blank account and no error, which is exactly the silent branch.
	claudeDir := t.TempDir()

	stdout, stderr := captureOutput(t, func() {
		sendQuotaSamples(context.Background(), nil, claudeDir, "someone@example.com", nil)
	})

	out := stdout + stderr
	if !strings.Contains(out, "[quota]") || !strings.Contains(out, claudeDir) {
		t.Fatalf("no report naming the config dir; got %q", out)
	}
}

// The latch that fires when a server has no history endpoint must keep costing
// one log line per process, not one per tick. Reporting the missing account
// ahead of it would turn a silenced condition back into a repeating one.
func TestSendQuotaSamplesStaysQuietOnceTheServerIsKnownUnsupported(t *testing.T) {
	useProgressTTY(t, false)
	quotaSamplesUnsupported.Store(true)
	t.Cleanup(func() { quotaSamplesUnsupported.Store(false) })

	stdout, stderr := captureOutput(t, func() {
		sendQuotaSamples(context.Background(), nil, t.TempDir(), "someone@example.com", nil)
	})

	if out := stdout + stderr; out != "" {
		t.Fatalf("expected no output, got %q", out)
	}
}

// writeAccountConfig gives claudeDir a .claude.json naming one account, which
// is where sendQuotaSamples reads the identity it stamps.
func writeAccountConfig(t *testing.T, claudeDir, accountUUID string) {
	t.Helper()
	body := `{"oauthAccount":{"accountUuid":"` + accountUUID + `","emailAddress":"someone@example.test"}}`
	if err := os.WriteFile(filepath.Join(claudeDir, ".claude.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write .claude.json: %v", err)
	}
}

// #538: the token comes from the keychain and the identity from .claude.json,
// and an account switch does not rewrite both at once. A tick that reads one
// store before the switch and the other after it produces a reading of one
// account's usage stamped with the other's uuid -- a wrong, plausible row that
// no filter downstream can recognise. The whole tick has to go.
//
// Mutation: drop the CheckAccountBinding call from sendQuotaSamples and this
// fails -- the mixed tick is sent, silently.
func TestSendQuotaSamplesDiscardsATickWhoseTokenAndIdentityDisagree(t *testing.T) {
	useProgressTTY(t, false)
	quotaSamplesUnsupported.Store(false)
	t.Cleanup(func() { quotaSamplesUnsupported.Store(false) })

	claudeDir := t.TempDir()
	// The steady pairing this dir was last seen in: the old account, holding
	// the token that is still in the keychain.
	if err := usage.CheckAccountBinding(claudeDir, "acct-old", &usage.Response{TokenFingerprint: "tok-old"}); err != nil {
		t.Fatalf("seed the binding: %v", err)
	}

	// The switch has reached .claude.json but not the keychain, so the reading
	// still carries the old account's credentials.
	writeAccountConfig(t, claudeDir, "acct-new")
	u := &usage.Response{
		FetchedAt:        time.Now(),
		TokenFingerprint: "tok-old",
		FiveHour:         &usage.UsageWindow{Utilization: 100, ResetsAt: "2026-09-04T05:19:59Z"},
	}

	stdout, stderr := captureOutput(t, func() {
		// A nil client is safe only because nothing may be sent: reaching the
		// send is itself the failure this test is looking for.
		sendQuotaSamples(context.Background(), nil, claudeDir, "someone@example.test", u)
	})

	out := stdout + stderr
	if !strings.Contains(out, "[quota]") || !strings.Contains(out, "acct-new") {
		t.Fatalf("account switch went unreported; got %q", out)
	}
	if strings.Contains(out, "tok-old") {
		t.Fatalf("credential fingerprint leaked into the log: %q", out)
	}
}

// The guard must cost the ordinary case nothing. With the identity and the
// credentials moving together -- which is every tick that is not a switch --
// sendQuotaSamples has to reach the send with no complaint of its own.
//
// Mutation: reject on any change of either half, first observation included,
// and this fails.
func TestSendQuotaSamplesStaysSilentWhenNothingSwitched(t *testing.T) {
	useProgressTTY(t, false)
	quotaSamplesUnsupported.Store(false)
	t.Cleanup(func() { quotaSamplesUnsupported.Store(false) })

	claudeDir := t.TempDir()
	writeAccountConfig(t, claudeDir, "acct-steady")
	// FetchedAt is left zero so Samples() yields nothing and the nil client is
	// never reached; the binding check runs before that either way.
	u := &usage.Response{TokenFingerprint: "tok-steady"}

	for i := 0; i < 3; i++ {
		stdout, stderr := captureOutput(t, func() {
			sendQuotaSamples(context.Background(), nil, claudeDir, "someone@example.test", u)
		})
		if out := stdout + stderr; out != "" {
			t.Fatalf("tick %d reported %q on an unchanged account", i, out)
		}
	}
}
