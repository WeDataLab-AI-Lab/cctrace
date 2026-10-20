package sessionfacts

import (
	"encoding/json"
	"iter"
	"slices"
	"strconv"
	"strings"
)

func observed(n int) Signal        { return Signal{State: "observed", Value: &n} }
func unknown(reason string) Signal { return Signal{State: "undeterminable", Reason: reason} }

// readClaudeFacts computes the observable signals and compact source references;
// it does not retain free-text question, task or plan bodies.
func (c *computation) readClaudeFacts() {
	s := &c.session
	s.Detail.Signals.Turn = unknown("pre_collect")
	s.Detail.Signals.Background = unknown("pre_collect")
	s.Detail.Signals.Todo = unknown("no_list")
	s.Detail.Signals.Question = unknown("pre_collect")
	if s.TurnBoundaries == "observed" {
		s.Detail.Signals.Turn = observed(*s.Turns)
	}
	c.readBackground()
	c.readTodo()
	c.readQuestionsAndPlans()
	c.readEdits()
}

func (c *computation) readBackground() { c.readBackgroundRows(slices.Values(c.lines)) }

func (c *computation) readBackgroundRows(rows iter.Seq[*line]) {
	var last *line
	pending := make(map[string]bool)
	for l := range rows {
		c.pulse()
		if l.copied || l.IsSidechain {
			continue
		}
		if l.RecordType == "system" && l.Subtype == "turn_duration" {
			last = l
		}
		if l.RecordType == "user" {
			text := l.text()
			if strings.HasPrefix(text, "<task-notification>") {
				if at := strings.Index(text, "<task-id>"); at >= 0 {
					text = text[at+len("<task-id>"):]
					if id, _, ok := strings.Cut(text, "</task-id>"); ok {
						delete(pending, id)
					}
				}
			}
		}
		if l.RecordType == "user" {
			if id := jsonText(jsonMembers(l.ToolUseResult)["backgroundTaskId"]); id != "" {
				pending[id] = true
			}
		}
	}
	if last == nil {
		return
	}
	agent, workflow := last.state("pendingBackgroundAgentCount", last.PendingBackgroundAgentCount), last.state("pendingWorkflowCount", last.PendingWorkflowCount)
	a, w := last.pendingCounts()
	if agent == statePresent || agent == stateZero {
		c.session.Detail.SubagentsRunning = &a
	}
	if agent != statePresent && agent != stateZero {
		c.session.Detail.Signals.Background = unknown(string(agent))
		return
	}
	if workflow != statePresent && workflow != stateZero {
		c.session.Detail.Signals.Background = unknown(string(workflow))
		return
	}
	if len(pending) > 0 {
		for _, l := range c.lines {
			c.pulse()
			if l.RecordType == "user" && !l.IsSidechain && !l.copied && l.bodyRedacted() {
				c.session.Detail.Signals.Background = unknown("redacted")
				return
			}
		}
	}
	c.session.Detail.Signals.Background = observed(a + w + len(pending))
}

func (c *computation) readTodo() { c.readTodoRows(slices.Values(c.lines)) }

func (c *computation) readTodoRows(rows iter.Seq[*line]) {
	items := map[string]string{}
	seen := false
	var reason string
	created := 0
	for l := range rows {
		c.pulse()
		if l.copied || l.IsSidechain {
			continue
		}
		if l.RecordType == "attachment" {
			a := jsonMembers(l.Attachment)
			if jsonText(a["type"]) == "task_reminder" {
				state := l.state("attachment.content", a["content"])
				if state != statePresent {
					reason = string(state)
					seen = false
					continue
				}
				var entries []struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				}
				if json.Unmarshal(a["content"], &entries) != nil {
					reason = "dropped"
					seen = false
					continue
				}
				items = map[string]string{}
				for _, e := range entries {
					c.pulse()
					items[e.ID] = e.Status
				}
				seen = true
				created = 0
				reason = ""
			}
		}
		for _, b := range l.blocks {
			c.pulse()
			if b.Type != "tool_use" {
				continue
			}
			var data map[string]json.RawMessage
			_ = json.Unmarshal(b.Input, &data)
			switch b.Name {
			case "TodoWrite":
				var todo []string
				if json.Unmarshal(data["sts"], &todo) == nil && len(data["sts"]) > 0 {
					items = map[string]string{}
					for i, status := range todo {
						c.pulse()
						items[strconv.Itoa(i)] = status
					}
					seen = true
					created = 0
					reason = ""
				}
			case "TaskUpdate":
				if seen {
					if id := jsonText(data["id"]); id != "" {
						items[id] = jsonText(data["st"])
					}
				}
			case "TaskCreate":
				if seen {
					created++
				}
			}
		}
	}
	if !seen {
		if reason == "" {
			reason = "no_list"
		}
		c.session.Detail.Signals.Todo = unknown(reason)
		return
	}
	remaining := created
	for _, status := range items {
		c.pulse()
		if status != "completed" {
			remaining++
		}
	}
	c.session.Detail.Signals.Todo = observed(remaining)
}

func (c *computation) readQuestionsAndPlans() { c.readQuestionsAndPlansRows(slices.Values(c.lines)) }

