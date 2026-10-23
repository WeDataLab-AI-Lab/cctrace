package dependencyaudit

import (
	"regexp"
	"strings"
)

// injectedTags wrap user-role text a harness wrote rather than the person: the
// weekly report's set (command and hook output, slash-command plumbing,
// reminders, environment and instruction preambles) and the messages agents
// and sessions send each other.
var injectedTags = []string{
	"system-reminder", "bash-input", "bash-stdout", "bash-stderr",
	"command-name", "command-message", "command-args",
	"local-command-stdout", "local-command-stderr", "local-command-caveat",
	"environment_context", "user_instructions",
	"subagent_notification", "cross-session-message", "turn_aborted",
	"skill", "send_user_message", "task-notification",
}

// injectedBlock matches one closed tagged block wherever it sits.
var injectedBlock = func() *regexp.Regexp {
	alts := make([]string, 0, len(injectedTags))
	for _, tag := range injectedTags {
		alts = append(alts, "<"+tag+`(?:\s[^>]*)?>.*?</`+tag+`>`)
	}
	return regexp.MustCompile(`(?s)` + strings.Join(alts, "|"))
}()

// injectedOpening is a tag that opens what is left of a message once the
// closed blocks are gone: an unclosed block a harness wrote, running to the
// end. The same tag further in is a person writing about it and stays.
var injectedOpening = regexp.MustCompile(`^<(?:` + strings.Join(injectedTags, "|") + `)(?:\s[^>]*)?>`)

// injectedLeads open a whole message a harness wrote. They are sentences, not
// blocks, so they mark a message only when it starts with one.
var injectedLeads = []string{"Another Claude session sent a message:", "# AGENTS.md instructions"}

// StripInjected removes injected blocks from a user message and keeps what the
// person wrote. ok is false when nothing of theirs is left: the message was
// only blocks, or it leads with a harness sentence once the blocks are gone.
func StripInjected(text string) (string, bool) {
	text = strings.TrimSpace(injectedBlock.ReplaceAllString(text, ""))
	if injectedOpening.MatchString(text) {
		return "", false
	}
	for _, lead := range injectedLeads {
		if strings.HasPrefix(text, lead) {
			return "", false
		}
	}
	return text, text != ""
}
