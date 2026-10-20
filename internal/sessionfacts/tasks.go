package sessionfacts

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cctrace/internal/gitcmd"
)

type TaskFacts struct {
	State     string
	Truncated bool
	Items     []Task
}

type Task struct {
	Key, Kind, ID              string
	Title, TitleState, Summary string
	URL                        string
	Actions                    TaskActions
	FirstAt                    time.Time
	Touches                    []TaskTouch
}

type TaskActions struct {
	Added, Completed, Edited, CommitLinked bool
}

type TaskTouch struct {
	TurnKey string    `json:"turn_key"`
	Action  string    `json:"a"`
	At      time.Time `json:"ts"`
}

type taskKey struct{ file, id string }

type taskCall struct {
	line   *line
	block  block
	parsed gitcmd.Commands
	hidden bool
}

type taskResult struct {
	line   *line
	text   string
	failed bool
	known  bool
}

type taskBuilder struct {
	facts TaskFacts
	items map[string]*Task
}

var (
	taskCreateResult = regexp.MustCompile(`^Task #([0-9]+) created`)
	issuePRURL       = regexp.MustCompile(`https://github\.com/([^/\s]+)/([^/\s]+)/(issues|pull)/([0-9]+)`)
	issueNumber      = regexp.MustCompile(`(?:^|[^0-9])([0-9]+)(?:$|[^0-9])`)
	commitIssueRef   = regexp.MustCompile(`#([0-9]+)`)
)

func contentTaskID(content string) string {
	h := sha1.Sum([]byte(content))
	return hex.EncodeToString(h[:])[:6]
}

