package usage

import (
	"errors"
	"testing"
)

// The observed defect (#538): the identity store moved to the new account while
// the keychain still held the old account's token, so one reading of user-a's
// usage was stamped with wedata_ai's account uuid. Nothing in the pair is
// individually wrong -- both stores are internally consistent -- so the only
// evidence available is that they stopped moving together.
//
// Mutation: drop the `prev.token != cur.token` half of the both-moved test in
// CheckAccountBinding, so any change of identity is accepted whatever the
// credentials did. This test then fails.
func TestCheckAccountBindingRejectsAnIdentityThatMovedWithoutTheToken(t *testing.T) {
	dir := t.TempDir()

	if err := CheckAccountBinding(dir, "acct-user-a", &Response{TokenFingerprint: "tok-user-a"}); err != nil {
		t.Fatalf("first observation rejected: %v", err)
	}
	err := CheckAccountBinding(dir, "acct-wedata", &Response{TokenFingerprint: "tok-user-a"})
	if !errors.Is(err, ErrAccountSwitch) {
		t.Fatalf("identity moved alone, got err = %v, want ErrAccountSwitch", err)
	}
}

// The mirror case named in the issue's completion conditions: the keychain is
// rewritten first and .claude.json has not caught up, so the reading belongs to
// the new account and would be stamped with the old one.
//
// Mutation: drop the `prev.accountUUID != cur.accountUUID` half of the
// both-moved test and this fails. A token refresh with no account switch trips
// this arm too; that costs one five-minute sample and is deliberate -- nothing on the machine
// distinguishes a refreshed token from a half-landed switch.
func TestCheckAccountBindingRejectsATokenThatMovedWithoutTheIdentity(t *testing.T) {
	dir := t.TempDir()

	if err := CheckAccountBinding(dir, "acct-user-a", &Response{TokenFingerprint: "tok-user-a"}); err != nil {
		t.Fatalf("first observation rejected: %v", err)
	}
	err := CheckAccountBinding(dir, "acct-user-a", &Response{TokenFingerprint: "tok-wedata"})
	if !errors.Is(err, ErrAccountSwitch) {
		t.Fatalf("token moved alone, got err = %v, want ErrAccountSwitch", err)
	}
}

// The regression guard. Ordinary polling repeats the same pair every tick, and
// a switch that lands entirely between two ticks moves both halves at once --
// neither may be discarded, or the history the chart draws would be full of
// holes for no reason.
//
// Mutation: reject on any change at all (drop the both-moved arm) and the
// second half of this fails. The same arm is what accepts the first reading
// after a restart, which TestSendQuotaSamplesStaysSilentWhenNothingSwitched
// covers from the caller's side.
func TestCheckAccountBindingAcceptsAPairThatMovesTogether(t *testing.T) {
	dir := t.TempDir()

	for i := 0; i < 3; i++ {
		if err := CheckAccountBinding(dir, "acct-user-a", &Response{TokenFingerprint: "tok-user-a"}); err != nil {
			t.Fatalf("steady pair rejected on tick %d: %v", i, err)
		}
	}
	if err := CheckAccountBinding(dir, "acct-wedata", &Response{TokenFingerprint: "tok-wedata"}); err != nil {
		t.Fatalf("completed switch rejected: %v", err)
	}
}

// Rejecting a tick must not wedge the poller: the mixed pair becomes the new
// observation, so the reading that follows it is judged against what was
// actually last seen rather than against a pair that no longer exists anywhere
// on the machine.
//
// Mutation: skip the map write when the check fails, and the final assertion
// here fails -- every later tick is compared to the stale pair and discarded.
func TestCheckAccountBindingRecoversAfterASwitch(t *testing.T) {
	dir := t.TempDir()

	_ = CheckAccountBinding(dir, "acct-user-a", &Response{TokenFingerprint: "tok-user-a"})
	// Identity first, then the keychain: two mixed ticks, both discarded.
	if err := CheckAccountBinding(dir, "acct-wedata", &Response{TokenFingerprint: "tok-user-a"}); err == nil {
		t.Fatal("mixed pair accepted")
	}
	if err := CheckAccountBinding(dir, "acct-wedata", &Response{TokenFingerprint: "tok-wedata"}); err == nil {
		t.Fatal("second mixed pair accepted")
	}
	if err := CheckAccountBinding(dir, "acct-wedata", &Response{TokenFingerprint: "tok-wedata"}); err != nil {
		t.Fatalf("poller still discarding after the switch settled: %v", err)
	}
}

// One machine runs several config dirs holding different accounts, and they
// switch independently. A pairing observed for one must not be compared against
// another's. Two dirs logged into the *same* account is the case that shows it:
// each has its own keychain entry, so the credentials differ legitimately, and
// against a shared memo the second dir reads as a token that moved alone.
//
// Mutation: key the bindings map on a constant and this fails.
func TestCheckAccountBindingIsPerConfigDir(t *testing.T) {
	if err := CheckAccountBinding(t.TempDir(), "acct-a", &Response{TokenFingerprint: "tok-a"}); err != nil {
		t.Fatalf("first dir: %v", err)
	}
	if err := CheckAccountBinding(t.TempDir(), "acct-a", &Response{TokenFingerprint: "tok-b"}); err != nil {
		t.Fatalf("second dir judged against the first: %v", err)
	}
}

// A reading with no fingerprint is not evidence of anything -- Fetch only omits
// it on paths that never reached the keychain -- and an unattributable reading
// is already dropped one line earlier by the caller. Neither may be reported as
// a switch, or the log would name a condition that did not happen.
//
// Mutation: remove the empty-value guards and this fails on the second call,
// which would otherwise read as a token that moved alone.
func TestCheckAccountBindingIgnoresMissingEvidence(t *testing.T) {
	dir := t.TempDir()

	if err := CheckAccountBinding(dir, "acct-a", &Response{TokenFingerprint: "tok-a"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := CheckAccountBinding(dir, "acct-a", &Response{}); err != nil {
		t.Fatalf("fingerprintless reading reported as a switch: %v", err)
	}
	if err := CheckAccountBinding(dir, "", &Response{TokenFingerprint: "tok-a"}); err != nil {
		t.Fatalf("blank account reported as a switch: %v", err)
	}
	if err := CheckAccountBinding(dir, "acct-a", nil); err != nil {
		t.Fatalf("nil response reported as a switch: %v", err)
	}
}

// The fingerprint is the whole reason the token side is knowable at all, and it
// is the one value in this file that must never be reversible or loggable.
//
// Mutation: return the token itself, or the full digest, and this fails.
func TestFingerprintTokenHidesTheToken(t *testing.T) {
	const token = "sk-ant-oat01-secret"
	got := fingerprintToken(token)

	if got == "" || got == token || len(got) != tokenFingerprintLen {
		t.Fatalf("fingerprint = %q, want %d opaque characters", got, tokenFingerprintLen)
	}
	if got == fingerprintToken(token+"x") {
		t.Fatal("two tokens share a fingerprint")
	}
	if fingerprintToken("") != "" {
		t.Fatal("an absent token must not get a fingerprint")
	}
}
