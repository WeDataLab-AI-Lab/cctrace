package sessionfacts

import "sort"

// input is one input of a turn (p1b-design §3.9): the turn's input line, the
// answer to an AskUserQuestion, or a prompt queued while the agent was busy.
type input struct {
	line   *line
	kind   string
	images int
	// extra says the input is a queued prompt a person wrote: a request with no
	// turn of its own.
	extra bool
	n     int
}

// state decides how a turn ended, reading its user and assistant lines from the
// last one back; the first rule that holds wins (p1b-design §3.6). Attachment
// and system lines are read only for a turn_duration and a local command's
// output.
//
// An error comes first because Claude Code still writes turn_duration after
// one (75 local turns). An interruption needs the interrupt line to be the end
// of the turn: 111 local turns went on after one. The turn's source is read by
// now (readInputs), which is how a command's turn is known.
func (t *turn) state() (status, reason string) {
	var lastAssistant, lastUser, lastSpoken *line
	var answered, turnDuration, localOutput bool
	for _, l := range t.read {
		if t.checkpoint != nil {
			t.checkpoint()
		}
		switch l.RecordType {
		case "assistant":
			lastAssistant, lastSpoken, answered = l, l, true
		case "user":
			lastUser, lastSpoken, answered = l, l, false
			localOutput = localOutput || l.textStarts("<local-command-stdout>", "<bash-stdout>")
		case "system":
			turnDuration = turnDuration || l.Subtype == "turn_duration"
			localOutput = localOutput || l.Subtype == "local_command"
		}
	}
	t.lastAssistant = lastAssistant

	// 1. error. isApiErrorMessage and error are collected fields, so a row from
	// before the collection cannot say it was not one -- but every API error
	// line is synthetic (275 of 275 locally), so only a synthetic last line
	// leaves the question open.
	if a := lastAssistant; a != nil {
		flag := a.state("isApiErrorMessage", a.IsAPIErrorMessage)
		switch {
		case a.HasError, flag == statePresent && a.apiError():
			return "error", ""
		case flag != statePresent && flag != stateZero && a.Model == syntheticModel:
			return "undeterminable", string(flag)
		}
	}

	// 2. interrupted. A last user line whose text is the marker and that no
	// column explains may be an interrupt line or a tool result: the wording
	// that would tell is gone.
	unreadable := false
	if u := lastUser; u != nil && !answered {
		if u.isInterrupt() {
			return "interrupted", ""
		}
		unreadable = u.columnJudged() && u.PromptSource == ""
	}

	// 3. ended. A command that only ran locally has no assistant line and no
	// turn_duration; its output is its end (37 of the 53 local last turns with
	// no response).
	if turnDuration || (t.out.InputKind == "command" && localOutput) {
		return "ended", ""
	}
	if unreadable {
		return "undeterminable", "redacted"
	}
	if a := lastAssistant; a != nil {
		if a.contentRedacted() {
			return "undeterminable", "redacted"
		}
		if !a.has("tool_use") {
			return "ended", ""
		}
	}

	// 4. no response.
	if lastAssistant == nil {
		return "no_response", ""
	}

	// 5. open: the turn stops at a tool call or at its result. The last
	// assistant line holds a tool_use by now, so it is open when that line is
	// the last thing said.
	if lastSpoken == lastAssistant || lastSpoken.has("tool_result") {
		return "open", ""
	}
	return "undeterminable", ""
}

// readInputs finds a turn's inputs on the lines it is read on (p1b-design
// §3.9) and fills the columns that come from its input line.
//
// The input line is the first of the turn's lines that is an input
// (line.isInput). That is the opening line unless it put nothing to the agent:
// a meta or summary line, a tool result, an interrupt line, a command's output,
// or a marker line no prompt_source explains. A turn opened by a notification,
// another session or a schedule has one all the same. Copies are not inputs,
// but a copied input line still names the turn's source and preview.
func (c *computation) readInputs(t *turn) {
	for _, l := range t.read {
		if t.checkpoint != nil {
			t.checkpoint()
		}
		c.pulse()
		switch {
		case l.RecordType == "user" && t.input == nil && l.isInput():
			t.input = l
			if !l.copied {
				t.inputs = append(t.inputs, &input{line: l, kind: l.inputKind(), images: l.imageCount()})
			}
		case l.copied:
		case l.RecordType == "user" && c.answersQuestion(l):
			t.inputs = append(t.inputs, &input{line: l, kind: "answer"})
		case l.RecordType == "attachment":
			queued, origin, images := l.queuedInput()
			if !queued {
				continue
			}
			// origin.kind is all the client keeps of a queued prompt's origin.
			in := &input{line: l, kind: "queued", images: images}
			switch {
			case origin == "peer":
				in.kind = "peer"
			case l.personsOrigin(origin, "attachment.origin"):
				in.extra = true
			}
			t.inputs = append(t.inputs, in)
		}
	}

	t.out.InputKind, t.out.PreviewState = "unknown", "none"
	if t.input != nil {
		t.out.InputKind = t.input.inputKind()
		t.out.Preview, t.out.PreviewState = t.input.preview()
	}
	for _, in := range t.inputs {
		c.pulse()
		t.out.ImageCount += in.images
	}
}

// answersQuestion reports whether a user line carries the answer to an
// AskUserQuestion: a tool_result for that tool that is not an error. A
// rejection is an error result and is no input.
func (c *computation) answersQuestion(l *line) bool {
	for _, b := range l.blocks {
		c.pulse()
		if b.Type == "tool_result" && string(b.Err) != "true" && c.toolUses[b.ToolUseID].name == "AskUserQuestion" {
			return true
		}
	}
	return false
}

// isRequest reports whether a turn is a request (p1b-design §3.9): an active
// turn whose input a person made. A command is one only when a model answered
// inside the turn -- custom commands and skills put the model to work, /clear
// and /model do not (244 of 541 local command lines had a real response) --
// and since rule 1-b moved the next human text out, that reply is the
// command's own.
func (t *turn) isRequest() bool {
	if t.out.Branch != "active" || t.out.OpenerCopied || t.input == nil {
		return false
	}
	switch t.out.InputKind {
	case "typed", "unknown":
		return true
	case "queued":
		return t.input.personsOrigin(t.input.OriginKind, "origin")
	case "command":
		for _, l := range t.read {
			if t.checkpoint != nil {
				t.checkpoint()
			}
			if l.RecordType == "assistant" && l.Model != syntheticModel {
				return true
			}
		}
	}
	return false
}

// numberInputs numbers the inputs of the active turns in (ts, uuid) order
// across the session and writes each turn's detail. An abandoned turn keeps
// its source and preview but lists no input: inputs are on the active path.
func (c *computation) numberInputs() int {
	var all []*input
	for _, t := range c.turns {
		c.pulse()
		if t.out.Branch == "active" {
			all = append(all, t.inputs...)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].line.before(all[j].line) })
	for i, in := range all {
		c.pulse()
		in.n = i + 1
	}
	for _, t := range c.turns {
		c.pulse()
		if t.out.Branch != "active" {
			continue
		}
		for _, in := range t.inputs {
			c.pulse()
			t.out.Detail.Inputs = append(t.out.Detail.Inputs, TurnInput{UUID: in.line.UUID, TS: in.line.TS, Kind: in.kind, N: in.n})
			if in.extra {
				t.out.Detail.ExtraInputs++
				t.out.Detail.ExtraInputTS = append(t.out.Detail.ExtraInputTS, in.line.TS)
				in.line.human = true
			}
		}
		if t.out.IsRequest {
			t.input.human = true
		}
	}
	return len(all)
}