func (c *computation) readTasks(commits CommitFacts) TaskFacts {
	b := &taskBuilder{facts: TaskFacts{State: "observed"}, items: make(map[string]*Task)}
	if c.in.Agent == "codex" {
		c.readCodexTasks(b)
	} else {
		c.readClaudeTasks(b)
	}
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

func (b *taskBuilder) markUnknown() { b.facts.State = "undeterminable" }

func (b *taskBuilder) action(key, kind, id, title, summary, itemURL, action, turn string, at time.Time) {
	if key == "" || kind == "" || id == "" || action == "" {
		b.markUnknown()
		return
	}
	item := b.items[key]
	if item == nil {
		item = &Task{Key: key, Kind: kind, ID: id, TitleState: "none", FirstAt: at}
		b.items[key] = item
	} else if at.Before(item.FirstAt) {
		item.FirstAt = at
	}
	setTaskTitle(item, title)
	if summary != "" && item.Summary == "" && summary != redactedMarker {
		item.Summary = boundedTaskText(summary, 200)
	}
	if itemURL != "" && item.URL == "" {
		item.URL = itemURL
	}
	switch action {
	case "added":
		item.Actions.Added = true
	case "completed":
		item.Actions.Completed = true
	case "edited":
		item.Actions.Edited = true
	case "linked":
		item.Actions.CommitLinked = true
	default:
		b.markUnknown()
		return
	}
	for _, old := range item.Touches {
		if old.Action == action && old.TurnKey == turn && old.At.Equal(at) {
			return
		}
	}
	item.Touches = append(item.Touches, TaskTouch{TurnKey: turn, Action: action, At: at.UTC()})
	sort.Slice(item.Touches, func(i, j int) bool {
		if !item.Touches[i].At.Equal(item.Touches[j].At) {
			return item.Touches[i].At.Before(item.Touches[j].At)
		}
		return item.Touches[i].Action < item.Touches[j].Action
	})
}

func setTaskTitle(item *Task, title string) {
	if title == redactedMarker {
		item.Title, item.TitleState = "", "redacted"
		item.Summary = ""
		return
	}
	if item.TitleState == "redacted" || title == "" {
		return
	}
	item.Title, item.TitleState = boundedTaskText(title, 200), "ok"
}

func boundedTaskText(s string, limit int) string {
	if len(s) <= limit {
		return strings.Clone(s)
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.Clone(s[:cut])
}

func taskTurnForClaude(c *computation, l *line, threads map[string]*turn) string {
	if l == nil {
		return ""
	}
	if l.IsSidechain {
		if t := threads[sidechainKey(l.SourceFile, l.AgentID)]; t != nil {
			return t.out.TurnKey
		}
		return ""
	}
	if l.turn != nil {
		return l.turn.out.TurnKey
	}
	return ""
}

func toolFailed(raw json.RawMessage) (bool, bool) {
	if len(raw) == 0 {
		return false, true
	}
	var failed bool
	if err := json.Unmarshal(raw, &failed); err != nil {
		return false, false
	}
	return failed, true
}

func (c *computation) claudeTaskResults() map[taskKey][]taskResult {
	results := make(map[taskKey][]taskResult)
	for _, l := range c.lines {
		c.pulse()
		if c.copied(l.ID) || l.RecordType != "user" {
			continue
		}
		for _, block := range l.blocks {
			c.pulse()
			if block.Type != "tool_result" || block.ToolUseID == "" {
				continue
			}
			failed, known := toolFailed(block.Err)
			result := taskResult{line: l, failed: failed, known: known}
			for _, selected := range c.in.CommitResults {
				c.pulse()
				if selected.SrecID == l.ID && selected.SourceFile == l.SourceFile && selected.Call == block.ToolUseID {
					result.text = selected.Text
				}
			}
			results[taskKey{file: l.SourceFile, id: block.ToolUseID}] = append(results[taskKey{file: l.SourceFile, id: block.ToolUseID}], result)
		}
	}
	return results
}

func (c *computation) readClaudeTasks(b *taskBuilder) {
	uses := make(map[taskKey][]taskCall)
	results := c.claudeTaskResults()
	for _, l := range c.lines {
		c.pulse()
		if c.copied(l.ID) {
			continue
		}
		if l.RecordType == "assistant" && l.contentRedacted() {
			b.markUnknown()
		}
		for _, block := range l.blocks {
			c.pulse()
			if block.Type != "tool_use" || block.ID == "" {
				continue
			}
			switch block.Name {
			case "TaskCreate", "TaskUpdate", "TodoWrite":
				if block.InputHidden || jsonBool(jsonMembers(block.Input)["hid"]) {
					b.markUnknown()
					continue
				}
				key := taskKey{file: l.SourceFile, id: block.ID}
				uses[key] = append(uses[key], taskCall{line: l, block: block})
			case "Bash":
				input := jsonMembers(block.Input)
				if block.InputHidden || jsonBool(input["hid"]) {
					b.markUnknown()
					key := taskKey{file: l.SourceFile, id: block.ID}
					uses[key] = append(uses[key], taskCall{line: l, block: block, hidden: true})
					continue
				}
				var command string
				if json.Unmarshal(input["gc"], &command) != nil || command == "" || command == redactedMarker {
					continue
				}
				parsed := gitcmd.ParseWithMessages(command, l.CWD)
				unknownGitHubAPI := strings.Contains(command, "gh api")
				if parsed.Incomplete || unknownGitHubAPI {
					b.markUnknown()
				}
				if len(parsed.GitHub) == 0 && !unknownGitHubAPI {
					continue
				}
				key := taskKey{file: l.SourceFile, id: block.ID}
				uses[key] = append(uses[key], taskCall{line: l, block: block, parsed: parsed})
			}
		}
	}
	threads := c.attributeThreads()
	ordered := make([]taskCall, 0, len(uses))
	for key, calls := range uses {
		c.pulse()
		if key.file == "" || len(calls) != 1 {
			b.markUnknown()
			continue
		}
		ordered = append(ordered, calls[0])
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].line.before(ordered[j].line) })
	for _, call := range ordered {
		c.pulse()
		key := taskKey{file: call.line.SourceFile, id: call.block.ID}
		resultRows := results[key]
		if len(resultRows) != 1 {
			b.markUnknown()
			continue
		}
		result := resultRows[0]
		if result.line == nil || call.line.UserID != result.line.UserID || call.line.ProfileEmail != result.line.ProfileEmail || call.line.LoginEmail != result.line.LoginEmail || call.line.BillingProvider != result.line.BillingProvider || call.line.AccountID != result.line.AccountID || !result.known {
			b.markUnknown()
			continue
		}
		if result.failed {
			continue
		}
		turn := taskTurnForClaude(c, call.line, threads)
		at := call.line.TS
		switch call.block.Name {
		case "TaskCreate":
			id := taskIDFromCreate(call.block, result.line)
			if id == "" {
				b.markUnknown()
				continue
			}
			subject, description := taskInputText(call.block, "subject", call.block.TaskSubject), taskInputText(call.block, "description", call.block.TaskDescription)
			if subject == "" {
				subject, description = taskReminderFor(result.line, id)
			}
			b.action("task:"+id, "task", id, subject, firstLine(description), "", "added", turn, at)
		case "TaskUpdate":
			id := taskInputText(call.block, "taskId", "")
			status := taskInputText(call.block, "status", call.block.TaskStatus)
			edited := call.block.TaskEdited || jsonBool(jsonMembers(call.block.Input)["ed"])
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
			b.action("task:"+id, "task", id, taskInputText(call.block, "subject", call.block.TaskSubject), firstLine(taskInputText(call.block, "description", call.block.TaskDescription)), "", action, turn, at)
		case "TodoWrite":
			contents, statuses := todoInput(call.block)
			if contents == nil || statuses == nil || len(contents) != len(statuses) {
				b.markUnknown()
				continue
			}
			c.applyTodoWrite(b, contents, statuses, turn, at)
		case "Bash":
			if !call.hidden {
				for _, gh := range call.parsed.GitHub {
					c.pulse()
					if err := applyGitHubAction(b, c.session.RepositoryKey, gh, result.text, turn, at); err != nil {
						b.markUnknown()
					}
				}
			}
			c.applyGitOperationPR(b, c.session.RepositoryKey, result.line, result.text, turn, at)
		}
	}
	c.addSnapshotPRURLs(b)
}

