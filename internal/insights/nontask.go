package insights

import "strings"

// Turns that reach the classifier as role=user but carry no request.
//
// Claude Code stores a lot of machinery as user records: hook output, slash
// command scaffolding, another agent's message, the marker left when somebody
// presses escape. PromptFromRaw reads their text correctly -- they really are
// text on a user turn -- so the classifier saw them and answered "unknown",
// which is how a category that does not exist became the biggest slice of the
// chart.
//
// Counted on 61,131 typed turns from real session logs, the top "unknown"
// entries were, in order: [Request interrupted by user] 2,067,
// <local-command-caveat> 1,238, <system-reminder> variants 2,000+,
// [Request interrupted by user for tool use] 866, [Image: ...] 785,
// <teammate-message> 800+, <command-name> 800+, Stop hook feedback 380+ (#429).
//
// Matching is on the raw text rather than the lowered form: these are literal
// markers Claude Code writes, and lowering them buys nothing while making the
// intent less obvious.

// nonTaskPrefixes are markers a turn begins with. A prefix rather than a
// substring: a person quoting "[Request interrupted by user]" mid-sentence is
// still writing a request, and the scaffolding always leads.
var nonTaskPrefixes = []string{
	"[Request interrupted by user",
	"[Image:",
	"[Image #",
	"[Image source:",
	"<system-reminder>",
	"<local-command-caveat>",
	"<local-command-stdout>",
	"<local-command-stderr>",
	"<command-name>",
	"<command-message>",
	"<command-args>",
	"<bash-input>",
	"<bash-stdout>",
	"<bash-stderr>",
	"<teammate-message",
	"Stop hook feedback:",
	"Another Claude session sent a message:",
	"Caveat: The messages below were generated",
	// Orchestration and cross-session plumbing. Same shape as the above: a user
	// record whose text a machine wrote.
	"<task-notification>",
	"<conversation_history>",
	"<channel source=",
	"<om-senpi-task>",
	"[SYSTEM NOTIFICATION",
	"[Cross-session delivery notice]",
	"[Your previous response had no visible output",
	"Tool result (",
	"You have 1 orchestration message",
	"Output token limit hit.",
	"This session is being continued from a previous conversation",
}

// nonTaskExact are whole turns that are navigation rather than instruction.
// Exact, not prefix: "continue" alone is a nudge, while "continue from the
// design doc and add the missing section" is a request.
var nonTaskExact = []string{
	"Tool loaded.",
	"Continue from where you left off.",
	"continue",
	"계속",
	"ㅇㅇ",
	"ok",
	"okay",
	"yes",
	"no",
	"y",
	"n",
	"네",
	"응",
	"아니",
	"go",
	".",
	"Warmup",
	// Approving a proposal is not a task description: the work was named in the
	// turn before, and labelling these would put "do both" in a chart that
	// answers "what kind of work happened". Same reason "continue" is here.
	"둘 다 해줘",
	"둘 다 해",
	"둘 다 해.",
	"한번 해봐",
	"한번 해봐.",
	"해줘",
	"해.",
	"진행해",
	"진행해.",
	"계속 진행",
	"done",
	"ㄱ",
}

// shortInputRunes is the length at or below which a turn is treated as an
// acknowledgement rather than a request. Measured: single digits and letters are
// menu answers ("1", "2", "a"), and the short Korean forms are agreement. Three
// runes is the point where real requests start appearing, so the bound is
// deliberately below it.
const shortInputRunes = 2

func isNonTaskInput(text string) bool {
	if text == "" {
		return true
	}
	for _, prefix := range nonTaskPrefixes {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	lower := strings.ToLower(text)
	for _, exact := range nonTaskExact {
		if lower == strings.ToLower(exact) {
			return true
		}
	}
	// Counted in runes, not bytes: a two-character Korean acknowledgement is six
	// bytes, and a byte bound would let it through while rejecting "ok".
	if runeLen(text) <= shortInputRunes {
		return true
	}
	return isBareSlashCommand(text)
}

// A turn that is nothing but a slash command -- "/compact", "/clear" -- is an
// instruction to the client, not a request for work. Bare only: "/loop 5m check
// the deploy" carries one.
func isBareSlashCommand(text string) bool {
	if len(text) < 2 || text[0] != '/' {
		return false
	}
	for i := 1; i < len(text); i++ {
		c := text[i]
		if c == '-' || c == ':' || c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		return false
	}
	return true
}

func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
