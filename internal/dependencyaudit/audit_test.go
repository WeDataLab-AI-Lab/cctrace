package dependencyaudit

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var (
	auditSince = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	auditNow   = time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
)

const (
	refactorLine = "Please refactor the parser module and keep the public API stable"
	pasteOne     = "The cache layer needs a bounded eviction policy here."
	pasteTwo     = "Reads should fall back to the origin when the entry expired."
	stockLine    = "Let me know if you need anything else from me."
)

// claudeFile writes a synthetic Claude transcript: each line chains to the one
// before it, one second apart.
type claudeFile struct {
	session string
	t       time.Time
	n       int
	parent  string
	lines   []string
}

func newClaude(session string, start time.Time) *claudeFile {
	return &claudeFile{session: session, t: start}
}

func (c *claudeFile) line(typ string, fields map[string]any) {
	c.n++
	c.t = c.t.Add(time.Second)
	uuid := fmt.Sprintf("%s-%d", c.session, c.n)
	m := map[string]any{"type": typ, "uuid": uuid, "parentUuid": c.parent, "timestamp": c.t.Format(time.RFC3339), "sessionId": c.session, "cwd": "/work/alpha"}
	for k, v := range fields {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	c.lines = append(c.lines, string(b))
	c.parent = uuid
}

func (c *claudeFile) typed(text, source string) {
	c.line("user", map[string]any{"promptSource": source, "message": map[string]any{"role": "user", "content": text}})
}

func (c *claudeFile) assistant(content []map[string]any) {
	c.line("assistant", map[string]any{"message": map[string]any{"id": fmt.Sprintf("m-%s-%d", c.session, c.n+1), "role": "assistant", "model": "test-model", "content": content, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}}})
}

func (c *claudeFile) say(text string) {
	c.assistant([]map[string]any{{"type": "text", "text": text}})
}

func (c *claudeFile) tool(id, name string, input map[string]any) {
	c.assistant([]map[string]any{{"type": "tool_use", "id": id, "name": name, "input": input}})
}

