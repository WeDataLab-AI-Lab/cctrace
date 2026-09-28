package airuntime

import "unicode/utf8"

// MaxToolOutputBytes caps one tool output sent to the model. Every request
// resends the whole conversation, so an oversized output is paid for again on
// each later request; report tools stay well under it.
const (
	MaxToolOutputBytes = 64 << 10
	TruncatedMark      = "\n[output truncated]"
)

// TruncateText keeps at most limit bytes of s, cut on a rune boundary, and
// appends mark when it cuts. The boundary search stops after utf8.UTFMax
// bytes, so bytes that are not UTF-8 are cut where they fall.
//
// Callers cap two different things with it: a tool output bound for the model
// (MaxToolOutputBytes, marked) and a response body bound for a log line (an
// unmarked, much smaller limit), so the limit and mark stay parameters.
func TruncateText(s string, limit int, mark string) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && cut > limit-utf8.UTFMax && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if !utf8.RuneStart(s[cut]) {
		cut = limit
	}
	return s[:cut] + mark
}
