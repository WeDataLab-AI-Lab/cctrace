package sessionfacts

import (
	"encoding/json"
	"testing"
)

// Fields a collecting client keeps on the lines these cases need.
func apiError() opt {
	return func(r *ClaudeRow) {
		r.Model, r.IsAPIErrorMessage, r.HasError = "<synthetic>", json.RawMessage(`true`), true
	}
}

func interruptID(id string) opt { return func(r *ClaudeRow) { r.InterruptedMessageID = id } }

// p1b-design §9.1, 턴 끝과 중단. Each case is one turn, or a few.
var statusCases = []tcase{
	{
		// A turn_duration ends the turn whatever the last assistant line holds.
		name: "ended by a turn_duration",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			call("a1", "u1", 5, "toolu_1", "Bash"),
			result("r1", "a1", 10, "toolu_1", prompt("P1")),
			call("a2", "r1", 15, "toolu_2", "Bash"),
			system("t1", "a2", 16, "turn_duration"),
		),
		checks: checks(turns("key status", "u1 ended")),
	},
	{
		name: "ended by a last assistant line that calls no tool",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			call("a1", "u1", 5, "toolu_1", "Bash"),
			result("r1", "a1", 10, "toolu_1", prompt("P1")),
			reply("a2", "r1", 15),
		),
		checks: checks(turns("key status", "u1 ended")),
	},
	{
		// Claude Code writes turn_duration after an API error too, so the error
		// is read first.
		name: "an API error wins over the turn_duration after it",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			reply("a1", "u1", 5, apiError()),
			system("t1", "a1", 6, "turn_duration"),
		),
		checks: checks(turns("key status", "u1 error")),
	},
	{
		name: "an error key alone is an error",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			reply("a1", "u1", 5, func(r *ClaudeRow) { r.HasError = true }),
		),
		checks: checks(turns("key status", "u1 error")),
	},
	{
		name: "an error flag that is false is not an error",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			reply("a1", "u1", 5, func(r *ClaudeRow) { r.IsAPIErrorMessage = json.RawMessage(`false`) }),
		),
		checks: checks(turns("key status", "u1 ended")),
	},
	{
		// A redacted line with a prompt_source is typed text, so it is no
		// interrupt line: the turn simply has no reply.
		name:   "a redacted input with no reply is no response",
		in:     session(user("u1", "", 0, "", prompt("P1"), source("typed"), redacted())),
		checks: checks(turns("key status", "u1 no_response")),
	},
	{
		name: "the error flag alone is an error",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			reply("a1", "u1", 5, model("<synthetic>"), func(r *ClaudeRow) { r.IsAPIErrorMessage = json.RawMessage(`true`) }),
		),
		checks: checks(turns("key status", "u1 error")),
	},
	{
		// Only a command's turn ends at local output.
		name: "local output in a turn that is no command ends nothing",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			user("o1", "u1", 1, "<local-command-stdout>stray</local-command-stdout>", prompt("P1")),
		),
		checks: checks(turns("key status", "u1 no_response")),
	},
	{
		name: "interrupted, by the id on the interrupt line",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			call("a1", "u1", 5, "toolu_1", "Bash"),
			user("i1", "a1", 10, "[Request interrupted by user for tool use]", prompt("P1"), interruptID("msg_1")),
		),
		checks: checks(turns("key status", "u1 interrupted")),
	},
	{
		// Before P0 only the wording marks the line. It opens no turn.
		name: "interrupted, by the wording of a pre-P0 line",
		in: session(
			user("o1", "", 0, "question", preP0()),
			call("oa", "o1", 5, "toolu_1", "Bash", preP0()),
			user("oi", "oa", 10, "[Request interrupted by user]", preP0()),
		),
		checks: checks(turns("key status", "o1 interrupted")),
	},
	{
		// 111 local turns went on after an interrupt line.
		name: "an interrupt line the turn went on after does not end it",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			call("a1", "u1", 5, "toolu_1", "Bash"),
			user("i1", "a1", 10, "[Request interrupted by user for tool use]", prompt("P1"), interruptID("msg_1")),
			reply("a2", "i1", 15),
		),
		checks: checks(turns("key status", "u1 ended")),
	},
	{
		// The id survives a prompt policy; the wording does not. Without the id
		// a marker line at the end of a turn may be an interrupt or a tool
		// result.
		name: "a marker line at the end is an interrupt only by its id",
		in: session(
			user("u1", "", 0, "", prompt("P1"), source("typed"), redacted()),
			call("a1", "u1", 5, "toolu_1", "Bash", redacted()),
			user("i1", "a1", 10, "", prompt("P1"), interruptID("msg_1"), redacted()),
			user("u2", "", 100, "", prompt("P2"), source("typed"), redacted()),
			call("a2", "u2", 105, "toolu_2", "Bash", redacted()),
			user("x2", "a2", 110, "", prompt("P2"), redacted()),
		),
		checks: checks(turns("key status", "u1 interrupted", "u2 undeterminable(redacted)")),
	},
	{
		// A new promptId can first appear on the interrupt line itself. The
		// turn it opens has no input. The id marks the line, whatever it says.
		name: "a turn opened by an interrupt line",
		in: session(
			user("i1", "", 0, "Interrupted before anything was asked", prompt("P1"), interruptID("msg_1")),
		),
		checks: checks(turns("key kind request status", "i1 unknown - interrupted")),
	},
	{
		// A command that ran locally has no reply; its output is its end.
		name: "a command ends at its local output",
		in: session(
			command("c1", "", 0, "model", prompt("P1")),
			user("o1", "c1", 1, "<local-command-stdout>Set model</local-command-stdout>", prompt("P1")),
			command("c2", "", 10, "cost", prompt("P2")),
			system("l2", "c2", 11, "local_command"),
			user("b3", "", 20, "<bash-input>ls</bash-input>", prompt("P3"), origin("human")),
			user("o3", "b3", 21, "<bash-stdout>README.md</bash-stdout>", prompt("P3")),
			command("c4", "", 30, "clear", prompt("P4")),
		),
		checks: checks(turns("key kind status", "c1 command ended", "c2 command ended", "b3 command ended", "c4 command no_response")),
	},
	{
		name:   "no response",
		in:     session(typed("u1", "", 0, "P1", "question")),
		checks: checks(turns("key status", "u1 no_response")),
	},
	{
		name: "open at a tool call, and at its result",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			call("a1", "u1", 5, "toolu_1", "Bash"),
			typed("u2", "", 100, "P2", "question"),
			call("a2", "u2", 105, "toolu_2", "Bash"),
			result("r2", "a2", 110, "toolu_2", prompt("P2")),
		),
		checks: checks(turns("key status", "u1 open", "u2 open")),
	},
	{
		// The attachment and the system line after the tool result decide
		// nothing: only user and assistant lines are read for the end.
		name: "open is read on user and assistant lines only",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			call("a1", "u1", 5, "toolu_1", "Bash"),
			result("r1", "a1", 10, "toolu_1", prompt("P1")),
			attachment("h1", "r1", 11, `{"type":"task_status","taskId":"t1","status":"running"}`),
			system("s1", "h1", 12, "stop_hook_summary"),
		),
		checks: checks(turns("key status", "u1 open")),
	},
	{
		// The last assistant line calls a tool, and what follows is neither its
		// result nor an interrupt.
		name: "undeterminable when no rule decides",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			call("a1", "u1", 5, "toolu_1", "Bash"),
			user("m1", "a1", 10, "scaffolding", prompt("P1"), meta()),
		),
		checks: checks(turns("key status", "u1 undeterminable")),
	},
	{
		// A row from before the collection cannot say it was not an API error,
		// but every API error line is synthetic: a real model's line is judged
		// like any other, a synthetic one is left open.
		name: "a pre-P0 last line is undeterminable only when it is synthetic",
		in: session(
			user("o1", "", 0, "question", preP0()),
			reply("oa1", "o1", 5, preP0(), model("<synthetic>")),
			user("o2", "", 100, "question", preP0()),
			reply("oa2", "o2", 105, preP0()),
			user("o3", "", 200, "question", preP0()),
			call("oa3", "o3", 205, "toolu_1", "Bash", preP0()),
		),
		checks: checks(turns("key status", "o1 undeterminable(pre_collect)", "o2 ended", "o3 open")),
	},
	{
		// A collected synthetic line that is not an error -- a notice -- is read
		// as it is: the mark vouches for the absent field.
		name: "a collected synthetic line without the error flag is not an error",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			reply("a1", "u1", 5, model("<synthetic>")),
		),
		checks: checks(turns("key status", "u1 ended")),
	},
	{
		// The marker hides whether the last assistant line calls a tool.
		name: "a redacted last assistant line ends only by a turn_duration",
		in: session(
			user("u1", "", 0, "", prompt("P1"), source("typed"), redacted()),
			reply("a1", "u1", 5, redacted()),
			user("u2", "", 100, "", prompt("P2"), source("typed"), redacted()),
			reply("a2", "u2", 105, redacted()),
			system("t2", "a2", 106, "turn_duration"),
		),
		checks: checks(turns("key status", "u1 undeterminable(redacted)", "u2 ended")),
	},
	{
		// Claude Code writes no turn_duration after an interruption, so a turn
		// that has one ended, whatever unexplained marker line follows it.
		name: "a turn_duration ends the turn whatever marker line follows",
		in: session(
			user("u1", "", 0, "", prompt("P1"), source("typed"), redacted()),
			reply("a1", "u1", 5, redacted()),
			system("t1", "a1", 6, "turn_duration"),
			user("x1", "t1", 7, "", prompt("P1"), redacted()),
		),
		checks: checks(turns("key status", "u1 ended")),
	},
	{
		// An abandoned turn has no active path to be read on, so it is read on
		// all of its lines. h1, with no turn above it, joined u2's turn by its
		// time and is its latest line; u2 is still the turn's input.
		name: "an abandoned turn is read on all of its lines",
		in: session(
			typed("u1", "", 0, "P1", "first"),
			reply("a1", "u1", 5),
			typed("u2", "a1", 10, "P2", "rewound away from"),
			reply("a2", "u2", 15),
			attachment("h1", "never-stored", 20, `{"type":"plan_mode_exit"}`),
			typed("u3", "a1", 30, "P3", "asked instead"),
			reply("a3", "u3", 35),
		),
		checks: checks(turns("key branch kind preview status span",
			`u1 active typed "first"/ok ended 0..5`,
			`u2 abandoned typed "rewound away from"/ok ended 10..20`,
			`u3 active typed "asked instead"/ok ended 30..35`)),
	},
	{
		// r1 and a2 are a tail the person rewound away from: a2 would end the
		// turn, but the turn is read on its active path, where it is open.
		name: "an abandoned tail inside an active turn decides nothing",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			call("a1", "u1", 5, "toolu_1", "Bash"),
			result("r1", "a1", 10, "toolu_1", prompt("P1")),
			reply("a2", "r1", 15),
			result("rx", "a1", 20, "toolu_1", prompt("P1")),
		).request("a1", 1).request("a2", 2),
		checks: checks(turns("key branch status requests", "u1 active open 2req/300in")),
	},
}