func jsonBool(raw json.RawMessage) bool {
	var value bool
	_ = json.Unmarshal(raw, &value)
	return value
}

func taskInputText(block block, key, projected string) string {
	if projected != "" {
		return projected
	}
	var value string
	input := jsonMembers(block.Input)
	field := key
	switch key {
	case "subject":
		field = "sub"
	case "description":
		field = "desc"
	case "taskId":
		field = "id"
	case "status":
		field = "st"
	}
	_ = json.Unmarshal(input[field], &value)
	if value == "" && field != key {
		_ = json.Unmarshal(input[key], &value)
	}
	return value
}

func taskIDFromCreate(call block, result *line) string {
	for _, candidate := range []string{call.TaskID, jsonText(jsonMembers(call.Input)["tk"])} {
		if id := taskID(candidate); id != "" {
			return id
		}
	}
	for _, item := range result.blocks {
		if item.Type == "tool_result" {
			if id := taskID(item.TaskID); id != "" {
				return id
			}
		}
	}
	raw := jsonMembers(result.ToolUseResult)
	if task := jsonMembers(raw["task"]); len(task) > 0 {
		if id := jsonText(task["id"]); id != "" {
			return id
		}
	}
	return ""
}

func taskID(value string) string {
	if m := taskCreateResult.FindStringSubmatch(value); len(m) == 2 {
		return m[1]
	}
	if value != "" {
		allDigits := true
		for _, r := range value {
			allDigits = allDigits && r >= '0' && r <= '9'
		}
		if allDigits {
			return value
		}
	}
	return ""
}

func taskReminderFor(l *line, id string) (string, string) {
	value := jsonMembers(l.Attachment)
	if jsonText(value["type"]) != "task_reminder" {
		return "", ""
	}
	for _, raw := range jsonArray(value["tasks"]) {
		item := jsonMembers(raw)
		if jsonText(item["id"]) == id {
			return jsonText(item["subject"]), jsonText(item["description"])
		}
	}
	return "", ""
}

func jsonArray(raw json.RawMessage) []json.RawMessage {
	var values []json.RawMessage
	_ = json.Unmarshal(raw, &values)
	return values
}

func firstLine(value string) string {
	if i := strings.IndexByte(value, '\n'); i >= 0 {
		value = value[:i]
	}
	return value
}

func todoInput(block block) ([]string, []string) {
	if block.TodoContents != nil || block.TodoStatuses != nil {
		return block.TodoContents, block.TodoStatuses
	}
	input := jsonMembers(block.Input)
	var contents, statuses []string
	if len(input["tds"]) > 0 || len(input["sts"]) > 0 {
		if json.Unmarshal(input["tds"], &contents) != nil || json.Unmarshal(input["sts"], &statuses) != nil {
			return nil, nil
		}
		return contents, statuses
	}
	var todoList []struct {
		Content string `json:"content"`
		Status  string `json:"status"`
	}
	if json.Unmarshal(input["todos"], &todoList) != nil {
		return nil, nil
	}
	contents, statuses = make([]string, 0, len(todoList)), make([]string, 0, len(todoList))
	for _, item := range todoList {
		contents, statuses = append(contents, item.Content), append(statuses, item.Status)
	}
	return contents, statuses
}

