package dependencyaudit

import (
	"regexp"
	"strings"
)

// secretPattern matches credential shapes: cctrace tokens, Anthropic, OpenAI,
// GitHub, GitLab, AWS and Slack keys, JWTs and PEM private keys (an unclosed
// key runs to the end of the text). A shape must not follow a letter or digit,
// so a word that merely contains one ("risk-assessment...") is left alone
// while one joined by "_" or "-" ("mcp__ghp_...") is not; the character before
// it is group 1 and is kept.
var secretPattern = regexp.MustCompile(`(?s)(^|[^A-Za-z0-9])(?:` + strings.Join([]string{
	`cct_[A-Za-z0-9_-]{8,}`,
	`sk-ant-[A-Za-z0-9_-]{8,}`,
	`sk-[A-Za-z0-9_-]{16,}`,
	`ghp_[A-Za-z0-9]{8,}`,
	`gh[pousr]_[A-Za-z0-9]{20,}`,
	`github_pat_[A-Za-z0-9_]{8,}`,
	`glpat-[A-Za-z0-9_-]{8,}`,
	`AKIA[0-9A-Z]{16}`,
	`xox[bp]-[A-Za-z0-9-]{8,}`,
	`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`,
	`-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|\z)`,
}, "|") + `)`)

// labelledSecrets keep what names a credential and mask the credential: a
// URL's user and password, a bearer token, and the value of a password,
// token, secret or API key setting. Each starts where no letter or digit
// comes before it.
var labelledSecrets = []struct {
	pattern *regexp.Regexp
	keep    string
}{
	{regexp.MustCompile(`(?i)(^|[^a-z0-9])([a-z][a-z0-9+.-]*://)[^/\s:@]+:[^/\s@]+@`), "${1}${2}[REDACTED]@"},
	{regexp.MustCompile(`(?i)(^|[^a-z0-9])(bearer\s+)\S{12,}`), "${1}${2}[REDACTED]"},
	{regexp.MustCompile(`(?i)(^|[^a-z0-9])((?:pass(?:word)?|passwd|pwd|token|secret|api[_-]?key)\s*[=:]\s*)\S+`), "${1}${2}[REDACTED]"},
}

// Mask replaces every credential shape in text. Every free-text field the
// command prints passes through it.
func Mask(text string) string {
	text = secretPattern.ReplaceAllString(text, "${1}[REDACTED]")
	for _, l := range labelledSecrets {
		text = l.pattern.ReplaceAllString(text, l.keep)
	}
	return text
}

const excerptRunes = 160

// excerpt is a masked, whitespace-collapsed, at most 160-character view of a
// text. It masks before cutting so that a cut cannot leave a key's prefix
// shorter than its pattern.
func excerpt(text string) string {
	text = strings.Join(strings.Fields(Mask(text)), " ")
	if r := []rune(text); len(r) > excerptRunes {
		text = string(r[:excerptRunes])
	}
	return text
}
