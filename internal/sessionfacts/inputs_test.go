package sessionfacts

import (
	"encoding/json"
	"testing"
	"time"
)

func turnOrigin(v string) opt { return func(r *ClaudeRow) { r.TurnOrigin = v } }

// sourceRows is one turn per row of the source table (p1b-design §3.9): a user
// line with the given fields and a reply, each under its own root and prompt
// id.
func sourceRows(lines ...ClaudeRow) *fixture {
	var rows []ClaudeRow
	for i, l := range lines {
		l.TS, l.PromptID = at(10*i), "P-"+l.UUID
		rows = append(rows, l, reply("a-"+l.UUID, l.UUID, 10*i+5))
	}
	return session(rows...)
}

// p1b-design §9.1, 입력·요청.
var inputCases = []tcase{
	{
		// The source is the first field that names one: the line's own tag, then
		// origin.kind, then turnOrigin, then prompt_source. A request is an
		// input a person made.
		name: "every source, in the order the fields are read",
		in: sourceRows(
			user("k01", "", 0, "text", origin("human")),
			user("k02", "", 0, "text", origin("human"), source("queued")),
			user("k03", "", 0, "the task finished", origin("task-notification")),
			user("k04", "", 0, "text", origin("peer")),
			user("k05", "", 0, "text", origin("channel")),
			user("k06", "", 0, "text", origin("auto-continuation")),
			user("k07", "", 0, "text", turnOrigin("human")),
			user("k08", "", 0, "text", turnOrigin("task_notification")),
			user("k09", "", 0, "text", turnOrigin("peer")),
			user("k10", "", 0, "text", turnOrigin("scheduled")),
			user("k11", "", 0, "text", turnOrigin("sdk")),
			user("k12", "", 0, "text", turnOrigin("system")),
			user("k13", "", 0, "text", source("typed")),
			user("k14", "", 0, "text", source("paste")),
			user("k15", "", 0, "text", source("suggestion_accepted")),
			user("k16", "", 0, "text", source("queued")),
			user("k17", "", 0, "text", source("sdk")),
			user("k18", "", 0, "text", source("system")),
			user("k19", "", 0, "text"),
			user("k20", "", 0, "<task-notification>\n<task-id>t1</task-id>", origin("human")),
			user("k21", "", 0, "<bash-input>ls</bash-input>", origin("human")),
			user("k22", "", 0, `<skill name="deploy">`, source("sdk")),
			user("k23", "", 0, "text", origin("peer"), turnOrigin("human"), source("typed")),
			user("k24", "", 0, "text", turnOrigin("scheduled"), source("typed")),
			user("k25", "", 0, "text", origin("something-new"), source("typed")),
			user("k26", "", 0, "text", origin("something-new"), source("queued")),
			user("k27", "", 0, "text", source("queued"), func(r *ClaudeRow) { r.CctraceDropped = json.RawMessage(`["origin"]`) }),
		),
		checks: checks(
			turns("key kind request",
				"k01 typed request#1",
				"k02 queued request#2",
				"k03 notification -",
				"k04 peer -",
				"k05 channel -",
				"k06 auto -",
				"k07 typed request#3",
				"k08 notification -",
				"k09 peer -",
				"k10 scheduled -",
				"k11 sdk -",
				"k12 system -",
				"k13 typed request#4",
				"k14 typed request#5",
				"k15 typed request#6",
				"k16 queued request#7",
				"k17 sdk -",
				"k18 system -",
				"k19 unknown request#8",
				"k20 notification -",
				"k21 command request#9",
				"k22 command request#10",
				"k23 peer -",
				"k24 scheduled -",
				"k25 typed request#11",
				// Queued, but from an origin that is not a person's.
				"k26 queued -",
				// Queued, with an origin the client dropped for its shape: it
				// was there, so it is not "none".
				"k27 queued -"),
			turnCounts("27/11/27/0"),
		),
	},
	{
		// One turn with every kind of input inside it. The answer to an
		// AskUserQuestion and the queued prompts are inputs; the rejected
		// question, a queued task notification and the tool results are not.
		// The two prompts a person queued are requests with no turn of their
		// own, so the session has three requests in one turn. q5's origin was
		// dropped for its shape, which is not the same as having none: it is an
		// input, but not known to be a person's.
		name: "answers and queued prompts are inputs inside a turn",
		in: session(
			typed("u1", "", 0, "P1", "do the thing", func(r *ClaudeRow) { r.ImagePasteCount = json.RawMessage(`3`) }),
			call("a1", "u1", 5, "toolu_q1", "AskUserQuestion"),
			result("r1", "a1", 10, "toolu_q1", prompt("P1")),
			call("a2", "r1", 15, "toolu_b", "Bash"),
			attachment("q1", "a2", 20, `{"type":"queued_command","commandMode":"prompt","prompt":"and the docs","origin":{"kind":"human"},"imageCount":2}`),
			attachment("q2", "q1", 21, `{"type":"queued_command","commandMode":"prompt","prompt":"from another session","origin":{"kind":"peer"}}`),
			attachment("q3", "q2", 22, `{"type":"queued_command","commandMode":"task-notification","prompt":"<task-notification>"}`),
			attachment("q4", "q3", 23, `{"type":"queued_command","commandMode":"prompt","prompt":"one more"}`),
			attachment("q5", "q4", 24, `{"type":"queued_command","commandMode":"prompt","prompt":"origin unread"}`,
				func(r *ClaudeRow) { r.CctraceDropped = json.RawMessage(`["attachment.origin"]`) }),
			result("r2", "q5", 25, "toolu_b", prompt("P1")),
			call("a3", "r2", 30, "toolu_q2", "AskUserQuestion"),
			newRow("user", "r3", "a3", 35, prompt("P1"), blocks(`[{"t":"tool_result","tu":"toolu_q2","err":true}]`)),
			reply("a4", "r3", 40),
		),
		checks: checks(
			turns("key inputs images request",
				"u1 [1:u1:typed,2:r1:answer,3:q1:queued,4:q2:peer,5:q4:queued,6:q5:queued]+2 5img request#1"),
			turnCounts("1/3/6/0"),
			segments("0..40/13/3"),
			func(t *testing.T, out Output) {
				equal(t, "extra input times", out.Turns[0].Detail.ExtraInputTS, []time.Time{at(20), at(23)})
			},
		),
	},
}