func (c *claudeFile) result(id string, isError bool, fields map[string]any) {
	m := map[string]any{"message": map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": id, "is_error": isError, "content": "output"}}}}
	for k, v := range fields {
		m[k] = v
	}
	c.line("user", m)
}

func permission(decision, source string) map[string]any {
	return map[string]any{"permissionDecision": map[string]any{"decision": decision, "source": source}}
}

type codexFile struct {
	t     time.Time
	lines []string
}

func (c *codexFile) add(typ string, payload map[string]any) {
	c.t = c.t.Add(time.Second)
	b, _ := json.Marshal(map[string]any{"timestamp": c.t.Format(time.RFC3339), "type": typ, "payload": payload})
	c.lines = append(c.lines, string(b))
}

func (c *codexFile) user(text string) {
	c.add("response_item", map[string]any{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": text}}})
}

func (c *codexFile) say(text string) {
	c.add("response_item", map[string]any{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": text}}})
}

func writeLines(t *testing.T, path string, lines []string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// machineInputs are the harness-written texts that arrive as typed user lines:
// one per prefix the audit must not count as a person's words.
var machineInputs = []string{
	`<subagent_notification>{"agent":"done"}</subagent_notification>`,
	`<cross-session-message from="peer">please look</cross-session-message>`,
	`<environment_context><cwd>/work/alpha</cwd></environment_context>`,
	"# AGENTS.md instructions for /work/alpha\n\nkeep tests green",
	"<turn_aborted>The user interrupted the previous turn.</turn_aborted>",
	"<task-notification><task-id>t1</task-id><status>completed</status></task-notification>",
	"Another Claude session sent a message: please review the branch",
	`<skill name="review">run the review</skill>`,
	"<send_user_message>status update</send_user_message>",
}

type fixture struct {
	homeA, homeB, homeC string
	secret              string
}

func buildFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	fx := fixture{homeA: filepath.Join(root, "claude-a"), homeB: filepath.Join(root, "claude-b"), homeC: filepath.Join(root, "codex"), secret: secretShapes()[3]}
	day := func(d int) time.Time { return time.Date(2026, 1, d, 9, 0, 0, 0, time.UTC) }

	o := newClaude("sess-orig", day(10))
	o.typed(refactorLine, "typed")
	o.say("I'll start with the lexer.")
	o.tool("tb1", "Bash", map[string]any{"command": "cd /work/alpha && FOO=1 go test ./..."})
	o.result("tb1", false, permission("accept", "user_temporary"))
	questions := []map[string]any{
		{"question": "Q1 speed?", "multiSelect": false, "options": []map[string]any{{"label": "Fast (Recommended)"}, {"label": "Slow"}}},
		{"question": "Q2 extras?", "multiSelect": true, "options": []map[string]any{{"label": "Docs"}, {"label": "Tests (Recommended)"}, {"label": "Lint"}}},
		{"question": "Q3 name?", "multiSelect": false, "options": []map[string]any{{"label": "A"}, {"label": "B"}}},
		{"question": "Q4 targets?", "multiSelect": true, "options": []map[string]any{{"label": "X (권장)"}, {"label": "Y"}}},
	}
	o.tool("tq1", "AskUserQuestion", map[string]any{"questions": questions})
	result := map[string]any{"questions": questions, "answers": map[string]any{"Q1 speed?": "Slow", "Q2 extras?": "Docs, Tests (Recommended)", "Q3 name?": "my own words", "Q4 targets?": "Y, other thing"}, "annotations": map[string]any{"Q1 speed?": map[string]any{"notes": "because"}, "Q2 extras?": map[string]any{"preview": "shown"}}}
	fields := permission("accept", "config")
	fields["toolUseResult"] = result
	o.result("tq1", false, fields)
	o.say("Shall I continue?")
	o.typed("yes", "typed")
	copied := len(o.lines)
	o.tool("tb2", "Bash", map[string]any{"command": "rm -rf build"})
	o.result("tb2", true, map[string]any{"toolDenialKind": "user-rejected"})
	o.line("user", map[string]any{"message": map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": "[Request interrupted by user]"}}}})
	o.typed("no, use the other approach", "typed")
	o.tool("tb3", "Bash", map[string]any{"command": "go build ./..."})
	tb3 := permission("reject", "config")
	tb3["toolDenialKind"] = "permission-rule"
	o.result("tb3", true, tb3)
	o.tool("tb4", "Bash", map[string]any{"command": "ls -la"})
	tb4 := permission("reject", "user_reject")
	tb4["toolDenialKind"] = "cancelled"
	o.result("tb4", true, tb4)
	o.tool("tb5", "Read", map[string]any{"file_path": "/work/alpha/a.go"})
	o.result("tb5", false, permission("accept", "hook"))
	o.say(pasteOne + "\n" + pasteTwo + "\nDone.")
	o.line("system", map[string]any{"subtype": "compact_boundary", "content": "Conversation compacted"})
	o.line("user", map[string]any{"isCompactSummary": true, "message": map[string]any{"role": "user", "content": "This session is being continued from a previous conversation."}})
	o.typed("Restating the plan: "+strings.Repeat("keep the parser stable and the tests green. ", 8), "typed")
	o.line("assistant", map[string]any{"isApiErrorMessage": true, "error": "server_error", "message": map[string]any{"id": "m-err", "role": "assistant", "model": "<synthetic>", "content": []map[string]any{{"type": "text", "text": "API Error: 500"}}}})
	o.typed("please retry the last step", "typed")
	o.tool("tb6", "Bash", map[string]any{"command": "make test"})
	o.result("tb6", true, permission("accept", "config"))
	o.typed("it failed, look at the log", "typed")
	o.say("Looking.")
	for _, text := range machineInputs {
		o.typed(text, "typed")
		o.say("noted")
	}
	o.typed("check this <system-reminder>harness note</system-reminder> please", "typed")
	o.typed("also update the docs", "queued")
	o.typed("Run the tests", "suggestion_accepted")
	o.typed("my token is "+fx.secret, "typed")
	o.tool("tbs", "Bash", map[string]any{"command": "curl -H 'Authorization: Bearer " + fx.secret + "' localhost"})
	o.result("tbs", false, permission("accept", "user_temporary"))
	writeLines(t, filepath.Join(fx.homeA, "projects", "-work-alpha", "orig.jsonl"), o.lines, day(20))

	// A resumed copy in another home: the original's first lines under a new
	// session id, after a later first record, in a file with an older mtime.
	copyLines := []string{fmt.Sprintf(`{"type":"system","subtype":"informational","uuid":"copy-head","timestamp":%q,"sessionId":"sess-copy","content":"resumed"}`, day(11).Format(time.RFC3339))}
	for _, line := range o.lines[:copied] {
		copyLines = append(copyLines, strings.ReplaceAll(line, `"sessionId":"sess-orig"`, `"sessionId":"sess-copy"`))
	}
	writeLines(t, filepath.Join(fx.homeB, "projects", "-work-alpha", "resumed.jsonl"), copyLines, day(15))

	commandSecret := secretShapes()[2]
	for i := 1; i <= 5; i++ {
		s := newClaude(fmt.Sprintf("sess-s%d", i), day(12+i))
		first := refactorLine
		if i <= 3 {
			first += "\ndeploy with key " + fx.secret + " to the staging cluster now"
		}
		s.typed(first, "typed")
		if i <= 3 {
			s.say(stockLine)
		}
		s.say("Shall I continue?")
		s.tool(fmt.Sprintf("s%d-t1", i), "Bash", map[string]any{"command": "go test ./..."})
		s.result(fmt.Sprintf("s%d-t1", i), false, permission("accept", "config"))
		s.tool(fmt.Sprintf("s%d-t2", i), "Bash", map[string]any{"command": commandSecret + " --flag"})
		s.result(fmt.Sprintf("s%d-t2", i), false, permission("accept", "user_permanent"))
		s.typed("yes", "typed")
		if i == 4 {
			s.say("ok")
			s.typed(stockLine+"\n"+pasteTwo, "typed")
		}
		if i == 1 {
			s.tool("tq2", "AskUserQuestion", map[string]any{"questions": []map[string]any{{"question": "Q5 unanswered?", "options": []map[string]any{{"label": "A"}}}}})
		}
		writeLines(t, filepath.Join(fx.homeA, "projects", "-work-alpha", fmt.Sprintf("s%d.jsonl", i)), s.lines, day(20))
	}

	c := &codexFile{t: day(12)}
	c.add("session_meta", map[string]any{"id": "0123abcd-0000-4000-8000-000000000001", "cwd": "/work/beta", "originator": "codex-tui"})
	c.add("event_msg", map[string]any{"type": "task_started", "turn_id": "turn-1"})
	c.user("<environment_context><cwd>/work/beta</cwd></environment_context>")
	c.user("# AGENTS.md instructions for /work/beta\n<INSTRUCTIONS>rules</INSTRUCTIONS>")
	c.user(refactorLine)
	c.say("Shall I proceed?")
	c.add("response_item", map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": "c1", "input": `const r = await tools.exec_command({cmd: "go test ./..."})`})
	c.add("response_item", map[string]any{"type": "custom_tool_call_output", "call_id": "c1", "output": "ok"})
	c.add("response_item", map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": "c2", "input": "tools.exec_command({cmd: `go test ${pkg}`})"})
	c.add("response_item", map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": "c3", "input": `await tools.apply_patch("*** Begin Patch")`})
	c.add("response_item", map[string]any{"type": "function_call", "name": "request_user_input", "call_id": "r1", "arguments": `{"questions":[{"id":"q1","question":"Pick?","options":[{"label":"A"}]}]}`})
	c.add("response_item", map[string]any{"type": "function_call_output", "call_id": "r1", "output": `{"answers":{"q1":{"answers":["A"]}}}`})
	c.add("response_item", map[string]any{"type": "function_call", "name": "request_user_input", "call_id": "r2", "arguments": `{"questions":[]}`})
	c.add("response_item", map[string]any{"type": "function_call_output", "call_id": "r2", "output": ""})
	c.add("response_item", map[string]any{"type": "function_call", "name": "request_user_input_async", "call_id": "r3", "arguments": `{"questions":[]}`})
	c.add("response_item", map[string]any{"type": "function_call_output", "call_id": "r3", "output": `{"accepted":true}`})
	c.add("event_msg", map[string]any{"type": "task_complete", "turn_id": "turn-1"})
	c.add("event_msg", map[string]any{"type": "task_started", "turn_id": "turn-2"})
	c.user("go ahead")
	c.user(`<subagent_notification>{"agent":"done"}</subagent_notification>`)
	c.user(`<cross-session-message from="peer">hi</cross-session-message>`)
	c.user("<turn_aborted>The user interrupted.</turn_aborted>")
	c.add("event_msg", map[string]any{"type": "turn_aborted", "turn_id": "turn-2", "reason": "interrupted"})
	c.add("event_msg", map[string]any{"type": "task_started", "turn_id": "turn-3"})
	c.user(pasteOne + "\n" + pasteTwo)
	c.say("Will do.")
	c.add("event_msg", map[string]any{"type": "task_complete", "turn_id": "turn-3"})
	writeLines(t, filepath.Join(fx.homeC, "sessions", "2026", "01", "12", "rollout-2026-01-12T09-00-00-0123abcd-0000-4000-8000-000000000001.jsonl"), c.lines, day(20))
	return fx
}

func runFixture(t *testing.T, opts Options) (*Result, string) {
	t.Helper()
	opts.Since, opts.Now = auditSince, auditNow
	res, err := Run(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	return res, string(raw)
}

func TestAuditCountsEachSignalOnce(t *testing.T) {
	t.Parallel()
	fx := buildFixture(t)
	res, raw := runFixture(t, Options{Agent: "all", ClaudeDirs: []string{fx.homeA, fx.homeB}, CodexDirs: []string{fx.homeC}})
	_, reversed := runFixture(t, Options{Agent: "all", ClaudeDirs: []string{fx.homeB, fx.homeA}, CodexDirs: []string{fx.homeC}})
	if raw != reversed {
		t.Fatal("the result depends on the order the homes were listed in")
	}
	s, cov := res.Signals, res.Coverage

	check := func(name string, got, want int) {
		t.Helper()
		if got != want {
			t.Errorf("%s = %d, want %d", name, got, want)
		}
	}
	check("human inputs", cov.HumanInputs, 24)
	check("injected-only inputs", cov.InjectedOnly, 7)
	if want := map[string]int{"notification": 1, "command": 1, "codex_harness_text": 5}; !maps.Equal(cov.ExcludedInputs, want) {
		t.Errorf("excluded inputs = %v, want %v", cov.ExcludedInputs, want)
	}
	check("duplicates", cov.Duplicates, 4)
	check("go_ahead", s.GoAhead.Count, 7)
	check("go_ahead asked", s.GoAhead.After.Asked, 7)
	check("accepted suggestion", s.GoAhead.AcceptedSuggestion, 1)
	check("correction", s.Correction.Count, 1)
	q := s.RecommendedAnswer.Claude
	check("questions", q.Questions, 5)
	check("without recommendation", q.WithoutRecommendation, 2)
	check("recommended", q.Recommended, 1)
	check("other option", q.OtherOption, 1)
	check("free text", q.FreeText, 1)
	check("unparsed", q.Unparsed, 1)
	check("unanswered", q.Unanswered, 1)
	check("with note", q.WithNote, 1)
	cx := s.RecommendedAnswer.Codex
	check("codex requests", cx.Requests, 3)
	check("codex answered", cx.Answered, 1)
	check("codex unanswered", cx.Unanswered, 1)
	check("codex async", cx.Async, 1)
	p := s.Permission
	check("observed results", p.ObservedResults, 17)
	check("unobserved results", p.UnobservedResults, 1)
	check("config", p.Config, 8)
	check("user_temporary", p.BySource["user_temporary"], 2)
	check("user_permanent", p.BySource["user_permanent"], 5)
	check("hook", p.BySource["hook"], 1)
	check("user_reject", p.BySource["user_reject"], 1)
	check("human rejected", s.Rejected.Human["user-rejected"], 1)
	check("machine rejected", s.Rejected.Machine["permission-rule"], 1)
	check("unknown rejected", s.Rejected.Unknown["cancelled"], 1)
	check("claude interrupts", s.Interrupt.Claude, 1)
	check("codex interrupts", s.Interrupt.Codex, 1)
	check("api error handoff", s.ErrorHandoff.APIError, 1)
	check("tool error handoff", s.ErrorHandoff.ToolError, 1)
	check("after compaction", s.AfterCompaction.Count, 1)
	check("pasted output", s.PastedOutput.Count, 1)
	if s.PastedOutput.StockLineHits == 0 || len(s.PastedOutput.Pairs) != 1 || s.PastedOutput.Pairs[0] != (AgentPair{"claude", "codex", 1}) {
		t.Errorf("pasted output = %+v", s.PastedOutput)
	}
	if len(s.RepeatedLine.Lines) != 2 || s.RepeatedLine.Lines[0].Sessions != 7 || s.RepeatedLine.Lines[1].Sessions != 3 {
		t.Errorf("repeated lines = %+v", s.RepeatedLine.Lines)
	}
	cmds := s.RecurringCommand.Commands
	if len(cmds) != 2 || cmds[0].Command != "go test" || cmds[0].Sessions != 7 || len(cmds[0].Agents) != 2 || cmds[1].Sessions != 5 || s.RecurringCommand.Unparsed != 1 {
		t.Errorf("recurring commands = %+v unparsed %d", cmds, s.RecurringCommand.Unparsed)
	}
	if len(cov.Homes) != 3 {
		t.Fatalf("homes = %+v", cov.Homes)
	}
	for _, h := range cov.Homes {
		switch {
		case h.Agent == "codex" && h.Interactive != 1,
			h.Agent == "claude" && h.Files == 6 && h.Interactive != 6,
			h.Agent == "claude" && h.Files == 1 && h.Copies != 1:
			t.Errorf("home coverage = %+v", h)
		}
	}

	// The original session, not its copy, owns what both files hold.
	for _, ex := range s.GoAhead.Examples {
		if ex.SessionID == "sess-copy" {
			t.Fatalf("a copied input was attributed to the copy: %+v", ex)
		}
	}
	for _, ex := range s.Interrupt.Examples {
		if ex.Agent == "claude" && (ex.Excerpt != "no, use the other approach" || ex.Line == 0 || ex.SourceFile == "" || ex.Project != "alpha") {
			t.Errorf("interrupt example = %+v", ex)
		}
	}
	values := stringValues(t, raw)
	for _, v := range values {
		for _, machine := range []string{"please look", "keep tests green", "please review the branch", "status update", "run the review", "interrupted the previous turn", "harness note", "t1"} {
			if strings.Contains(v, machine) {
				t.Fatalf("machine-written text reached the output: %q", v)
			}
		}
		for _, secret := range secretShapes() {
			if strings.Contains(v, secret) {
				t.Fatalf("a secret reached the output")
			}
		}
	}
	if !strings.Contains(strings.Join(values, "\n"), "[REDACTED] --flag") {
		t.Error("a command key holding a secret was not masked")
	}
}

// stringValues lists every string the output serializes.
func stringValues(t *testing.T, raw string) []string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			out = append(out, x)
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			for k, e := range x {
				out = append(out, k)
				walk(e)
			}
		}
	}
	walk(v)
	return out
}

func TestAuditReadsOnlyTheSelectedAgent(t *testing.T) {
	t.Parallel()
	fx := buildFixture(t)
	res, _ := runFixture(t, Options{Agent: "claude", ClaudeDirs: []string{fx.homeA, fx.homeB}, CodexDirs: []string{fx.homeC}})
	for _, h := range res.Coverage.Homes {
		if h.Agent != "claude" {
			t.Fatalf("codex home read for --agent claude: %+v", h)
		}
	}
	if res.Signals.Interrupt.Codex != 0 || res.Signals.RecommendedAnswer.Codex.Requests != 0 || res.Signals.PastedOutput.Count != 0 {
		t.Fatalf("codex evidence counted: %+v", res.Signals.Interrupt)
	}
}

// A block pasted into the same sessions repeats every line of it. It is listed
// once, by its first-ranked line, with the number of lines it stands for, so
// the top of the list holds distinct blocks.
func TestRepeatedLineListsEachBlockOnce(t *testing.T) {
	t.Parallel()
	home := filepath.Join(t.TempDir(), "claude")
	block := []string{
		"Run the full test suite before you open the pull request.",
		"Keep the public API stable and note any breaking change.",
		"Write the commit message in the repository's usual style.",
		"Do not push until the reviewer has approved the change.",
	}
	other := "Check the deployment notes in the shared runbook first."
	for i := 1; i <= 4; i++ {
		s := newClaude(fmt.Sprintf("sess-b%d", i), time.Date(2026, 1, 10+i, 9, 0, 0, 0, time.UTC))
		first := "start the task"
		if i <= 3 {
			first = strings.Join(block, "\n")
		}
		if i >= 2 {
			first += "\n" + other
		}
		s.typed(first, "typed")
		s.say("Shall I continue?")
		s.typed("yes", "typed")
		writeLines(t, filepath.Join(home, "projects", "-work-alpha", fmt.Sprintf("b%d.jsonl", i)), s.lines, time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC))
	}
	res, _ := runFixture(t, Options{Agent: "claude", ClaudeDirs: []string{home}})
	got := map[string]int{}
	for _, l := range res.Signals.RepeatedLine.Lines {
		if l.Sessions != 3 {
			t.Errorf("line %q in %d sessions, want 3", l.Text, l.Sessions)
		}
		got[l.Text] = l.BlockLines
	}
	if len(got) != 2 || got[other] != 1 {
		t.Fatalf("repeated lines = %+v, want the block once and the other line", res.Signals.RepeatedLine.Lines)
	}
	for text, n := range got {
		if text != other && (!slices.Contains(block, text) || n != len(block)) {
			t.Errorf("block entry %q stands for %d lines, want one of the block standing for %d", text, n, len(block))
		}
	}
}

// Each home says the transmission choice of the profile that registers it, and
// a home whose profile does not allow transmission is noted as read here only.
func TestCoverageNamesEachHomesTransmission(t *testing.T) {
	t.Parallel()
	fx := buildFixture(t)
	canonical := func(path string) string {
		t.Helper()
		p, err := filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	res, _ := runFixture(t, Options{Agent: "all", ClaudeDirs: []string{fx.homeA, fx.homeB}, CodexDirs: []string{fx.homeC},
		Transmission: map[HomeKey]string{{"claude", canonical(fx.homeA)}: "allow", {"claude", canonical(fx.homeB)}: "exclude"}})
	got := map[string]string{}
	for _, h := range res.Coverage.Homes {
		got[h.Agent+" "+filepath.Base(h.Home)] = h.Transmission
	}
	want := map[string]string{"claude claude-a": "allow", "claude claude-b": "exclude", "codex codex": "unregistered"}
	if !maps.Equal(got, want) {
		t.Fatalf("transmission = %v, want %v", got, want)
	}
	if !slices.ContainsFunc(res.Caveats, func(c Caveat) bool { return c.Code == "homes_read_locally_only" }) {
		t.Fatalf("caveats = %+v", res.Caveats)
	}
	allowed, _ := runFixture(t, Options{Agent: "claude", ClaudeDirs: []string{fx.homeA}, Transmission: map[HomeKey]string{{"claude", canonical(fx.homeA)}: "allow"}})
	if slices.ContainsFunc(allowed.Caveats, func(c Caveat) bool { return c.Code == "homes_read_locally_only" }) {
		t.Fatal("an allowed home was noted as read locally only")
	}
}

// queued writes a prompt queued while the agent worked, as Claude Code records
// it: an attachment line, with the origin kind when one is given.
func (c *claudeFile) queued(prompt, origin string) {
	att := map[string]any{"type": "queued_command", "commandMode": "prompt", "prompt": prompt, "source_uuid": "src-" + prompt[:4]}
	if origin != "" {
		att["origin"] = map[string]any{"kind": origin}
	}
	c.line("attachment", map[string]any{"attachment": att})
}

// A prompt queued while the agent worked is a person's input when its origin
// is human or absent; one another session or an auto-continuation queued is
// counted apart. A resumed copy's queued prompt is counted once, and a queued
// prompt opens no request.
func TestQueuedPromptsAreHumanInputs(t *testing.T) {
	t.Parallel()
	home := filepath.Join(t.TempDir(), "claude")
	s := newClaude("sess-q", time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC))
	s.typed("Refactor the lexer and keep the tests green", "typed")
	s.tool("tq-1", "Bash", map[string]any{"command": "go test ./..."})
	s.queued("no, use the other approach", "human")
	s.queued("also update the changelog", "")
	s.queued("peer session says hello there", "peer")
	s.queued("continue with the next step", "auto-continuation")
	s.result("tq-1", false, permission("accept", "config"))
	s.say("Done.")
	writeLines(t, filepath.Join(home, "projects", "-work-alpha", "q.jsonl"), s.lines, time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC))
	copyLines := []string{fmt.Sprintf(`{"type":"system","subtype":"informational","uuid":"q-copy-head","timestamp":%q,"sessionId":"sess-q2","content":"resumed"}`, time.Date(2026, 1, 11, 0, 0, 0, 0, time.UTC).Format(time.RFC3339))}
	for _, line := range s.lines {
		copyLines = append(copyLines, strings.ReplaceAll(line, `"sessionId":"sess-q"`, `"sessionId":"sess-q2"`))
	}
	writeLines(t, filepath.Join(home, "projects", "-work-alpha", "q2.jsonl"), copyLines, time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC))

	only := newClaude("sess-only", time.Date(2026, 1, 12, 9, 0, 0, 0, time.UTC))
	only.line("user", map[string]any{"promptSource": "sdk", "message": map[string]any{"role": "user", "content": "scripted request"}})
	only.tool("to-1", "Bash", map[string]any{"command": "ls"})
	only.queued("please stop after this", "human")
	only.queued("and report back", "human")
	only.result("to-1", false, permission("accept", "config"))
	writeLines(t, filepath.Join(home, "projects", "-work-alpha", "only.jsonl"), only.lines, time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC))

	res, _ := runFixture(t, Options{Agent: "claude", ClaudeDirs: []string{home}})
	q := res.Coverage.QueuedPrompts
	if q.Human != 4 || q.Peer != 1 || q.AutoContinuation != 1 || q.Other != 0 {
		t.Fatalf("queued prompts = %+v", q)
	}
	if res.Coverage.HumanInputs != 5 || res.Signals.Correction.Count != 1 {
		t.Fatalf("human inputs = %d, corrections = %d", res.Coverage.HumanInputs, res.Signals.Correction.Count)
	}
	for _, h := range res.Coverage.Homes {
		if h.Interactive != 1 {
			t.Fatalf("home coverage = %+v; a session of queued prompts alone opened a request", h)
		}
	}
}

// A call whose result only a later file holds (the session was resumed before
// it finished) is located at that file's result line.
func TestExampleFromAResultInALaterFileNamesThatFile(t *testing.T) {
	t.Parallel()
	home := filepath.Join(t.TempDir(), "claude")
	o := newClaude("sess-m", time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC))
	o.typed("deploy the app", "typed")
	o.tool("tm1", "Bash", map[string]any{"command": "make deploy"})
	orig := filepath.Join(home, "projects", "-work-alpha", "m.jsonl")
	writeLines(t, orig, o.lines, time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC))
	copyLines := []string{fmt.Sprintf(`{"type":"system","subtype":"informational","uuid":"m-copy-head","timestamp":%q,"sessionId":"sess-m2","content":"resumed"}`, time.Date(2026, 1, 11, 0, 0, 0, 0, time.UTC).Format(time.RFC3339))}
	for _, line := range o.lines {
		copyLines = append(copyLines, strings.ReplaceAll(line, `"sessionId":"sess-m"`, `"sessionId":"sess-m2"`))
	}
	o.result("tm1", false, permission("accept", "user_temporary"))
	copyLines = append(copyLines, strings.ReplaceAll(o.lines[len(o.lines)-1], `"sessionId":"sess-m"`, `"sessionId":"sess-m2"`))
	resumed := filepath.Join(home, "projects", "-work-alpha", "m2.jsonl")
	writeLines(t, resumed, copyLines, time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC))
	resumed, _ = filepath.EvalSymlinks(resumed)
	res, _ := runFixture(t, Options{Agent: "claude", ClaudeDirs: []string{home}})
	ex := res.Signals.Permission.Examples
	if len(ex) != 1 || ex[0].SourceFile != resumed || ex[0].Line != len(copyLines) || ex[0].SessionID != "sess-m2" {
		t.Fatalf("examples = %+v, want %s:%d", ex, resumed, len(copyLines))
	}
}

// A resumed session's copy of the original's assistant lines is not another
// session saying them: an input quoting its own session's assistant is no
// paste from the copy.
func TestPastedOutputIgnoresAssistantLinesCopiedIntoAResumedFile(t *testing.T) {
	t.Parallel()
	home := filepath.Join(t.TempDir(), "claude")
	o := newClaude("sess-p", time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC))
	o.typed("design the cache layer", "typed")
	o.say(pasteOne + "\n" + pasteTwo)
	o.typed("about this:\n"+pasteOne+"\n"+pasteTwo, "typed")
	o.say("Noted.")
	writeLines(t, filepath.Join(home, "projects", "-work-alpha", "p.jsonl"), o.lines, time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC))
	copyLines := []string{fmt.Sprintf(`{"type":"system","subtype":"informational","uuid":"p-copy-head","timestamp":%q,"sessionId":"sess-p2","content":"resumed"}`, time.Date(2026, 1, 11, 0, 0, 0, 0, time.UTC).Format(time.RFC3339))}
	for _, line := range o.lines {
		copyLines = append(copyLines, strings.ReplaceAll(line, `"sessionId":"sess-p"`, `"sessionId":"sess-p2"`))
	}
	c := newClaude("sess-p2", time.Date(2026, 1, 11, 9, 0, 0, 0, time.UTC))
	c.parent = "sess-p-4"
	c.typed("now add the metrics", "typed")
	c.say("Adding.")
	c.typed("and the dashboards", "typed")
	writeLines(t, filepath.Join(home, "projects", "-work-alpha", "p2.jsonl"), append(copyLines, c.lines...), time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC))
	res, _ := runFixture(t, Options{Agent: "claude", ClaudeDirs: []string{home}})
	for _, h := range res.Coverage.Homes {
		if h.Interactive != 2 {
			t.Fatalf("home coverage = %+v, want both files interactive", h)
		}
	}
	if n := res.Signals.PastedOutput.Count; n != 0 {
		t.Fatalf("pasted output = %d, want 0: the copy is the same assistant", n)
	}
}