func (c *computation) applyTodoWrite(b *taskBuilder, contents, statuses []string, turn string, at time.Time) {
	current := make(map[string]string, len(contents))
	for i, content := range contents {
		c.pulse()
		if content == "" {
			b.markUnknown()
			continue
		}
		current[content] = statuses[i]
	}
	previous := c.todoState
	for content, status := range current {
		c.pulse()
		id, key := contentTaskID(content), "todo:"+contentTaskID(content)
		oldStatus, existed := previous[content]
		if !existed {
			b.action(key, "task", "todo-"+id, content, "", "", "added", turn, at)
		}
		if status == "completed" && oldStatus != "completed" {
			b.action(key, "task", "todo-"+id, content, "", "", "completed", turn, at)
		} else if existed && status != oldStatus {
			b.action(key, "task", "todo-"+id, content, "", "", "edited", turn, at)
		}
	}
	for content := range previous {
		c.pulse()
		if _, ok := current[content]; !ok {
			b.action("todo:"+contentTaskID(content), "task", "todo-"+contentTaskID(content), content, "", "", "edited", turn, at)
		}
	}
	c.todoState = current
}

func applyGitHubAction(b *taskBuilder, sessionRepo string, call gitcmd.GitHubCall, resultText, turn string, at time.Time) error {
	action, ok := githubAction(call)
	if !ok {
		return nil
	}
	urlValue, repoFromURL, kindFromURL, numberFromURL := githubResultURL(resultText)
	repo := normalizeGitHubRepo(call.Repository)
	if repoFromURL != "" {
		repo = repoFromURL
	}
	kind := call.Noun
	if kindFromURL != "" {
		kind = kindFromURL
	}
	if kind != "issue" && kind != "pr" {
		return nil
	}
	number := githubNumber(call.Target)
	if numberFromURL != "" && (number == "" || call.Verb == "create") {
		number = numberFromURL
	}
	if repo == "" && strings.HasPrefix(sessionRepo, "github.com/") {
		repo = normalizeGitHubRepo(sessionRepo)
	}
	if number == "" {
		return errors.New("github action target is unknown")
	}
	display := githubDisplayID(repo, number, sessionRepo)
	key := kind + ":" + repo + "#" + number
	title := call.Title
	if title == "" && action == "added" {
		title = display
	}
	b.action(key, kind, display, title, "", urlValue, action, turn, at)
	return nil
}

func githubAction(call gitcmd.GitHubCall) (string, bool) {
	switch call.Noun {
	case "issue":
		switch call.Verb {
		case "create":
			return "added", true
		case "close":
			return "completed", true
		case "edit", "comment", "reopen":
			return "edited", true
		}
	case "pr":
		switch call.Verb {
		case "create":
			return "added", true
		case "merge":
			if !call.Auto {
				return "completed", true
			}
			return "edited", true
		case "edit", "comment", "ready", "review", "close", "reopen":
			return "edited", true
		}
	}
	return "", false
}

func githubNumber(target string) string {
	if matches := issuePRURL.FindStringSubmatch(target); len(matches) == 5 {
		return matches[4]
	}
	if matches := issueNumber.FindStringSubmatch(target); len(matches) == 2 {
		return matches[1]
	}
	return ""
}

func githubResultURL(text string) (urlValue, repo, kind, number string) {
	if matches := issuePRURL.FindStringSubmatch(text); len(matches) == 5 {
		return matches[0], "github.com/" + matches[1] + "/" + strings.TrimSuffix(matches[2], ".git"), map[string]string{"issues": "issue", "pull": "pr"}[matches[3]], matches[4]
	}
	return "", "", "", ""
}

