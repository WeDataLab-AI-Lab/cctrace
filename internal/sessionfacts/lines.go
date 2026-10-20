package sessionfacts

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// redactedMarker is what the client's redaction leaves in place of a value
	// (syncer.redactedMarker). A prompt policy puts it where message.content
	// was, so a user line's text and its tool results both read as the marker.
	redactedMarker = "[redacted by cctrace client]"
	// syntheticModel is the model of a line Claude Code wrote itself -- an API
	// error, a notice -- which is not a request.
	syntheticModel = "<synthetic>"
)

// epoch is where sync dates a line that carries no timestamp (the metadata
// lines, syncer.toStoreRecord). Such a row is not an event: nothing reads its
// ts.
var epoch = time.Unix(0, 0)

// line is a Claude row with its content blocks decoded and its place in the
// tree and in a turn, once those are known.
type line struct {
	*ClaudeRow
	projection *claudeProjection
	blocks     []block
	copied     bool // marked as another session's line, with the copy predicate on

	// The main-thread forest (tree.go). parent is nil for a root.
	parent   *line
	children int
	root     *line
	active   bool // on its component's active path
	mark     int8

	// turn is the turn the line belongs to; direct says the line opened it or
	// carries its prompt id, rather than following an ancestor into it.
	turn   *turn
	direct bool
	walked bool
	// fallback says no ancestor placed the line and assignMembers gave it the
	// latest turn opened before it: a time join, never an exact attribution.
	fallback bool
	// human says the line is an input that counts as a request: a request
	// turn's input line, or a prompt a person queued inside a turn.
	human bool
}

// block is one element of the blocks summary: a content block's type, id, name
// and, for a tool_result, the tool_use it answers and whether it is an error.
type block struct {
	taskEligible, taskUnknown bool
	taskTarget                string
	taskFallback              bool
	Type                      string          `json:"t"`
	ID                        string          `json:"id"`
	Name                      string          `json:"n"`
	ToolUseID                 string          `json:"tu"`
	TaskID                    string          `json:"tk"`
	TaskSubject               string          `json:"sub"`
	TaskDescription           string          `json:"desc"`
	TaskStatus                string          `json:"st"`
	TaskEdited                bool            `json:"ed"`
	TodoStatuses              []string        `json:"sts"`
	TodoContents              []string        `json:"tds"`
	InputHidden               bool            `json:"hid"`
	Err                       json.RawMessage `json:"err"`
	Input                     json.RawMessage `json:"in"`
}

func newLine(row *ClaudeRow, copied bool) *line {
	l := &line{ClaudeRow: row, copied: copied}
	// A summary that does not decode is a content this cannot read: no blocks.
	_ = json.Unmarshal(row.Blocks, &l.blocks)
	return l
}

// before orders lines by (ts, uuid), the order every rule reads them in. The id
// only separates two rows no rule can tell apart otherwise.
func (l *line) before(m *line) bool {
	if !l.TS.Equal(m.TS) {
		return l.TS.Before(m.TS)
	}
	if l.UUID != m.UUID {
		return l.UUID < m.UUID
	}
	return l.ID < m.ID
}

// dated reports whether the row carries a timestamp of its own.
func (l *line) dated() bool { return l.TS.After(epoch) }

// digested reports whether the row's uuid is the digest sync gives a line that
// has none: a metadata line, not a line of the conversation.
func (l *line) digested() bool { return strings.HasPrefix(l.UUID, "content-") }

// keyed reports whether the row carries a uuid of its own. A row stored before
// the column existed has none at all.
func (l *line) keyed() bool { return l.UUID != "" && !l.digested() }

func (l *line) has(blockType string) bool {
	for _, b := range l.blocks {
		if b.Type == blockType {
			return true
		}
	}
	return false
}

// bodyRedacted reports whether a user line's text cannot be read: the whole raw
// was withheld, or message.content is the marker.
func (l *line) bodyRedacted() bool {
	if l.projection != nil {
		return l.projection.redacted
	}
	return l.RawRedacted == redactedMarker || (l.ContentKind == "string" && l.TextHead == redactedMarker)
}

