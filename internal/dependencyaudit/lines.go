package dependencyaudit

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"cctrace/internal/gitcmd"
	"cctrace/internal/localapi"
)

// fileData is what one file's raw lines say, gathered while Analyze reads it
// and used only once the file turned out stable.
type fileData struct {
	entry     localapi.Entry
	sessionID string
	cwd       string
	firstTS   time.Time
	lastTS    time.Time
	invalid   int
	oversized int

	users       []*userLine
	assistant   []outputLine
	calls       map[string]*toolCall
	callOrder   []string
	interrupts  []*marker
	compactions int
	// otherAborts are Codex turns aborted for a reason other than an
	// interrupt.
	otherAborts []*marker

	// The last line of the last assistant text so far, for the input that
	// follows it.
	lastAssistant string
}

// userLine is a user message with text: a Claude user line or a Codex user
// message. Which of them a person wrote is the facts' decision.
type userLine struct {
	uuid         string
	ts           time.Time
	line         int
	row          int64
	text         string
	promptSource string
	prev         string // the last line of the assistant text before it
	compaction   int    // compactions seen before it
	// queued marks a prompt queued while the agent worked (an attachment
	// line); origin is its origin kind, "" when the line names none.
	queued bool
	origin string
}

// outputLine is one line of assistant text, with the Claude record it came
// from ("" for Codex).
type outputLine struct {
	text string
	ts   time.Time
	uuid string
}

type question struct {
	Question    string   `json:"question"`
	MultiSelect bool     `json:"multiSelect"`
	Options     []option `json:"options"`
}

type option struct {
	Label string `json:"label"`
}

// toolCall is a tool call joined with its result in one file.
type toolCall struct {
	id, name  string
	ts        time.Time
	line      int
	sidechain bool
	shell     bool   // a shell command tool
	command   string // its literal command; "" when unparsed
	questions []question

	hasResult  bool
	resultTS   time.Time
	resultLine int
	resultRow  int64
	isError    bool
	permission string // permissionDecision.source
	// rejectedDecision is a permissionDecision whose decision is reject.
	rejectedDecision bool
	observed         bool   // the result carries a permissionDecision
	denial           string // toolDenialKind
	answers          map[string]string
	notes            map[string]bool
	// userAnswered is whether a Codex request_user_input call's output holds
	// an answer; the output itself is not kept.
	userAnswered bool
	resultQuest  []question
}

// marker is a Claude interrupt line or a Codex turn_aborted event.
type marker struct {
	key  string
	ts   time.Time
	line int
}

func newFileData(e localapi.Entry) *fileData {
	return &fileData{entry: e, calls: map[string]*toolCall{}}
}

func (f *fileData) call(id string) *toolCall {
	c := f.calls[id]
	if c == nil {
		c = &toolCall{id: id}
		f.calls[id] = c
		f.callOrder = append(f.callOrder, id)
	}
	return c
}

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func (f *fileData) seen(ts time.Time) {
	if ts.IsZero() {
		return
	}
	if f.firstTS.IsZero() || ts.Before(f.firstTS) {
		f.firstTS = ts
	}
	if ts.After(f.lastTS) {
		f.lastTS = ts
	}
}

// visit reads one raw line; it is the Visit callback of localapi.Analyze.
func (f *fileData) visit(row localapi.RowRef, raw []byte) {
	if raw == nil {
		f.oversized++
		return
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return
	}
	if f.entry.Agent == "codex" {
		f.visitCodex(row, raw)
	} else {
		f.visitClaude(row, raw)
	}
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
}

