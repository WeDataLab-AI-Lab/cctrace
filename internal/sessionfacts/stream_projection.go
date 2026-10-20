package sessionfacts

import (
	"encoding/json"
	"strings"
)

// claudeProjection contains predicates and source references only. It never
// owns an input JSON value, text body, tool input, attachment or result body.
type taskReminderShape struct{ title, redacted, summary bool }

type claudeProjection struct {
	collectVersion, imageCount, pendingAgents, pendingWorkflow int
	apiError                                                   bool
	taskID                                                     string
	reminderShapes                                             map[string]taskReminderShape
	pr                                                         map[string]string
	prefixes                                                   uint16
	redacted                                                   bool
	states                                                     [3]fieldState
	dropped                                                    [2]bool
	queued                                                     bool
	queuedOrigin                                               string
	queuedImages                                               int
	attachmentType                                             string
	agentIDs                                                   [2]string
	blocksValid                                                bool
	planPath                                                   string // final plan-file evidence, populated in pass two
}

var predicatePrefixes = []string{"[Request interrupted", "<local-command-", "<bash-stdout>", "<bash-stderr>", "<", "<bash-input>", "<skill", "<task-notification>", "<local-command-stdout>"}

func projectClaude(row ClaudeRow, copied bool, checkpoint func()) *line {
	raw := newLine(&row, copied)
	p := &claudeProjection{redacted: raw.bodyRedacted(), blocksValid: json.Valid(row.Blocks)}
	for i, prefix := range predicatePrefixes {
		if raw.textStarts(prefix) {
			p.prefixes |= 1 << i
		}
	}
	p.states = [3]fieldState{raw.state("isApiErrorMessage", row.IsAPIErrorMessage), raw.state("pendingBackgroundAgentCount", row.PendingBackgroundAgentCount), raw.state("pendingWorkflowCount", row.PendingWorkflowCount)}
	p.dropped = [2]bool{names(row.CctraceDropped, "origin"), names(row.CctraceDropped, "attachment.origin")}
	p.imageCount = jsonInt(row.ImagePasteCount)
	p.pendingAgents = jsonInt(row.PendingBackgroundAgentCount)
	p.pendingWorkflow = jsonInt(row.PendingWorkflowCount)
	p.apiError = string(row.IsAPIErrorMessage) == "true"
	_ = json.Unmarshal(row.CctraceCollect, &p.collectVersion)

	a := jsonMembers(row.Attachment)
	p.attachmentType = jsonText(a["type"])
	p.queued = p.attachmentType == "queued_command" && jsonText(a["commandMode"]) == "prompt"
	p.queuedOrigin = jsonText(jsonMembers(a["origin"])["kind"])
	p.queuedImages = jsonInt(a["imageCount"])
	r := jsonMembers(row.ToolUseResult)
	p.agentIDs = [2]string{jsonText(r["agentId"]), jsonText(r["agent_id"])}
	p.taskID = strings.Clone(taskIDFromCreate(block{}, raw))
	if p.attachmentType == "task_reminder" {
		p.reminderShapes = map[string]taskReminderShape{}
		for _, value := range jsonArray(a["tasks"]) {
			checkpoint()
			item := jsonMembers(value)
			title, summary := jsonText(item["subject"]), firstLine(jsonText(item["description"]))
			id := jsonText(item["id"])
			if _, exists := p.reminderShapes[id]; !exists {
				p.reminderShapes[id] = taskReminderShape{title != "", title == redactedMarker, summary != "" && summary != redactedMarker}
			}
		}
	}
	if pr := jsonMembers(jsonMembers(r["gitOperation"])["pr"]); len(pr) > 0 {
		p.pr = map[string]string{}
		for _, key := range []string{"action", "url", "number"} {
			p.pr[key] = jsonText(pr[key])
		}
	}
	row.Blocks = nil
	row.ToolUseResult = nil
	row.Attachment = nil
	row.TextHead = ""
	row.RawRedacted = ""
	// All JSON input columns are released. Scalar rules use the projection.
	row.CctraceCollect = nil
	row.CctraceDropped = nil
	row.CctraceWithheld = nil
	row.TurnPosition = nil
	row.ImagePasteCount = nil
	row.PendingBackgroundAgentCount = nil
	row.PendingWorkflowCount = nil
	row.IsAPIErrorMessage = nil

	l := &line{ClaudeRow: &row, copied: copied, projection: p}
	for _, b := range raw.blocks {
		checkpoint()
		e := json.RawMessage(nil)
		if len(b.Err) > 0 {
			var failed bool
			if json.Unmarshal(b.Err, &failed) != nil {
				e = []byte(`"invalid"`)
			} else if failed {
				e = []byte("true")
			} else {
				e = []byte("false")
			}
		}
		eligible, unknown := taskProjection(b, row.CWD)
		target := ""
		fallback := false
		if b.Name == "TaskCreate" {
			target = strings.Clone(taskIDFromCreate(b, &line{ClaudeRow: &ClaudeRow{}}))
			fallback = taskInputText(b, "subject", b.TaskSubject) == ""
		}
		l.blocks = append(l.blocks, block{taskTarget: target, taskFallback: fallback, taskEligible: eligible, taskUnknown: unknown, Type: strings.Clone(b.Type), ID: strings.Clone(b.ID), Name: strings.Clone(b.Name), ToolUseID: strings.Clone(b.ToolUseID), Err: e})
	}
	return l
}
func (l *line) resultAgents() [2]string {
	if l.projection != nil {
		return l.projection.agentIDs
	}
	r := jsonMembers(l.ToolUseResult)
	return [2]string{jsonText(r["agentId"]), jsonText(r["agent_id"])}
}
func (l *line) attachmentKind() string {
	if l.projection != nil {
		return l.projection.attachmentType
	}
	return jsonText(jsonMembers(l.Attachment)["type"])
}
func (l *line) planFilePath() string {
	if l.projection != nil {
		return l.projection.planPath
	}
	return jsonText(jsonMembers(l.Attachment)["planFilePath"])
}
func (l *line) blocksValid() bool {
	if l.projection != nil {
		return l.projection.blocksValid
	}
	return json.Valid(l.Blocks)
}
func (l *line) queuedInput() (bool, string, int) {
	if l.projection != nil {
		p := l.projection
		return p.queued, p.queuedOrigin, p.queuedImages
	}
	a := jsonMembers(l.Attachment)
	return jsonText(a["type"]) == "queued_command" && jsonText(a["commandMode"]) == "prompt", jsonText(jsonMembers(a["origin"])["kind"]), jsonInt(a["imageCount"])
}

