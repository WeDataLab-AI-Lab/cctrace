// Package projecthash derives and repairs the project_hash that identifies a
// working directory.
//
// The convention is not ours. Claude Code creates a directory per project under
// ~/.claude/projects and that directory's name is the hash we record for claude
// sessions, so every other agent has to derive the same string from the same
// working directory. When they do not, one directory ends up under several keys and
// a project filter silently drops whatever sessions used the other spelling (#303).
package projecthash

import "strings"

// FromPath converts a working directory to its project hash.
//
// The shape of the rule was read off real Claude Code directory names rather than
// assumed: every rune that is not ASCII alphanumeric or '-' becomes '-', and nothing
// is trimmed. That covers the separators on both platforms ('/', '\', ':'), the
// characters people put in directory names ('.', '_', ' '), and non-ASCII text, which
// contributes one dash per rune rather than one per byte.
//
// Nothing is trimmed on purpose: a POSIX path's leading '/' becomes a leading '-',
// which is exactly what Claude Code produces. Trimming it was how the previous
// implementation split every POSIX project in two.
//
// One deliberate divergence: letters are lowercased, which Claude Code does not do.
// macOS and Windows are case-insensitive, so the same directory arrives spelled
// several ways -- 'Documents/GitHub' and 'Documents/Github' were eleven separate
// projects in our own data, and a Windows drive shows up as both 'C:' and 'c:'.
// Folding case merges them. The cost is on case-sensitive filesystems, where two
// genuinely different directories differing only in case become one project; that is
// rarer than the split it fixes, and it is why callers must canonicalise a
// project_hash filter through Repair instead of comparing raw strings.
//
// FromPath is idempotent on values it has already produced.
func FromPath(cwd string) string {
	if cwd == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(cwd))
	for _, r := range cwd {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// Repair converts a stored or received hash to the canonical spelling.
//
// This is not FromPath, and the difference matters. Clients before v0.7.21 trimmed
// the leading separator, so "/Users/alice/myapp" was stored as "users-alice-myapp". That
// leading '/' is gone: no function of the string alone can prove it was ever there.
// Repair therefore uses the shape of the value, which is a judgement, not a
// derivation:
//
//   - already starts with '-'  -> canonical, leave it
//   - looks like a drive root ("C--project-x") -> canonical Windows form, leave it
//   - a synthetic fallback key ("codex-9f8e") -> not a path at all, leave it
//   - anything else -> a POSIX path that lost its leading separator, restore it
//
// The one thing it can get wrong is a hash that never came from an absolute path and
// happens to match none of the exemptions; it would gain a leading dash it did not
// have. Every producer in this repository emits an absolute path or a prefixed
// fallback, so that case does not arise here -- but it is the reason this is a
// separate function with its own name rather than something FromPath does quietly.
func Repair(hash string) string {
	if hash == "" {
		return ""
	}
	h := FromPath(hash)
	if strings.HasPrefix(h, "-") || isDriveRooted(h) || isFallbackKey(h) {
		return h
	}
	return "-" + h
}

// isDriveRooted reports whether the hash came from a Windows path: a single drive
// letter, then the two dashes that ':' and '\' collapse into.
func isDriveRooted(h string) bool {
	if len(h) < 3 {
		return false
	}
	c := h[0]
	return c >= 'a' && c <= 'z' && h[1] == '-' && h[2] == '-'
}

// isFallbackKey reports whether the hash is a synthetic key an agent generates when
// it cannot determine a working directory. These are not paths and must not be
// given a leading separator.
func isFallbackKey(h string) bool {
	for _, prefix := range []string{"codex-", "gjc-", "omo-", "claude-"} {
		if strings.HasPrefix(h, prefix) {
			return true
		}
	}
	return false
}

// NameFromPath returns the human-readable project name: the last path segment.
//
// Separate from the hash on purpose. The hash has to be a single canonical token, so
// it flattens everything; the name is what someone reads in a list and should stay
// as they wrote it, case and underscores included.
//
// It splits on both separators because a Windows client reports a Windows path, and
// splitting on '/' alone left the whole "C:\work\app\.agents" sitting in the name
// column where a directory name belonged.
func NameFromPath(cwd string) string {
	trimmed := strings.TrimRight(cwd, `/\`)
	if trimmed == "" {
		return ""
	}
	if i := strings.LastIndexAny(trimmed, `/\`); i >= 0 {
		return trimmed[i+1:]
	}
	return trimmed
}
