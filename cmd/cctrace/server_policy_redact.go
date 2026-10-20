package main

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"cctrace/internal/profile"
)

// explicitCodexHomeMask stands for an explicit Codex home wherever a policy
// reason would name it, as the dry run and the config display do.
const explicitCodexHomeMask = "[explicit Codex home]"

// redactPolicyReason masks the explicit Codex home of every profile on this
// machine in a reason that leaves it: a policy report, a failure recorded in the
// state, a note on what an applied policy left alone. A home is a directory the
// user chose, and the reasons are read by people the server's operator shares
// them with. Everything else in the reason is kept.
//
// Not a security boundary: it masks the homes the profiles record, as written
// there or resolved through symlinks. A profile that cannot be read names none.
func redactPolicyReason(reason string) string {
	var homes []string
	_ = eachMachineProfile(func(_ string, p *profile.Profile) {
		if p.CodexHome == "" {
			return
		}
		homes = append(homes, filepath.Clean(p.CodexHome))
		if resolved, err := filepath.EvalSymlinks(p.CodexHome); err == nil {
			homes = append(homes, resolved)
		}
	})
	// A home inside another is masked whole: the longer name first.
	sort.Slice(homes, func(i, j int) bool { return len(homes[i]) > len(homes[j]) })
	for _, home := range homes {
		// A home that is a root names every path; there is nothing to mask in it.
		if filepath.Dir(home) == home {
			continue
		}
		reason = maskPath(reason, home)
	}
	return reason
}

// maskPath replaces each occurrence of path in text that stands for that path,
// not for a longer name that starts or ends with it: it must follow the start of
// the text or a character that cannot continue a name, and be followed by the end
// of the text, a separator, a colon, white space or a closing quote or bracket.
func maskPath(text, path string) string {
	var out strings.Builder
	copied := 0
	for from := 0; ; {
		i := strings.Index(text[from:], path)
		if i < 0 {
			break
		}
		start, end := from+i, from+i+len(path)
		from = end
		if before, _ := utf8.DecodeLastRuneInString(text[:start]); start > 0 && isNameChar(before) {
			continue
		}
		if after, _ := utf8.DecodeRuneInString(text[end:]); end < len(text) && !strings.ContainsRune("/\\:;,\"')] \t\r\n", after) {
			continue
		}
		out.WriteString(text[copied:start])
		out.WriteString(explicitCodexHomeMask)
		copied = end
	}
	out.WriteString(text[copied:])
	return out.String()
}

// isNameChar reports whether c can be part of a path component or the one before.
func isNameChar(c rune) bool {
	return c == '/' || c == '\\' || c == '.' || c == '-' || c == '_' || c == '~' || unicode.IsLetter(c) || unicode.IsDigit(c)
}

// eachMachineProfile visits the default profile (named "") and every named one.
// A name with no profile behind it, such as no default profile or an empty
// directory among the named ones, is no profile and is skipped. A profile that
// cannot be read is skipped too, and the first such error is returned once all
// have been visited: absence is not evidence, so a caller that needs every profile
// must not trust a visit that came back with one.
func eachMachineProfile(visit func(name string, p *profile.Profile)) error {
	names, firstErr := profile.ListNamed()
	for _, name := range append([]string{""}, names...) {
		var p *profile.Profile
		var err error
		if name == "" {
			p, err = profile.Load()
		} else {
			p, err = profile.LoadNamed(name)
		}
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		visit(name, p)
	}
	return firstErr
}