func (c *computation) readQuestionsAndPlansRows(rows iter.Seq[*line]) {
	questions := map[string]*QuestionReference{}
	var order []string
	var plans []PlanReference
	planParents := make(map[string][]int)
	planPaths := make(map[int]string)
	attachmentCounts := make(map[int]int)
	for l := range rows {
		c.pulse()
		if l.copied || l.IsSidechain {
			continue
		}
		for _, b := range l.blocks {
			c.pulse()
			if b.Type == "tool_use" && b.Name == "AskUserQuestion" && b.ID != "" && l.active {
				q := &QuestionReference{ToolUseID: b.ID, SrecID: l.ID, Status: "unanswered", State: "observed"}
				var headers struct {
					Headers []string `json:"h"`
				}
				_ = json.Unmarshal(b.Input, &headers)
				q.Headers = headers.Headers
				questions[b.ID] = q
				order = append(order, b.ID)
			}
			if b.Type == "tool_use" && b.Name == "ExitPlanMode" && b.ID != "" {
				p := PlanReference{SrecID: l.ID, ToolUseID: b.ID, TS: l.TS, Decision: "pending", FilePath: jsonText(jsonMembers(b.Input)["pf"])}
				if l.turn != nil {
					p.TurnKey = l.turn.out.TurnKey
				}
				plans = append(plans, p)
				planParents[l.UUID] = append(planParents[l.UUID], len(plans)-1)
				c.session.UsedPlanMode = true
			}
			if b.Type == "tool_result" && l.active {
				if q := questions[b.ToolUseID]; q != nil {
					q.AnswerSrecID = l.ID
					if string(b.Err) == "true" {
						q.Status = "rejected"
					} else {
						q.Status = "answered"
					}
				}
				for i := range plans {
					c.pulse()
					if plans[i].ToolUseID == b.ToolUseID {
						plans[i].ResultSrecID = l.ID
						plans[i].Decision = "approved"
						if l.isInterrupt() || l.ToolDenialKind == "interrupted" {
							plans[i].Decision = "interrupted"
						} else if string(b.Err) == "true" || l.ToolDenialKind != "" {
							plans[i].Decision = "rejected"
						}
					}
				}
			}
		}
		if l.attachmentKind() == "plan_file_reference" {
			c.session.HasPlanFile = true
		}
	}
	for _, l := range c.lines {
		c.pulse()
		if l.copied || l.IsSidechain || l.attachmentKind() != "plan_file_reference" {
			continue
		}
		if candidates := planParents[l.ParentUUID]; len(candidates) == 1 {
			i := candidates[0]
			attachmentCounts[i]++
			if attachmentCounts[i] == 1 {
				planPaths[i] = plans[i].FilePath
				plans[i].FilePath = l.planFilePath()
				plans[i].AttachmentSrecID = l.ID
			} else {
				plans[i].FilePath = planPaths[i]
				plans[i].AttachmentSrecID = 0
			}
		}
	}
	c.readTurnMetadata()

	unanswered := 0
	for _, id := range order {
		c.pulse()
		q := questions[id]
		if q.Status == "unanswered" {
			unanswered++
			if l := c.toolUses[id].line; l != nil && l.turn != nil {
				c.session.OpenQuestionTurnKey = l.turn.out.TurnKey
			}
		}
		if l := c.toolUses[id].line; l != nil && l.turn != nil {
			l.turn.out.Detail.Questions = append(l.turn.out.Detail.Questions, *q)
		}
	}
	c.session.Detail.Plans = plans
	for _, p := range plans {
		c.pulse()
		for _, t := range c.turns {
			c.pulse()
			if t.out.TurnKey == p.TurnKey {
				t.out.Detail.Plans = append(t.out.Detail.Plans, TurnPlanReference{SrecID: p.SrecID, ToolUseID: p.ToolUseID, ResultSrecID: p.ResultSrecID})
			}
		}
	}
	if c.session.TurnBoundaries != "observed" {
		c.session.Detail.Signals.Question = unknown(c.session.TurnBoundariesReason)
		return
	}
	for _, l := range c.nodes {
		c.pulse()
		if l.RecordType == "assistant" && l.active && !l.copied && l.contentRedacted() {
			c.session.Detail.Signals.Question = unknown("redacted")
			return
		}
	}
	c.session.OpenQuestions = &unanswered
	c.session.Detail.Signals.Question = observed(unanswered)
}

func (c *computation) readTurnMetadata() {
	for _, t := range c.turns {
		c.pulse()
		for _, l := range t.read {
			c.pulse()
			if l.PermissionMode != "" {
				t.out.PermissionMode = l.PermissionMode
			}
			if l.ToolDenialKind != "" {
				t.out.Denied = true
			}
			if l.RecordType == "assistant" && l.MessageID != "" {
				t.out.LastAssistantMessageID = l.MessageID
			}
			for _, b := range l.blocks {
				c.pulse()
				if b.Type == "tool_use" && (b.Name == "Agent" || b.Name == "Task") && l.active {
					t.out.Subagents++
				}
			}
		}
	}
}
