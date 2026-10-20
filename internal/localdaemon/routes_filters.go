package localdaemon

import (
	"slices"
	"strings"
)

// Filters are the settings of a profile that decide which records it sends.
// Whoever reads a shared source applies its own, so a source must not move to a
// profile whose filters send more than its holder's did.
type Filters struct {
	// RepoPrefixes restricts collection to repositories whose id starts with one
	// of them; empty collects every repository.
	RepoPrefixes    []string
	RedactPrompts   bool
	RedactTools     bool
	ExcludeAccounts []string
}

// noLooser reports whether target sends nothing that old would not have sent:
// the repositories it takes are within old's, it redacts whatever old redacted,
// and it excludes every account old excluded. The comparison reads only the
// settings, never records.
func noLooser(old, target Filters) bool {
	return prefixesNoLooser(old.RepoPrefixes, target.RepoPrefixes) &&
		(!old.RedactPrompts || target.RedactPrompts) &&
		(!old.RedactTools || target.RedactTools) &&
		accountsCovered(old.ExcludeAccounts, target.ExcludeAccounts)
}

// prefixesNoLooser: an empty list takes every repository, so old taking all
// admits any target, and a target taking all fits only an old that takes all.
// Otherwise every prefix of target must equal or lie under a prefix of old, at a
// path boundary: "host/org" holds "host/org/repo" but not "host/organization".
// The collector matches a prefix as written (gitctx.AllowsRepository), so its
// trailing slash counts: "host/org" is wider than "host/org/", as it also takes
// "host/organization/...".
func prefixesNoLooser(old, target []string) bool {
	if len(old) == 0 {
		return true
	}
	if len(target) == 0 {
		return false
	}
	held := normalizePrefixes(old)
	for _, t := range normalizePrefixes(target) {
		if !slices.ContainsFunc(held, func(o string) bool {
			return strings.HasPrefix(t, o) && (len(t) == len(o) || strings.HasSuffix(o, "/") || t[len(o)] == '/')
		}) {
			return false
		}
	}
	return true
}

// normalizePrefixes reads prefixes the way the collector's allowlist does:
// case-insensitive, space trimmed, blank entries match nothing.
func normalizePrefixes(list []string) []string {
	var out []string
	for _, p := range list {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// accountsCovered: every account old excludes is excluded by target too. An
// entry is provider:id or a bare id that matches the id under any provider, so a
// bare id in target covers a provider entry in old but not the other way round.
func accountsCovered(old, target []string) bool {
	have := map[string]bool{}
	for _, e := range normalizeAccounts(target) {
		have[e] = true
	}
	for _, e := range normalizeAccounts(old) {
		if have[e] {
			continue
		}
		if _, id, ok := strings.Cut(e, ":"); ok && have[id] {
			continue
		}
		return false
	}
	return true
}

// normalizeAccounts spells entries as the client does when it installs them:
// space trimmed, the provider lower-cased, blank entries dropped.
func normalizeAccounts(list []string) []string {
	var out []string
	for _, e := range list {
		e = strings.TrimSpace(e)
		if provider, id, ok := strings.Cut(e, ":"); ok {
			e = strings.ToLower(strings.TrimSpace(provider)) + ":" + strings.TrimSpace(id)
		}
		if e != "" {
			out = append(out, e)
		}
	}
	return out
}

// FilterNoLooser reports whether a profile with filters target sends nothing that
// one with filters old would not have sent. A person asked to move a source
// between two profiles is told when it would not hold.
func FilterNoLooser(old, target Filters) bool { return noLooser(old, target) }
