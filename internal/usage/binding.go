package usage

import (
	"errors"
	"fmt"
	"sync"
)

// ErrAccountSwitch reports that the credentials one reading was fetched with
// and the identity about to be stamped on it did not move together, so the two
// no longer describe the same account.
var ErrAccountSwitch = errors.New("credentials and identity disagree")

// binding is one observation of how a config dir's two stores were paired.
//
// The token side is held only as a fingerprint. This package's contract is that
// the token itself never leaves credentials.go, and the pairing needs to know
// that the credentials changed, not what they are.
type binding struct {
	accountUUID string
	token       string
}

// bindings remembers the last observed pairing per Claude config dir. It is
// keyed on the dir for the same reason the response cache is: one machine runs
// several at once holding different billing accounts, and they switch
// independently.
var (
	bindingMu sync.Mutex
	bindings  = map[string]binding{}
)

// CheckAccountBinding reports whether accountUUID may be stamped on r.
//
// The two halves of a quota row come from different stores. The reading is
// whatever the token in the keychain was allowed to ask for; the account uuid
// and login email come from .claude.json. `claude login` to a second account
// rewrites both, but not at the same instant, and a poll that lands in between
// reads one store before the switch and the other after it. The result is not a
// missing label but a wrong one -- one account's five-hour window, at its own
// utilization and its own reset time, filed under the other account's name.
// Observed once in 53 days of history (#538), which is also why no downstream
// filter can catch it: every field is a real value, and the account it belongs
// to is the only thing that is wrong.
//
// Re-reading .claude.json either side of the fetch does not detect this. In the
// observed case the identity store had *already* flipped before the tick began,
// so both reads agree on the new account while the keychain still holds the old
// one's token; the race window narrows and does not close. Nothing on the
// machine links a token to an account directly -- the usage API answers with no
// identity of its own -- so the only available evidence is that, while nothing
// is switching, the pair (account uuid, credentials) stays constant. A tick in
// which exactly one half has changed since the last observation is a tick taken
// during a switch, and what it says about ownership cannot be trusted.
//
// A pair that moves in both halves at once is a switch that landed entirely
// between two ticks, and is accepted: the alternative discards good history
// after every ordinary login. A token that changed alone is refused even though
// an OAuth refresh produces exactly that shape, because a refresh and a
// half-landed switch are indistinguishable from here; the cost is one
// five-minute sample per refresh, against a wrong row that survives forever.
//
// The observation is recorded even when the check fails. Otherwise every later
// tick would be compared against a pairing that no longer exists on the machine,
// and the poller would discard history for good rather than for the two ticks a
// switch actually spans.
func CheckAccountBinding(claudeDir, accountUUID string, r *Response) error {
	if r == nil || r.TokenFingerprint == "" || accountUUID == "" {
		// Not evidence of anything: a reading that never reached the keychain
		// has no fingerprint, and an unattributable account is already dropped
		// by the caller. Calling either a switch would name a condition in the
		// log that did not happen.
		return nil
	}
	cur := binding{accountUUID: accountUUID, token: r.TokenFingerprint}

	bindingMu.Lock()
	prev := bindings[claudeDir]
	bindings[claudeDir] = cur
	bindingMu.Unlock()

	switch {
	case prev == cur:
		// Nothing moved: the ordinary tick.
		return nil
	case prev.accountUUID != cur.accountUUID && prev.token != cur.token:
		// Both halves moved together: a completed switch. The first reading
		// after a restart lands here too, since the absent observation differs
		// in both halves. That is deliberate: with nothing to compare against
		// there is no evidence of a switch, and refusing to record history
		// until a second tick five minutes later would cost more than it saves.
		return nil
	}
	// The uuid is safe to name -- it is already written into every row this
	// path produces -- and the fingerprint deliberately is not.
	return fmt.Errorf("%w for %s (identity now says account %s)", ErrAccountSwitch, claudeDir, accountUUID)
}