// A Codex turn aborted for a reason other than the person interrupting it is
// no interrupt; it is counted apart.
func TestCodexAbortsOtherThanInterruptsAreCountedApart(t *testing.T) {
	t.Parallel()
	home := filepath.Join(t.TempDir(), "codex")
	c := &codexFile{t: time.Date(2026, 1, 12, 9, 0, 0, 0, time.UTC)}
	c.add("session_meta", map[string]any{"id": "0123abcd-0000-4000-8000-000000000009", "cwd": "/work/beta", "originator": "codex-tui"})
	for i, reason := range []string{"interrupted", "replaced", ""} {
		turn := fmt.Sprintf("turn-%d", i)
		c.add("event_msg", map[string]any{"type": "task_started", "turn_id": turn})
		c.user(fmt.Sprintf("request number %d", i))
		abort := map[string]any{"type": "turn_aborted", "turn_id": turn}
		if reason != "" {
			abort["reason"] = reason
		}
		c.add("event_msg", abort)
	}
	writeLines(t, filepath.Join(home, "sessions", "2026", "01", "12", "rollout-2026-01-12T09-00-00-0123abcd-0000-4000-8000-000000000009.jsonl"), c.lines, time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC))
	res, _ := runFixture(t, Options{Agent: "codex", CodexDirs: []string{home}})
	if i := res.Signals.Interrupt; i.Codex != 1 || i.CodexOtherAborts != 2 || len(i.Examples) != 1 {
		t.Fatalf("interrupt = %+v", i)
	}
}