func TestTurnEnd(t *testing.T) { runCases(t, statusCases) }

// p1b-design §3.2: how an absent field reads depends on the row's marks.
func TestFieldState(t *testing.T) {
	collected := json.RawMessage(`1`)
	cases := []struct {
		name  string
		row   ClaudeRow
		value string
		want  fieldState
	}{
		{"a value is present", ClaudeRow{CctraceCollect: collected}, `true`, statePresent},
		{"a value is present even when the withheld list names it", ClaudeRow{CctraceCollect: collected, CctraceWithheld: json.RawMessage(`["isApiErrorMessage"]`)}, `false`, statePresent},
		{"absent on a collected row is zero", ClaudeRow{CctraceCollect: collected}, ``, stateZero},
		{"a JSON null is absent", ClaudeRow{CctraceCollect: collected}, `null`, stateZero},
		{"absent and named as withheld", ClaudeRow{CctraceCollect: collected, CctraceWithheld: json.RawMessage(`["other","isApiErrorMessage"]`)}, ``, stateWithheld},
		{"named as dropped", ClaudeRow{CctraceCollect: collected, CctraceDropped: json.RawMessage(`["isApiErrorMessage"]`)}, ``, stateDropped},
		{"the lists name other fields", ClaudeRow{CctraceCollect: collected, CctraceDropped: json.RawMessage(`["error"]`), CctraceWithheld: json.RawMessage(`["content"]`)}, ``, stateZero},
		{"absent on a row with no mark", ClaudeRow{}, ``, statePreCollect},
		{"the marker", ClaudeRow{CctraceCollect: collected}, `"[redacted by cctrace client]"`, stateRedacted},
		{"the whole raw withheld", ClaudeRow{RawRedacted: marker}, ``, stateRedacted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newLine(&tc.row, false)
			if got := l.state("isApiErrorMessage", json.RawMessage(tc.value)); got != tc.want {
				t.Errorf("state = %q, want %q", got, tc.want)
			}
		})
	}
}

// A field the client dropped for its shape is not a field the line lacked: a
// synthetic last line whose error flag was dropped is left open, like one from
// before the collection.
func TestDroppedErrorFlagLeavesTheTurnOpenToDoubt(t *testing.T) {
	out := Compute(session(
		typed("u1", "", 0, "P1", "question"),
		reply("a1", "u1", 5, model("<synthetic>"), func(r *ClaudeRow) { r.CctraceDropped = json.RawMessage(`["isApiErrorMessage"]`) }),
	).in)
	equal(t, "turns", turnsBy(out, "key", "status"), []string{"u1 undeterminable(dropped)"})
}
