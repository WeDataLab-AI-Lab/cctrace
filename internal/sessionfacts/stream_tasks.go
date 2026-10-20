package sessionfacts

import (
	"sort"
	"strings"
	"time"

	"cctrace/internal/gitcmd"
)

type taskTextSource struct {
	result *line
	id     string
}
type pendingTaskText struct {
	key    string
	source taskTextSource
}
type codexTaskIntent struct {
	row    *CodexRow
	parsed gitcmd.Commands
	order  int
}
type githubFieldOrder struct{ title, url int }

// streamTasks keeps final task state and unresolved source references only.
// TodoWrite histories and native task inputs are never cached across Apply.
type streamTasks struct {
	projections                map[*line]*claudeProjection
	c                          *computation
	b                          *taskBuilder
	counts                     map[taskKey]int
	results                    map[taskKey][]taskResult
	threads                    map[string]*turn
	reminderNeed               map[*line]map[string]bool
	reminderValues             map[taskTextSource][2]string
	pendingText                map[*line][]pendingTaskText
	titleSource, summarySource map[string]taskTextSource
	codexResults               map[taskKey][]*CodexRow
	pendingCodex               map[*CodexRow][]codexTaskIntent
	ghOrder                    map[string]githubFieldOrder
	order                      int
}

func newStreamTasks(c *computation) *streamTasks {
	s := &streamTasks{c: c, b: &taskBuilder{facts: TaskFacts{State: "observed"}, items: map[string]*Task{}}, counts: map[taskKey]int{}, results: c.claudeTaskResults(), threads: c.attributeThreads(), reminderNeed: map[*line]map[string]bool{}, reminderValues: map[taskTextSource][2]string{}, pendingText: map[*line][]pendingTaskText{}, titleSource: map[string]taskTextSource{}, summarySource: map[string]taskTextSource{}, codexResults: map[taskKey][]*CodexRow{}, pendingCodex: map[*CodexRow][]codexTaskIntent{}, ghOrder: map[string]githubFieldOrder{}, projections: map[*line]*claudeProjection{}}
	for _, l := range c.lines {
		s.projections[l] = l.projection
		c.pulse()
		if c.copied(l.ID) {
			continue
		}
		for _, b := range l.blocks {
			if b.taskEligible {
				s.counts[taskKey{l.SourceFile, b.ID}]++
			}
		}
	}
	for _, l := range c.lines {
		c.pulse()
		if c.copied(l.ID) {
			continue
		}
		for _, b := range l.blocks {
			if !b.taskEligible || b.Name != "TaskCreate" || !b.taskFallback {
				continue
			}
			key := taskKey{l.SourceFile, b.ID}
			rs := s.results[key]
			if s.counts[key] != 1 || len(rs) != 1 {
				continue
			}
			r := rs[0]
			if key.file == "" || r.line == nil || !r.known || r.failed || l.UserID != r.line.UserID || l.ProfileEmail != r.line.ProfileEmail || l.LoginEmail != r.line.LoginEmail || l.BillingProvider != r.line.BillingProvider || l.AccountID != r.line.AccountID {
				continue
			}
			id := b.taskTarget
			if id == "" {
				id = s.projections[rs[0].line].taskID
			}
			shape := s.projections[rs[0].line].reminderShapes[id]
			if !shape.title && !shape.summary {
				continue
			}
			if s.reminderNeed[rs[0].line] == nil {
				s.reminderNeed[rs[0].line] = map[string]bool{}
			}
			s.reminderNeed[rs[0].line][id] = true
		}
	}
	for i := range c.in.CodexRows {
		c.pulse()
		r := &c.in.CodexRows[i]
		if r.RecordType == "tool_output" && r.ToolCallID != "" && !c.copied(r.ID) {
			s.codexResults[taskKey{r.SourceFile, r.ToolCallID}] = append(s.codexResults[taskKey{r.SourceFile, r.ToolCallID}], r)
		}
	}
	return s
}
func (s *streamTasks) resultText(l *line) {
	for id := range s.reminderNeed[l] {
		title, summary := taskReminderFor(l, id)
		value := [2]string{strings.Clone(boundedTaskText(title, 200)), strings.Clone(boundedTaskText(firstLine(summary), 200))}
		source := taskTextSource{l, id}
		pending := false
		for _, p := range s.pendingText[l] {
			if p.source == source {
				pending = true
				if s.titleSource[p.key] == source {
					setTaskTitle(s.b.items[p.key], value[0])
					delete(s.titleSource, p.key)
				}
				if s.summarySource[p.key] == source {
					s.b.items[p.key].Summary = value[1]
					delete(s.summarySource, p.key)
				}
			}
		}
		if !pending {
			s.reminderValues[source] = value
		}
	}
	delete(s.pendingText, l)
	delete(s.reminderNeed, l)
}
func (s *streamTasks) nativeAction(key, id, title, summary, action, turn string, at time.Time) {
	if title == redactedMarker {
		delete(s.titleSource, key)
		delete(s.summarySource, key)
	} else if title != "" {
		delete(s.titleSource, key)
	}
	if _, pending := s.summarySource[key]; pending {
		summary = ""
	}
	s.b.action(key, "task", id, title, summary, "", action, turn, at)
}
func (s *streamTasks) create(l, result *line, call block, turn string) {
	id := call.taskTarget
	if id == "" {
		id = s.projections[result].taskID
	}
	if id == "" {
		s.b.markUnknown()
		return
	}
	key := "task:" + id
	title, summary := taskInputText(call, "subject", call.TaskSubject), firstLine(taskInputText(call, "description", call.TaskDescription))
	if title != "" {
		s.nativeAction(key, id, title, summary, "added", turn, l.TS)
		return
	}
	source := taskTextSource{result, id}
	if value, ready := s.reminderValues[source]; ready {
		s.nativeAction(key, id, value[0], value[1], "added", turn, l.TS)
		delete(s.reminderValues, source)
		return
	}
	shape := s.projections[result].reminderShapes[id]
	if !shape.title && !shape.summary {
		s.nativeAction(key, id, "", "", "added", turn, l.TS)
		return
	}
	if shape.redacted {
		title = redactedMarker
	}
	s.nativeAction(key, id, title, "", "added", turn, l.TS)
	item := s.b.items[key]
	if shape.title && !shape.redacted && item.TitleState != "redacted" {
		s.titleSource[key] = source
	}
	if shape.summary && item.Summary == "" {
		if _, pending := s.summarySource[key]; !pending {
			s.summarySource[key] = source
		}
	}
	s.pendingText[result] = append(s.pendingText[result], pendingTaskText{key, source})
}
func (s *streamTasks) applyClaude(l *line, indexed []block) {
	s.resultText(l)
	c, b := s.c, s.b
	if c.copied(l.ID) {
		return
	}
	if l.RecordType == "assistant" && l.contentRedacted() {
		b.markUnknown()
	}
	for i, call := range l.blocks {
		c.pulse()
		if call.Type != "tool_use" || call.ID == "" {
			continue
		}
		p := indexed[i]
		if p.taskUnknown {
			b.markUnknown()
		}
		if !p.taskEligible {
			continue
		}
		key := taskKey{l.SourceFile, call.ID}
		if key.file == "" || s.counts[key] != 1 {
			b.markUnknown()
			continue
		}
		rs := s.results[key]
		if len(rs) != 1 {
			b.markUnknown()
			continue
		}
		r := rs[0]
		if r.line == nil || l.UserID != r.line.UserID || l.ProfileEmail != r.line.ProfileEmail || l.LoginEmail != r.line.LoginEmail || l.BillingProvider != r.line.BillingProvider || l.AccountID != r.line.AccountID || !r.known {
			b.markUnknown()
			continue
		}
		if r.failed {
			continue
		}
		turn := taskTurnForClaude(c, l, s.threads)
		switch call.Name {
		case "TodoWrite":
			contents, statuses := todoInput(call)
			if contents == nil || statuses == nil || len(contents) != len(statuses) {
				b.markUnknown()
				continue
			}
			c.applyTodoWrite(b, contents, statuses, turn, l.TS)
		case "TaskCreate":
			call.taskTarget = p.taskTarget
			s.create(l, r.line, call, turn)
		case "TaskUpdate":
			id := taskInputText(call, "taskId", "")
			status := taskInputText(call, "status", call.TaskStatus)
			edited := call.TaskEdited || jsonBool(jsonMembers(call.Input)["ed"])
			if id == "" {
				b.markUnknown()
				continue
			}
			action := ""
			if status == "completed" {
				action = "completed"
			} else if status != "" || edited {
				action = "edited"
			}
			if action == "" {
				b.markUnknown()
				continue
			}
			s.nativeAction("task:"+id, id, taskInputText(call, "subject", call.TaskSubject), firstLine(taskInputText(call, "description", call.TaskDescription)), action, turn, l.TS)
		case "Bash":
			s.order++
			input := jsonMembers(call.Input)
			hidden := call.InputHidden || jsonBool(input["hid"])
			url := ""
			selected := c.stream.selected[resultKey{r.line.ID, l.SourceFile, call.ID}]
			if len(selected) > 0 {
				url = selected[len(selected)-1].URL
			}
			if !hidden {
				parsed := gitcmd.ParseWithMessages(jsonText(input["gc"]), l.CWD)
				for _, gh := range parsed.GitHub {
					s.github(gh, url, turn, l.TS, s.order)
				}
			}
			s.applyPR(s.projections[r.line].pr, url, turn, l.TS, s.order)
		}
	}
}
func (s *streamTasks) github(call gitcmd.GitHubCall, url, turn string, at time.Time, order int) {
	tmp := &taskBuilder{facts: TaskFacts{State: "observed"}, items: map[string]*Task{}}
	if applyGitHubAction(tmp, s.c.session.RepositoryKey, call, url, turn, at) != nil {
		s.b.markUnknown()
		return
	}
	for _, item := range tmp.items {
		for _, touch := range item.Touches {
			title := item.Title
			if item.TitleState == "redacted" {
				title = redactedMarker
			}
			s.githubAction(item.Key, item.Kind, item.ID, title, item.URL, touch.Action, touch.TurnKey, touch.At, order)
		}
	}
}
func (s *streamTasks) githubAction(key, kind, id, title, url, action, turn string, at time.Time, order int) {
	rank, exists := s.ghOrder[key]
	if title != "" {
		if !exists || order >= rank.title {
			rank.title = order
		} else if title != redactedMarker {
			title = ""
		}
	}
	if url != "" {
		if !exists || rank.url == 0 || order < rank.url {
			rank.url = order
			if old := s.b.items[key]; old != nil {
				old.URL = ""
			}
		} else {
			url = ""
		}
	}
	s.ghOrder[key] = rank
	s.b.action(key, kind, id, title, "", url, action, turn, at)
}
func (s *streamTasks) applyCodex(compact, raw *CodexRow) {
	c, b := s.c, s.b
	if raw.RecordType == "tool_output" {
		for _, intent := range s.pendingCodex[compact] {
			s.codexAction(intent, compact)
		}
		delete(s.pendingCodex, compact)
		return
	}
	if raw.RecordType != "tool_call" || !codexShellName(raw.ToolName) || c.copied(raw.ID) {
		return
	}
	if raw.SourceFile == "" || raw.ToolCallID == "" {
		b.markUnknown()
		return
	}
	e := c.stream.codexCommands[compact]
	if e.parsed.Incomplete || e.unknownAPI {
		b.markUnknown()
	}
	if len(e.parsed.GitHub) == 0 {
		return
	}
	outputs := s.codexResults[taskKey{raw.SourceFile, raw.ToolCallID}]
	if len(outputs) != 1 || !sameCodexIdentity(raw, outputs[0]) {
		b.markUnknown()
		return
	}
	order := int(c.stream.codex[c.stream.next].ref.Ordinal) + 1
	intent := codexTaskIntent{compact, gitcmd.Commands{GitHub: e.parsed.GitHub}, order}
	if _, found := c.codexResult(outputs[0]); found {
		s.codexAction(intent, outputs[0])
	} else {
		s.pendingCodex[outputs[0]] = append(s.pendingCodex[outputs[0]], intent)
	}
}
func (s *streamTasks) codexAction(intent codexTaskIntent, result *CodexRow) {
	e, found := s.c.codexResult(result)
	if !found {
		s.b.markUnknown()
		return
	}
	if e.Failed {
		return
	}
	for _, gh := range intent.parsed.GitHub {
		if e.Truncated {
			s.b.markUnknown()
			continue
		}
		s.github(gh, e.URL, codexTurnForCall(s.c, intent.row), intent.row.TS, intent.order)
	}
}
func (s *streamTasks) finish(commits CommitFacts) TaskFacts {
	c, b := s.c, s.b
	if len(s.pendingCodex) > 0 {
		b.markUnknown()
	}
	c.addSnapshotPRURLs(b)
	c.linkCommitTasks(b, commits)
	for _, item := range b.items {
		c.pulse()
		b.facts.Items = append(b.facts.Items, *item)
	}
	sort.Slice(b.facts.Items, func(i, j int) bool {
		a, d := b.facts.Items[i], b.facts.Items[j]
		if !a.FirstAt.Equal(d.FirstAt) {
			return a.FirstAt.Before(d.FirstAt)
		}
		return a.Key < d.Key
	})
	if len(b.facts.Items) > 200 {
		b.facts.Items = b.facts.Items[:200]
		b.facts.Truncated = true
		b.facts.State = "undeterminable"
	}
	for i := range b.facts.Items {
		c.pulse()
		if len(b.facts.Items[i].Touches) > 50 {
			b.facts.Items[i].Touches = b.facts.Items[i].Touches[:50]
			b.facts.Truncated = true
			b.facts.State = "undeterminable"
		}
	}
	return b.facts
}
func (s *streamTasks) applyPR(pr map[string]string, text, turn string, at time.Time, order int) {
	if len(pr) == 0 {
		return
	}
	b, c := s.b, s.c
	action := ""
	switch pr["action"] {
	case "created":
		action = "added"
	case "merged":
		action = "completed"
	case "commented", "closed", "edited", "ready":
		action = "edited"
	default:
		b.markUnknown()
		return
	}
	url := pr["url"]
	if url == "" {
		url, _, _, _ = githubResultURL(text)
	}
	number := pr["number"]
	if number == "" {
		_, _, _, number = githubResultURL(url)
	}
	if number == "" {
		b.markUnknown()
		return
	}
	_, repo, _, _ := githubResultURL(url)
	if repo == "" {
		repo = normalizeGitHubRepo(c.session.RepositoryKey)
	}
	display := githubDisplayID(repo, number, c.session.RepositoryKey)
	s.githubAction("pr:"+repo+"#"+number, "pr", display, display, url, action, turn, at, order)
}
func taskProjection(b block, cwd string) (eligible, unknown bool) {
	if b.Type != "tool_use" || b.ID == "" {
		return
	}
	switch b.Name {
	case "TaskCreate", "TaskUpdate", "TodoWrite":
		if b.InputHidden || jsonBool(jsonMembers(b.Input)["hid"]) {
			return false, true
		}
		return true, false
	case "Bash":
		input := jsonMembers(b.Input)
		if b.InputHidden || jsonBool(input["hid"]) {
			return true, true
		}
		command := jsonText(input["gc"])
		if command == "" || command == redactedMarker {
			return false, false
		}
		parsed := gitcmd.ParseWithMessages(command, cwd)
		api := strings.Contains(command, "gh api")
		return len(parsed.GitHub) > 0 || api, parsed.Incomplete || api
	}
	return
}