type codexProjection struct{ human, command bool }

func (p *claudeProjection) starts(prefix string) bool {
	for i, v := range predicatePrefixes {
		if v == prefix {
			return p.prefixes&(1<<i) != 0
		}
	}
	return false
}
func (p *claudeProjection) fieldState(name string) fieldState {
	switch name {
	case "isApiErrorMessage":
		return p.states[0]
	case "pendingBackgroundAgentCount":
		return p.states[1]
	case "pendingWorkflowCount":
		return p.states[2]
	}
	return ""
}
func (p *claudeProjection) isDropped(name string) bool {
	switch name {
	case "origin":
		return p.dropped[0]
	case "attachment.origin":
		return p.dropped[1]
	}
	return false
}
func (l *line) collectionVersion() int {
	if l.projection != nil {
		return l.projection.collectVersion
	}
	var n int
	_ = json.Unmarshal(l.CctraceCollect, &n)
	return n
}
func (l *line) imageCount() int {
	if l.projection != nil {
		return l.projection.imageCount
	}
	return jsonInt(l.ImagePasteCount)
}
func (l *line) pendingCounts() (int, int) {
	if l.projection != nil {
		return l.projection.pendingAgents, l.projection.pendingWorkflow
	}
	return jsonInt(l.PendingBackgroundAgentCount), jsonInt(l.PendingWorkflowCount)
}
func (l *line) apiError() bool {
	if l.projection != nil {
		return l.projection.apiError
	}
	return string(l.IsAPIErrorMessage) == "true"
}