func TestInputsAndRequests(t *testing.T) { runCases(t, inputCases) }

// p1b-design §9.1, 명령.
var commandCases = []tcase{
	{
		// A command is a request when a model answered inside its turn: a custom
		// command puts the model to work, /clear and /model run locally, and a
		// synthetic line is not a model's answer. A skill the model chose marks
		// its line "implicit", which makes the line no command at all.
		name: "a command is a request only when a model answered it",
		in: session(
			command("c1", "", 0, "deploy", prompt("P1")),
			reply("a1", "c1", 5),
			command("c2", "", 10, "clear", prompt("P2")),
			command("c3", "", 20, "model", prompt("P3")),
			user("o3", "c3", 21, "<local-command-stdout>Set model</local-command-stdout>", prompt("P3")),
			command("c4", "", 30, "compact", prompt("P4")),
			user("o4", "c4", 31, "<local-command-stdout>Compacted</local-command-stdout>", prompt("P4")),
			command("c5", "", 40, "login", prompt("P5")),
			reply("a5", "c5", 45, model("<synthetic>")),
			typed("u6", "", 50, "P6", "ship it", func(r *ClaudeRow) { r.CommandName, r.CommandInvoke = "deploy", "implicit" }),
			reply("a6", "u6", 55),
		),
		checks: checks(
			turns("key kind request preview",
				`c1 command request#1 "/deploy"/ok`,
				`c2 command - "/clear"/ok`,
				`c3 command - "/model"/ok`,
				`c4 command - "/compact"/ok`,
				`c5 command - "/login"/ok`,
				`u6 typed request#2 "ship it"/ok`),
			turnCounts("6/2/6/0"),
			// Only the two requests are a person's input to the calendar.
			segments("0..55/11/2"),
		),
	},
	{
		// A row stored before command_invoke existed still has its command
		// name. Only implicit skill attribution excludes it from command turns.
		name: "a legacy command name without command_invoke is a command",
		in: session(
			user("c1", "", 0, "<command-name>/clear</command-name>\n<command-message>clear</command-message>", preP0(),
				func(r *ClaudeRow) { r.CommandName = "clear" }),
		),
		checks: checks(turns("key kind request preview status",
			`c1 command - "/clear"/ok no_response`)),
	},
	{
		name: "a legacy command remains a command under redaction and splits a shared prompt",
		in: session(
			user("c1", "", 0, "", prompt("P1"), redacted(), func(r *ClaudeRow) { r.CommandName = "model" }),
			typed("u1", "c1", 10, "P1", "next question"),
			reply("a1", "u1", 15),
		),
		checks: checks(turns("key kind request preview prompt",
			`c1 command - "/model"/ok P1(shared)`,
			`u1 typed request#1 "next question"/ok P1(shared)`)),
	},
}

