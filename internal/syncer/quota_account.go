package syncer

import "time"

// Quota readings are attributed by a different rule than session records, and
// the difference is deliberate.
//
// CodexAccountAt and ClaudeAccountAt answer with the latest observation at or
// before the instant, and with "" for anything earlier than the first
// observation — a record older than its home's first observation is left
// unattributed rather than guessed. That is right for cost: money credited to
// the wrong account is a wrong bill.
//
// A quota reading has a different shape. An observation *lags* the switch it
// records: the daemon stamps the new account when it next looks, not when the
// user actually ran /login. So the true switch lies somewhere inside the gap
// between two differing observations, and the reading in that gap belongs to
// the later account under the assumption that the switch happened at the start
// of the gap rather than the end.
//
// Assuming the other end leaks. If b is an excluded account and the gap is
// credited to a, b's usage appears on screen wearing a's label. Erring toward
// the later account errs toward hiding rather than exposing, which is the
// direction to be wrong in.
//
// Everything before the first observation is one such gap with no earlier end,
// which is the whole of the Codex backfill: the observation log begins when
// this ships and the files being walked predate it. Applying the session rule
// there would make the backfill insert nothing at all.
//
// What is inferred is reported as inferred. A value that is present is not the
// same as a value that is certain, and the chart draws the difference.

// QuotaAccountAt attributes a Codex quota reading taken at ts in home.
func (s *State) QuotaAccountAt(home string, ts time.Time) (string, bool) {
	if home == "" {
		return "", false
	}
	var prev, next string
	var havePrev, haveNext bool
	for _, obs := range s.CodexAccountObservations {
		if obs.Home != home {
			continue
		}
		if obs.ObservedAt.After(ts) {
			next, haveNext = obs.AccountID, true
			break
		}
		prev, havePrev = obs.AccountID, true
	}
	return resolveQuotaAccount(prev, havePrev, next, haveNext)
}

// QuotaAccountAtClaude is QuotaAccountAt over the Claude observation log, so
// both providers read a gap the same way.
func (s *State) QuotaAccountAtClaude(home string, ts time.Time) (string, bool) {
	if home == "" {
		return "", false
	}
	var prev, next string
	var havePrev, haveNext bool
	for _, obs := range s.ClaudeAccountObservations {
		if obs.Home != home {
			continue
		}
		if obs.ObservedAt.After(ts) {
			next, haveNext = obs.AccountUUID, true
			break
		}
		prev, havePrev = obs.AccountUUID, true
	}
	return resolveQuotaAccount(prev, havePrev, next, haveNext)
}

// resolveQuotaAccount applies the rule to one home's surrounding observations.
func resolveQuotaAccount(prev string, havePrev bool, next string, haveNext bool) (string, bool) {
	switch {
	case !havePrev && !haveNext:
		// Nothing observed for this home. There is no account to credit and
		// another home's is never a fallback.
		return "", false
	case !havePrev:
		// Before everything known: one open-ended gap.
		return next, true
	case !haveNext:
		// The standing account. This is the live case, where the reading and
		// the observation are effectively simultaneous.
		return prev, false
	case prev == next:
		// The gap does not straddle a switch.
		return prev, false
	default:
		return next, true
	}
}
