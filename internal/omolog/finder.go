package omolog

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// uuidRe matches the UUID suffix in an omo session filename.
// e.g. 2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-000000000101.jsonl
var uuidRe = regexp.MustCompile(`_([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.jsonl$`)

// DefaultOmoDir returns the omo (senpi) config directory: $HOME/.omo.
func DefaultOmoDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".omo")
}

// sessionRoots are the two directories under omoDir that hold session
// JSONL files, each identically shaped (--<slugified cwd>--/*.jsonl):
//
//   - "sessions" holds the human-facing conversation - what the user
//     actually typed to omo.
//   - "agent/sessions" holds sessions omo itself spawned for its own
//     internal task work, not something a human typed.
//
// Session ids under the two roots are disjoint, so a caller must scan both
// to avoid silently dropping real usage.
var sessionRoots = []string{"sessions", filepath.Join("agent", "sessions")}

// FindSessionFiles returns all omo session JSONL file paths under omoDir,
// across both known session roots (see sessionRoots). Sessions live at
// <root>/--<slugified cwd>--/<timestamp>_<uuid>.jsonl. A session directory
// may also contain sibling artifact subdirectories (e.g.
// <timestamp>_<uuid>-artifacts/); only the top-level *.jsonl files directly
// inside each --*-- directory are sessions, so the glob pattern intentionally
// does not recurse. Results are deduplicated by absolute path and returned in
// a deterministic (sorted) order.
func FindSessionFiles(omoDir string) ([]string, error) {
	seen := make(map[string]struct{})
	var result []string
	for _, root := range sessionRoots {
		pattern := filepath.Join(omoDir, root, "--*--", "*.jsonl")
		// Report a bad pattern rather than skipping the root. ErrBadPattern is
		// Glob's only error and can only come from omoDir carrying glob
		// metacharacters, and omoDir is a prefix of every root's pattern -- so
		// when one root fails they all do, and swallowing it would return zero
		// files with no error. That is indistinguishable from "this user has no
		// omo sessions yet", which is how collection goes missing without
		// anyone noticing. An error reaches the syncer's log; a silent empty
		// scan does not.
		//
		// internal/codexlog continues past this error instead. That looks like
		// resilience across its several patterns, but its codexDir is a shared
		// prefix too, so it has the same silent-empty behavior. The difference
		// is not deliberate on either side; this is the safer half of it.
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			abs, err := filepath.Abs(m)
			if err != nil {
				continue
			}
			if _, ok := seen[abs]; !ok {
				seen[abs] = struct{}{}
				result = append(result, abs)
			}
		}
	}
	sort.Strings(result)
	return result, nil
}

// IsAgentSessionPath reports whether path sits under the agent/sessions
// root rather than the top-level sessions root. The distinction matters
// because the two roots mean different things: sessions/ is the
// human-facing conversation, while agent/sessions/ holds sessions omo
// itself spawned for its own task work. This is based purely on path
// structure (an "agent" directory immediately followed by a "sessions"
// directory), so it works regardless of where the omo dir itself lives.
func IsAgentSessionPath(path string) bool {
	dir := filepath.Dir(path)        // .../--slug--
	sessionsDir := filepath.Dir(dir) // .../sessions or .../agent/sessions
	if filepath.Base(sessionsDir) != "sessions" {
		return false
	}
	agentDir := filepath.Dir(sessionsDir) // .../agent or omoDir
	return filepath.Base(agentDir) == "agent"
}

// SessionIDFromPath extracts the session UUID from an omo session filename.
// Returns empty string if no UUID is found.
func SessionIDFromPath(path string) string {
	base := filepath.Base(path)
	m := uuidRe.FindStringSubmatch(base)
	if m == nil {
		return ""
	}
	return m[1]
}