// contentRedacted is bodyRedacted for an assistant line, whose text the slim
// row does not carry. An assistant content is an array of blocks unless a
// policy replaced it or withheld the raw whole, so anything else cannot say
// whether it holds a tool_use.
func (l *line) contentRedacted() bool { return l.ContentKind != "array" }

// text is a user line's readable text from its first character.
func (l *line) text() string { return strings.TrimLeft(l.TextHead, " \t\r\n") }

func (l *line) textStarts(prefixes ...string) bool {
	if l.projection != nil {
		for _, p := range prefixes {
			if l.projection.starts(p) {
				return true
			}
		}
		return false
	}
	t := l.text()
	for _, p := range prefixes {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// isCommand reports whether a user line is a slash command someone typed. It
// reads the column, not the text, so redaction cannot hide it; "implicit" marks
// a skill the model chose, on whatever line carried the attribution. An empty
// command_invoke on an older row still names a command.
func (l *line) isCommand() bool { return l.CommandName != "" && l.CommandInvoke != "implicit" }

// isInterrupt reports whether a user line is the one Claude Code writes when a
// person interrupts: it carries interruptedMessageId since P0, and before that
// only its wording says so. Locally 500 of 672 such lines had the id and no
// other line did (A-19).
func (l *line) isInterrupt() bool {
	return l.InterruptedMessageID != "" || l.textStarts("[Request interrupted")
}

// columnJudged reports whether nothing but prompt_source can say what a user
// line is: its text is the marker, and the marker replaced a tool result just
// as it replaced what a person typed.
func (l *line) columnJudged() bool {
	return !l.IsMeta && !l.IsCompactSummary && !l.isCommand() && l.InterruptedMessageID == "" && l.bodyRedacted()
}

// hasText reports whether a readable user line carries text at all.
func (l *line) hasText() bool {
	return l.ContentKind == "string" || l.has("text") || l.has("input_text")
}

// isInput reports whether a user line is something put to the agent -- typed
// text, a command, a notification -- as opposed to what the harness writes
// around it. It is the shape rule that opens a turn on a line with no promptId
// (p1b-design §3.5 rule 2), and the same test picks a turn's input line (§3.9).
//
// A line whose text is the marker is not judged by shape: it is an input only
// when prompt_source says so. No tool_result line carries one (0 of 60,109
// locally) and 90% of typed lines do, so this never opens a turn on a tool
// result and misses about a tenth of the typed lines of a redacted, pre-P0
// session.
func (l *line) isInput() bool {
	switch {
	case l.IsMeta, l.IsCompactSummary:
		return false
	case l.isCommand():
		return true
	case l.InterruptedMessageID != "":
		return false
	case l.bodyRedacted():
		return l.PromptSource != ""
	case l.has("tool_result"),
		l.textStarts("<local-command-", "<bash-stdout>", "<bash-stderr>", "[Request interrupted"):
		return false
	}
	return l.hasText()
}

// isHumanText reports whether a user line is text a person wrote: an input
// that is neither a command nor any of the tagged lines, all of which start
// with "<". The marker does not, so a redacted input is human text. After a
// command line it opens a turn of its own (rule 1-b).
func (l *line) isHumanText() bool {
	return l.isInput() && !l.isCommand() && !l.textStarts("<")
}

// inputKind is the source of a user line taken as an input, from the first of
// the line's fields that names one (p1b-design §3.9). The values are the ones
// the fields really carry: origin.kind is hyphenated, turnOrigin underscored.
func (l *line) inputKind() string {
	switch {
	case l.isCommand():
		return "command"
	case l.textStarts("<bash-input>", "<skill"):
		return "command"
	case l.textStarts("<task-notification>"):
		return "notification"
	}
	switch l.OriginKind {
	case "human":
		if l.PromptSource == "queued" {
			return "queued"
		}
		return "typed"
	case "task-notification":
		return "notification"
	case "peer", "channel":
		return l.OriginKind
	case "auto-continuation":
		return "auto"
	}
	switch l.TurnOrigin {
	case "human":
		return "typed"
	case "task_notification":
		return "notification"
	case "peer", "scheduled", "sdk", "system":
		return l.TurnOrigin
	}
	switch l.PromptSource {
	case "typed", "paste", "suggestion_accepted":
		return "typed"
	case "queued", "sdk", "system":
		return l.PromptSource
	}
	return "unknown"
}

// personsOrigin reports whether an origin kind is a person's: "human", or none
// at all, as on lines older than the field. An origin the client dropped for
// its shape was there, so it is not none (p1b-design §3.2); field is the name
// cctraceDropped lists it under.
func (l *line) personsOrigin(kind, field string) bool {
	if l.projection != nil {
		return kind == "human" || (kind == "" && !l.projection.isDropped(field))
	}
	return kind == "human" || (kind == "" && !names(l.CctraceDropped, field))
}

// preview is the first line of a user line's text, cut to 160 bytes, and how
// it reads. A command is named from its column, which is what was typed and
// survives redaction.
func (l *line) preview() (*string, string) {
	if l.projection != nil {
		return nil, "none"
	}
	if l.isCommand() {
		p := "/" + l.CommandName
		return &p, "ok"
	}
	if l.bodyRedacted() {
		return nil, "redacted"
	}
	first, _, _ := strings.Cut(l.text(), "\n")
	first = strings.TrimRight(first, " \t\r")
	if first == "" {
		return nil, "none"
	}
	const limit = 160
	if len(first) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(first[cut]) {
			cut--
		}
		first = first[:cut]
	}
	return &first, "ok"
}

// fieldState is how a collected field of a line reads when a rule asks for it
// (p1b-design §3.2).
type fieldState string

const (
	statePresent    fieldState = "present"     // the field has a value
	stateZero       fieldState = "zero"        // absent on a collected row: the line had none
	stateWithheld   fieldState = "withheld"    // content a default re-enrichment kept on the machine
	stateDropped    fieldState = "dropped"     // held in a shape the client's rule does not read
	statePreCollect fieldState = "pre_collect" // absent on a row collected before the rules
	stateRedacted   fieldState = "redacted"    // a policy replaced it
)

// state reads the field name, whose value on the row is value. Only zero means
// the line did not have it: a withheld or dropped field was there, and a row
// without cctraceCollect was never asked. A value wins over the lists, since
// cctraceWithheld also names fields a later run did send.
func (l *line) state(name string, value json.RawMessage) fieldState {
	if l.projection != nil {
		return l.projection.fieldState(name)
	}
	switch {
	case l.RawRedacted == redactedMarker, string(value) == `"`+redactedMarker+`"`:
		return stateRedacted
	case jsonPresent(value):
		return statePresent
	case names(l.CctraceDropped, name):
		return stateDropped
	case names(l.CctraceWithheld, name):
		return stateWithheld
	case !jsonPresent(l.CctraceCollect):
		return statePreCollect
	}
	return stateZero
}

// names reports whether a cctraceDropped or cctraceWithheld list has name.
func names(list json.RawMessage, name string) bool {
	var all []string
	_ = json.Unmarshal(list, &all)
	for _, n := range all {
		if n == name {
			return true
		}
	}
	return false
}

// jsonPresent reports whether a jsonb column holds a value: SQL NULL and a JSON
// null are both absent.
func jsonPresent(v json.RawMessage) bool { return len(v) > 0 && string(v) != "null" }

// jsonInt reads a count the client wrote (imagePasteCount, imageCount). Any
// other shape is 0.
func jsonInt(v json.RawMessage) int {
	var n int
	_ = json.Unmarshal(v, &n)
	return n
}

// jsonText is a JSON string's text, and "" for any other value.
func jsonText(v json.RawMessage) string {
	var s string
	_ = json.Unmarshal(v, &s)
	return s
}

// jsonMembers is a JSON object's members, nil when v is not one. toolUseResult
// and attachment keep their members in whatever shape the line had them, so
// each is read on its own rather than decoded into a struct that one unexpected
// shape would fail whole.
func jsonMembers(v json.RawMessage) map[string]json.RawMessage {
	var members map[string]json.RawMessage
	_ = json.Unmarshal(v, &members)
	return members
}