func (f *fileData) visitClaude(row localapi.RowRef, raw []byte) {
	var rec struct {
		Type               string `json:"type"`
		UUID               string `json:"uuid"`
		Timestamp          string `json:"timestamp"`
		SessionID          string `json:"sessionId"`
		CWD                string `json:"cwd"`
		IsSidechain        bool   `json:"isSidechain"`
		IsCompactSummary   bool   `json:"isCompactSummary"`
		PromptSource       string `json:"promptSource"`
		Subtype            string `json:"subtype"`
		ToolDenialKind     string `json:"toolDenialKind"`
		PermissionDecision *struct {
			Decision string `json:"decision"`
			Source   string `json:"source"`
		} `json:"permissionDecision"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		ToolUseResult json.RawMessage `json:"toolUseResult"`
		Attachment    struct {
			Type        string `json:"type"`
			CommandMode string `json:"commandMode"`
			Prompt      string `json:"prompt"`
			Origin      struct {
				Kind string `json:"kind"`
			} `json:"origin"`
		} `json:"attachment"`
	}
	if json.Unmarshal(raw, &rec) != nil {
		f.invalid++
		return
	}
	ts := parseTS(rec.Timestamp)
	f.seen(ts)
	if f.sessionID == "" {
		f.sessionID = rec.SessionID
	}
	if f.cwd == "" {
		f.cwd = rec.CWD
	}
	switch rec.Type {
	case "system":
		if rec.Subtype == "compact_boundary" {
			f.compactions++
		}
		return
	case "attachment":
		at := rec.Attachment
		if at.Type == "queued_command" && at.CommandMode == "prompt" && at.Prompt != "" && !rec.IsSidechain {
			f.users = append(f.users, &userLine{uuid: rec.UUID, ts: ts, line: row.Line, row: row.ID, text: at.Prompt, prev: f.lastAssistant, compaction: f.compactions, queued: true, origin: at.Origin.Kind})
		}
		return
	case "user", "assistant":
	default:
		return
	}
	var text string
	var blocks []claudeBlock
	content := bytes.TrimSpace(rec.Message.Content)
	if len(content) > 0 && content[0] == '"' {
		_ = json.Unmarshal(content, &text)
	} else if len(content) > 0 && content[0] == '[' {
		_ = json.Unmarshal(content, &blocks)
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		text = strings.Join(parts, "\n")
	}
	if rec.Type == "assistant" {
		for _, b := range blocks {
			if b.Type != "tool_use" || b.ID == "" {
				continue
			}
			c := f.call(b.ID)
			c.name, c.ts, c.line, c.sidechain = b.Name, ts, row.Line, rec.IsSidechain
			var input struct {
				Command   string     `json:"command"`
				Questions []question `json:"questions"`
			}
			_ = json.Unmarshal(b.Input, &input)
			if b.Name == "Bash" {
				c.shell, c.command = true, input.Command
			}
			if b.Name == "AskUserQuestion" {
				c.questions = input.Questions
			}
		}
		if text != "" && !rec.IsSidechain {
			f.lastAssistant = lastLine(text)
			for _, line := range strings.Split(text, "\n") {
				f.assistant = append(f.assistant, outputLine{line, ts, rec.UUID})
			}
		}
		return
	}
	if rec.IsCompactSummary {
		f.compactions++
		return
	}
	for _, b := range blocks {
		if b.Type != "tool_result" || b.ToolUseID == "" {
			continue
		}
		c := f.call(b.ToolUseID)
		c.hasResult, c.resultTS, c.resultLine, c.resultRow, c.isError = true, ts, row.Line, row.ID, b.IsError
		c.denial = rec.ToolDenialKind
		if rec.PermissionDecision != nil {
			c.observed, c.permission, c.rejectedDecision = true, rec.PermissionDecision.Source, rec.PermissionDecision.Decision == "reject"
		}
		readQuestionResult(c, rec.ToolUseResult)
	}
	if strings.HasPrefix(text, "[Request interrupted by user") && !strings.Contains(text, "for tool use") && !rec.IsSidechain {
		key := ""
		if rec.UUID != "" {
			key = "claude:uuid:" + rec.UUID
		}
		f.interrupts = append(f.interrupts, &marker{key: key, ts: ts, line: row.Line})
		return
	}
	if text != "" && !rec.IsSidechain {
		f.users = append(f.users, &userLine{uuid: rec.UUID, ts: ts, line: row.Line, row: row.ID, text: text, promptSource: rec.PromptSource, prev: f.lastAssistant, compaction: f.compactions})
	}
}

// readQuestionResult reads an AskUserQuestion result: the questions, the
// answers (a multi-select answer may be a list) and whether a note was added.
func readQuestionResult(c *toolCall, raw json.RawMessage) {
	if len(raw) == 0 || raw[0] != '{' {
		return
	}
	var result struct {
		Questions   []question                 `json:"questions"`
		Answers     map[string]json.RawMessage `json:"answers"`
		Annotations map[string]struct {
			Notes string `json:"notes"`
		} `json:"annotations"`
	}
	if json.Unmarshal(raw, &result) != nil || len(result.Questions) == 0 {
		return
	}
	c.resultQuest = result.Questions
	c.answers = map[string]string{}
	for q, value := range result.Answers {
		var one string
		var many []string
		if json.Unmarshal(value, &one) == nil {
			c.answers[q] = one
		} else if json.Unmarshal(value, &many) == nil {
			c.answers[q] = strings.Join(many, ", ")
		}
	}
	c.notes = map[string]bool{}
	for q, a := range result.Annotations {
		if strings.TrimSpace(a.Notes) != "" {
			c.notes[q] = true
		}
	}
}

func (f *fileData) visitCodex(row localapi.RowRef, raw []byte) {
	var rec struct {
		Timestamp string `json:"timestamp"`
		Type      string `json:"type"`
		Payload   struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			CWD       string          `json:"cwd"`
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			Name      string          `json:"name"`
			CallID    string          `json:"call_id"`
			Arguments json.RawMessage `json:"arguments"`
			Input     string          `json:"input"`
			Output    json.RawMessage `json:"output"`
			TurnID    string          `json:"turn_id"`
			Reason    string          `json:"reason"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &rec) != nil {
		f.invalid++
		return
	}
	ts := parseTS(rec.Timestamp)
	f.seen(ts)
	p := rec.Payload
	switch {
	case rec.Type == "session_meta":
		if f.sessionID == "" {
			f.sessionID = p.ID
		}
		if f.cwd == "" {
			f.cwd = p.CWD
		}
	case rec.Type == "turn_context" && f.cwd == "":
		f.cwd = p.CWD
	case rec.Type == "event_msg" && p.Type == "turn_aborted":
		key := ""
		if p.TurnID != "" {
			key = "codex:turn:" + p.TurnID
		}
		m := &marker{key: key, ts: ts, line: row.Line}
		if p.Reason == "interrupted" {
			f.interrupts = append(f.interrupts, m)
		} else {
			f.otherAborts = append(f.otherAborts, m)
		}
	case rec.Type != "response_item":
	case p.Type == "message":
		text := codexText(p.Content)
		if p.Role == "user" && text != "" {
			f.users = append(f.users, &userLine{ts: ts, line: row.Line, row: row.ID, text: text, prev: f.lastAssistant})
		}
		if p.Role == "assistant" && text != "" {
			f.lastAssistant = lastLine(text)
			for _, line := range strings.Split(text, "\n") {
				f.assistant = append(f.assistant, outputLine{line, ts, ""})
			}
		}
	case (p.Type == "function_call" || p.Type == "custom_tool_call") && p.CallID != "":
		c := f.call(p.CallID)
		c.name, c.ts, c.line = p.Name, ts, row.Line
		args := p.Input
		if p.Type == "function_call" {
			args = string(p.Arguments)
			var decoded string
			if json.Unmarshal(p.Arguments, &decoded) == nil {
				args = decoded
			}
		}
		command, dynamic := gitcmd.CodexCommand(p.Name, args)
		// exec code that runs no shell command (a patch, terminal input) is
		// not a command; one built at run time is a command left unparsed.
		c.shell, c.command = command != "" || dynamic, command
	case (p.Type == "function_call_output" || p.Type == "custom_tool_call_output") && p.CallID != "":
		c := f.call(p.CallID)
		c.hasResult, c.resultTS, c.resultLine, c.resultRow = true, ts, row.Line, row.ID
		if strings.HasPrefix(c.name, "request_user_input") {
			c.userAnswered = userInputAnswered(codexText(p.Output))
		}
	}
}

// codexText is a Codex content or output value as text: a string, or the text
// of its items.
func codexText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var items []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if item.Text != "" {
			parts = append(parts, item.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// lastLine is the last line of a text, surrounding space removed.
func lastLine(text string) string {
	text = strings.TrimSpace(text)
	return strings.TrimSpace(text[strings.LastIndexByte(text, '\n')+1:])
}
