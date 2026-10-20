package gitcmd

import (
	"regexp"
	"slices"
	"strings"
)

const MaxOutputBytes = 64 << 10

var commitLine = regexp.MustCompile(`(?m)^\[[^\]\r\n]* ([0-9a-f]{7,64})\] `)

// Output contains only hash-shaped commit receipts, never output text or a
// subject. These are prefixes until a separate work-tree lookup verifies them.
type Output struct {
	Hashes          []string
	NothingToCommit bool
	Incomplete      bool
}

func ReadOutput(text string) Output {
	out := Output{}
	if len(text) > MaxOutputBytes {
		out.Incomplete = true
		text = text[:MaxOutputBytes]
	}
	matches := commitLine.FindAllStringSubmatch(text, 101)
	if len(matches) > 100 {
		out.Incomplete = true
	}
	for _, match := range matches {
		if !slices.Contains(out.Hashes, match[1]) {
			if len(out.Hashes) == 100 {
				out.Incomplete = true
				break
			}
			out.Hashes = append(out.Hashes, match[1])
		}
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "nothing to commit") || strings.HasPrefix(line, "no changes added to commit") || strings.HasPrefix(line, "nothing added to commit") {
			out.NothingToCommit = true
		}
	}
	return out
}

func ValidHash(s string) bool {
	if len(s) < 7 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			if c < 'a' || c > 'f' {
				return false
			}
		}
	}
	return true
}