// The first record time is the first parsable timestamp in line order, however
// long the lines before it are.
func TestFirstRecordTimeReadsLineByLine(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	long := `{"type":"summary","summary":"` + strings.Repeat("x", 300<<10) + `"}`
	lines := []string{long, `{"type":"user","timestamp":"not a time"}`, `{"type":"user","timestamp":"2026-01-05T09:00:00Z"}`, `{"type":"user","timestamp":"2026-01-01T09:00:00Z"}`}
	writeLines(t, path, lines, time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC))
	if got := firstRecordTime(path); !got.Equal(time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("firstRecordTime = %v", got)
	}
}

// A permissionDecision that rejected is counted by its source like any other,
// but is no approval: it gets no approval example and is counted apart.
func TestRejectingPermissionDecisionIsNoApproval(t *testing.T) {
	t.Parallel()
	home := filepath.Join(t.TempDir(), "claude")
	s := newClaude("sess-r", time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC))
	s.typed("build it", "typed")
	s.tool("ra1", "Bash", map[string]any{"command": "make a"})
	s.result("ra1", false, permission("accept", "user_temporary"))
	s.tool("ra2", "Bash", map[string]any{"command": "make b"})
	s.result("ra2", true, permission("reject", "user_reject"))
	s.tool("ra3", "Read", map[string]any{"file_path": "/work/alpha/a.go"})
	s.result("ra3", true, permission("reject", "hook"))
	writeLines(t, filepath.Join(home, "projects", "-work-alpha", "r.jsonl"), s.lines, time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC))
	res, _ := runFixture(t, Options{Agent: "claude", ClaudeDirs: []string{home}})
	p := res.Signals.Permission
	if want := map[string]int{"user_temporary": 1, "user_reject": 1, "hook": 1}; !maps.Equal(p.BySource, want) {
		t.Errorf("by_source = %v, want %v", p.BySource, want)
	}
	if want := map[string]int{"user_reject": 1, "hook": 1}; !maps.Equal(p.RejectedBySource, want) {
		t.Errorf("rejected_by_source = %v, want %v", p.RejectedBySource, want)
	}
	if len(p.Examples) != 1 || p.Examples[0].Excerpt != "Bash: make a" {
		t.Errorf("approval examples = %+v", p.Examples)
	}
}