func normalizeGitHubRepo(value string) string {
	value = strings.TrimSpace(strings.TrimSuffix(value, ".git"))
	if value == "" {
		return ""
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		value = strings.TrimPrefix(parsed.Host+parsed.Path, "/")
	}
	value = strings.TrimPrefix(value, "https://")
	value = strings.TrimPrefix(value, "http://")
	if !strings.Contains(value, "/") {
		return ""
	}
	if !strings.Contains(value, ".") {
		value = "github.com/" + value
	}
	return strings.Trim(value, "/")
}

func githubDisplayID(repo, number, sessionRepo string) string {
	if repo == "" || repo == normalizeGitHubRepo(sessionRepo) {
		return "#" + number
	}
	repo = strings.TrimPrefix(repo, "github.com/")
	return repo + "#" + number
}

func (c *computation) applyGitOperationPR(b *taskBuilder, sessionRepo string, result *line, text, turn string, at time.Time) {
	if result == nil {
		return
	}
	operation := jsonMembers(jsonMembers(result.ToolUseResult)["gitOperation"])
	pr := jsonMembers(operation["pr"])
	if len(pr) == 0 {
		return
	}
	actionName := jsonText(pr["action"])
	var action string
	switch actionName {
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
	urlValue := jsonText(pr["url"])
	if urlValue == "" {
		urlValue, _, _, _ = githubResultURL(text)
	}
	number := jsonText(pr["number"])
	if number == "" {
		_, _, _, number = githubResultURL(urlValue)
	}
	if number == "" {
		b.markUnknown()
		return
	}
	_, repo, _, _ := githubResultURL(urlValue)
	if repo == "" {
		repo = normalizeGitHubRepo(sessionRepo)
	}
	display := githubDisplayID(repo, number, sessionRepo)
	b.action("pr:"+repo+"#"+number, "pr", display, display, "", urlValue, action, turn, at)
}

func (c *computation) addSnapshotPRURLs(b *taskBuilder) {
	for _, snap := range c.in.Snapshots {
		c.pulse()
		for _, link := range snap.PRLinks {
			c.pulse()
			repo := normalizeGitHubRepo(link.Repository)
			if repo == "" {
				_, repo, _, _ = githubResultURL(link.URL)
			}
			for _, item := range b.items {
				c.pulse()
				if item.Kind == "pr" && githubKey(item.Key) == repo+"#"+jsonNumber(link.Number) && item.URL == "" {
					item.URL = link.URL
				}
			}
		}
	}
}

func jsonNumber(value int64) string {
	if value <= 0 {
		return ""
	}
	return strconv.FormatInt(value, 10)
}

func githubKey(key string) string {
	_, tail, ok := strings.Cut(key, ":")
	if !ok {
		return ""
	}
	return tail
}

func (c *computation) linkCommitTasks(b *taskBuilder, commits CommitFacts) {
	for i := range commits.Items {
		c.pulse()
		commit := &commits.Items[i]
		if commit.Subject == "" || commit.SubjectState != "ok" {
			continue
		}
		repo := normalizeGitHubRepo(commit.Repository)
		if repo == "" && strings.HasPrefix(c.session.RepositoryKey, "github.com/") {
			repo = normalizeGitHubRepo(c.session.RepositoryKey)
		}
		seen := make(map[string]bool)
		for _, match := range commitIssueRef.FindAllStringSubmatch(commit.Subject, -1) {
			c.pulse()
			number := match[1]
			kind := "issue"
			prKey := "pr:" + repo + "#" + number
			if b.items[prKey] != nil {
				kind = "pr"
			}
			key := kind + ":" + repo + "#" + number
			item := b.items[key]
			if item == nil || seen[key] {
				continue
			}
			seen[key] = true
			commit.Refs = append(commit.Refs, key)
			b.action(key, kind, item.ID, item.Title, item.Summary, item.URL, "linked", commit.TurnKey, commit.CommittedAt)
		}
		sort.Strings(commit.Refs)
	}
}

func (c *computation) readCodexTasks(b *taskBuilder) {
	if len(c.in.CodexRows) == 0 {
		return
	}
	byKey := make(map[taskKey][]*CodexRow)
	for i := range c.in.CodexRows {
		c.pulse()
		row := &c.in.CodexRows[i]
		if row.RecordType == "tool_output" && row.ToolCallID != "" && !c.copied(row.ID) {
			key := taskKey{row.SourceFile, row.ToolCallID}
			byKey[key] = append(byKey[key], row)
		}
	}
	for i := range c.in.CodexRows {
		c.pulse()
		row := &c.in.CodexRows[i]
		if row.RecordType != "tool_call" || !codexShellName(row.ToolName) || c.copied(row.ID) {
			continue
		}
		if row.SourceFile == "" || row.ToolCallID == "" {
			b.markUnknown()
			continue
		}
		parsed := gitcmd.ParseCodex(row.ToolName, row.ToolArgs, row.CWD)
		parsed.Incomplete = parsed.Incomplete || row.ToolArgsTruncated
		if parsed.Incomplete || strings.Contains(row.ToolArgs, "gh api") {
			b.markUnknown()
		}
		if len(parsed.GitHub) == 0 {
			continue
		}
		outputs := byKey[taskKey{row.SourceFile, row.ToolCallID}]
		if len(outputs) != 1 || !sameCodexIdentity(row, outputs[0]) {
			b.markUnknown()
			continue
		}
		text, status, truncated, found := codexToolOutput(c, outputs[0])
		if !found {
			b.markUnknown()
			continue
		}
		if codexOutputFailed(status, text) {
			continue
		}
		for _, call := range parsed.GitHub {
			c.pulse()
			if truncated {
				b.markUnknown()
				continue
			}
			if err := applyGitHubAction(b, c.session.RepositoryKey, call, text, codexTurnForCall(c, row), row.TS); err != nil {
				b.markUnknown()
			}
		}
	}
}

func codexShellName(name string) bool {
	switch strings.ToLower(name) {
	case "exec", "exec_command", "shell", "shell_command":
		return true
	default:
		return false
	}
}

func sameCodexIdentity(a, b *CodexRow) bool {
	return a.UserID == b.UserID && a.ProfileEmail == b.ProfileEmail && a.LoginEmail == b.LoginEmail && a.BillingProvider == b.BillingProvider && a.AccountID == b.AccountID
}

func codexOutputFailed(status, output string) bool {
	switch strings.ToLower(status) {
	case "failed", "error", "aborted":
		return true
	}
	lower := strings.ToLower(output)
	return strings.Contains(lower, "process exited with code ") && !strings.Contains(lower, "process exited with code 0")
}

func codexToolOutput(c *computation, row *CodexRow) (text, status string, truncated, found bool) {
	for _, result := range c.in.CommitResults {
		if result.SrecID != row.ID || result.SourceFile != row.SourceFile || result.Call != row.ToolCallID {
			continue
		}
		if found {
			return "", "", true, false
		}
		text, status, truncated, found = result.Text, result.Status, result.Truncated, true
	}
	if !found && row.ToolOutput != "" {
		return row.ToolOutput, row.ToolStatus, false, true
	}
	return text, status, truncated, found
}

func codexTurnForCall(c *computation, row *CodexRow) string {
	root, turnID := codexRowRootTurnID(row), codexRowTurnID(row)
	if root == "" && turnID != "" {
		root = turnID
		for _, snapshot := range c.in.Snapshots {
			for _, lifecycle := range snapshot.CodexTurns {
				if lifecycle.TurnID == turnID && lifecycle.RootTurnID != "" {
					root = lifecycle.RootTurnID
					break
				}
			}
			if root != turnID {
				break
			}
		}
	}
	if root == "" {
		root = turnID
	}
	if root != "" {
		for _, turn := range c.turns {
			if turn.out.TurnKey == root {
				return root
			}
		}
		return ""
	}
	if row.CodexTurnMetadataKnown {
		return ""
	}
	if codexTimeBoundaryAmbiguous(c.turns, row.TS) {
		c.codexTurnAttributionUnknown = true
		return ""
	}
	var found string
	for i, turn := range c.turns {
		start := turn.out.StartedAt
		end := turn.out.EndedAt
		if i+1 < len(c.turns) && (end.IsZero() || c.turns[i+1].out.StartedAt.Before(end)) {
			end = c.turns[i+1].out.StartedAt
		}
		if !row.TS.Before(start) && (end.IsZero() || row.TS.Before(end)) {
			if found != "" {
				return ""
			}
			found = turn.out.TurnKey
		}
	}
	return found
}

func sameCodexSecond(a, b time.Time) bool {
	return a.UTC().Truncate(time.Second).Equal(b.UTC().Truncate(time.Second))
}