func TestCommands(t *testing.T) { runCases(t, commandCases) }

// p1b-design §9.1, 비식별화: a pre-P0 session collected under a prompt policy,
// where every user line's text is the marker.
var redactionCases = []tcase{
	{
		// r1 is a tool result and x1 is typed text the client stamped no
		// prompt_source on (about one typed line in ten): neither opens a turn.
		// The line with a prompt_source does, and the command does by its
		// column.
		name: "a marker line opens a turn only by prompt_source or the command column",
		in: session(
			user("u1", "", 0, "", preP0(), source("typed"), redacted()),
			reply("a1", "u1", 5, preP0(), redacted()),
			user("r1", "a1", 10, "", preP0(), redacted()),
			reply("a2", "r1", 15, preP0(), redacted()),
			command("c1", "a2", 20, "review", preP0(), redacted()),
			reply("a3", "c1", 25, preP0(), redacted()),
			user("x1", "a3", 30, "", preP0(), redacted()),
			reply("a4", "x1", 35, preP0(), redacted()),
		),
		checks: checks(
			turns("key kind preview request status span",
				`u1 typed <redacted> request#1 undeterminable(redacted) 0..15`,
				`c1 command "/review"/ok request#2 undeterminable(redacted) 20..35`),
			sessionIs("turn boundaries", func(s Session) string { return s.TurnBoundaries + " " + s.TurnBoundariesReason }, "observed "),
			turnCounts("2/2/2/0"),
		),
	},
	{
		// The same session from a client that wrote neither column: nothing
		// tells typed text from a tool result, so there are no turns. Tokens,
		// cost and activity are computed all the same.
		name: "without either column the turn boundaries are undeterminable",
		in: session(
			user("u1", "", 0, "", preP0(), redacted()),
			reply("a1", "u1", 5, preP0(), redacted()),
			user("r1", "a1", 10, "", preP0(), redacted()),
			reply("a2", "r1", 15, preP0(), redacted()),
		).request("a1", 1, priced(0.5)).request("a2", 2, priced(0.25)),
		checks: checks(
			turns("key"),
			sessionIs("turn boundaries", func(s Session) string { return s.TurnBoundaries + " " + s.TurnBoundariesReason }, "undeterminable redacted"),
			turnCounts("null/null/null/null"),
			sessionIs("tokens", tokens, "2: 300/30/3000/15"),
			sessionIs("cost", sessionCost, "estimate=0.75 0+0.75"),
			sessionIs("activity", activity, "0..15 first record 0"),
			segments("0..15/4/0"),
		),
	},
	{
		// Re-enrichment gave some lines their promptId, but the client that
		// stored them wrote no prompt_source. The line without an id cannot be
		// read, so the session has no turns -- not even the one the id opened.
		// Its requests are attached to none, and neither is the subagent a line
		// of that turn names.
		name: "an undeterminable session keeps no turn a prompt id opened",
		in: session(
			user("u1", "", 0, "", prompt("P1"), redacted()),
			reply("a1", "u1", 5, redacted()),
			user("r1", "a1", 7, "", prompt("P1"), redacted(), func(r *ClaudeRow) { r.ToolUseResult = json.RawMessage(`{"agentId":"ag-1"}`) }),
			user("su", "", 8, "", preP0(), sidechain("ag-1"), redacted()),
			reply("sa", "su", 9, preP0(), sidechain("ag-1"), redacted()),
			user("x2", "r1", 10, "", preP0(), redacted()),
			reply("a2", "x2", 15, preP0(), redacted()),
		).request("a1", 1, priced(0.5)).request("a2", 2, priced(0.25)).request("sa", 4, priced(1)),
		checks: checks(
			turns("key"),
			sessionIs("turn boundaries", func(s Session) string { return s.TurnBoundaries + " " + s.TurnBoundariesReason }, "undeterminable redacted"),
			turnCounts("null/null/null/null"),
			sessionIs("cost", sessionCost, "estimate=1.75 0+1.75"),
			sessionIs("unattributed", func(s Session) SubagentUnattributed { return s.Detail.SubagentUnattributed },
				SubagentUnattributed{Requests: 1, Tokens: 4460}),
		),
	},
	{
		name: "redaction takes precedence over missing lineage",
		in: session(
			user("", "", 0, "", preP0(), redacted()),
			reply("", "", 5, preP0(), redacted()),
		),
		checks: checks(turns("key"), turnCounts("null/null/null/null"),
			sessionIs("turn boundaries", func(s Session) string { return s.TurnBoundaries + " " + s.TurnBoundariesReason }, "undeterminable redacted")),
	},
	{
		name: "one prompt_source in the session is enough to read its marker lines",
		in: session(
			user("u1", "", 0, "", preP0(), source("typed"), redacted()),
			user("r1", "u1", 10, "", preP0(), redacted()),
		),
		checks: checks(turns("key", "u1"), sessionIs("turn boundaries", func(s Session) string { return s.TurnBoundaries }, "observed")),
	},
	{
		name: "one command column in the session is enough too",
		in: session(
			command("c1", "", 0, "review", preP0(), redacted()),
			user("r1", "c1", 10, "", preP0(), redacted()),
		),
		checks: checks(turns("key", "c1"), sessionIs("turn boundaries", func(s Session) string { return s.TurnBoundaries }, "observed")),
	},
	{
		// "Any user line of the session" includes a subagent's: its prompt_source
		// shows the client wrote the column, so the main-thread marker line
		// without one is not typed text.
		name: "a prompt_source on a subagent line counts for the session",
		in: session(
			user("x1", "", 0, "", preP0(), redacted()),
			reply("a1", "x1", 5, preP0(), redacted()),
			user("su", "", 1, "", preP0(), sidechain("ag-1"), source("typed"), redacted()),
		),
		checks: checks(
			turns("key"),
			sessionIs("turn boundaries", func(s Session) string { return s.TurnBoundaries }, "observed"),
			turnCounts("0/0/0/0"),
		),
	},
	{
		// A raw the client could not walk is withheld whole, as {"redacted":
		// marker}. The columns still say what the line is.
		name: "a line whose raw was withheld whole",
		in: session(
			user("u1", "", 0, "", preP0(), source("typed"), func(r *ClaudeRow) { r.ContentKind, r.RawRedacted = "", marker }),
			reply("a1", "u1", 5, preP0(), func(r *ClaudeRow) { r.ContentKind, r.Blocks, r.RawRedacted = "", nil, marker }),
		),
		checks: checks(turns("key kind preview request status", "u1 typed <redacted> request#1 undeterminable(redacted)")),
	},
	{
		// A marker line that carries a promptId is placed by it: such a session
		// is not undeterminable, though a line no column explains is not an
		// input.
		name: "a marker line with a prompt id is placed by the id",
		in: session(
			user("u1", "", 0, "", prompt("P1"), redacted()),
			reply("a1", "u1", 5, redacted()),
		),
		checks: checks(
			turns("key kind preview request", "u1 unknown <none> -"),
			sessionIs("turn boundaries", func(s Session) string { return s.TurnBoundaries }, "observed"),
		),
	},
	{
		// A compact summary is the marker under either policy, and a meta line
		// under a prompt policy, but their columns say what they are: neither is
		// a line only prompt_source could have explained.
		name: "meta and summary lines never make a session undeterminable",
		in: session(
			user("s1", "", 0, "", preP0(), redacted(), func(r *ClaudeRow) { r.IsCompactSummary = true }),
			user("m1", "s1", 1, "", preP0(), redacted(), meta()),
			user("u1", "m1", 2, "plain question", preP0()),
			reply("a1", "u1", 5, preP0()),
		),
		checks: checks(
			turns("key kind request", "u1 unknown request#1"),
			sessionIs("turn boundaries", func(s Session) string { return s.TurnBoundaries }, "observed"),
		),
	},
}

func TestRedaction(t *testing.T) { runCases(t, redactionCases) }
