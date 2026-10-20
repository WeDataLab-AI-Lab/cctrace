package sessionfacts

import (
	"encoding/json"
	"testing"
)

// p1b-design §9.1, 턴 경계 1-b.
var splitCases = []tcase{
	{
		// /model runs locally; what the person types a minute and a half later
		// carries the same promptId. It is a turn of its own, with its own
		// preview, and the command's turn ends at its output.
		name: "typed text after a command of the same prompt id opens a turn",
		in: session(
			command("c1", "", 0, "model", prompt("P1")),
			user("o1", "c1", 1, "<local-command-stdout>Set model to opus</local-command-stdout>", prompt("P1")),
			typed("u1", "o1", 90, "P1", "now fix the bug"),
			reply("a1", "u1", 95),
		),
		checks: checks(
			turns("key prompt kind request status preview",
				`c1 P1(shared) command - ended "/model"/ok`,
				`u1 P1(shared) typed request#1 ended "now fix the bug"/ok`),
			turnCounts("2/1/2/0"),
		),
	},
	{
		// The command put the model to work before the next input arrived, so
		// the command's turn is a request too, and its reply stays its own.
		name: "a queued input after a command the model answered",
		in: session(
			command("c1", "", 0, "review", prompt("P1")),
			reply("a0", "c1", 5),
			user("u1", "a0", 20, "also check the tests", prompt("P1"), origin("human"), source("queued")),
			reply("a1", "u1", 25),
		),
		checks: checks(
			turns("key kind request span", "c1 command request#1 0..5", "u1 queued request#2 20..25"),
			turnCounts("2/2/2/0"),
		),
	},
	{
		// The line order /compact really writes (A-17): the summary, the
		// command's caveat, the command, its output -- then the next input
		// under the same promptId, and the reply to it.
		name: "compact followed by the next input",
		in: session(
			user("s1", "", 0, "This session is being continued from a previous conversation", prompt("P1"), func(r *ClaudeRow) { r.IsCompactSummary = true }),
			user("m1", "s1", 1, "<local-command-caveat>Caveat: the messages below</local-command-caveat>", prompt("P1"), meta()),
			command("c1", "m1", 2, "compact", prompt("P1")),
			user("o1", "c1", 3, "<local-command-stdout>Compacted</local-command-stdout>", prompt("P1")),
			typed("u1", "o1", 60, "P1", "continue with step two"),
			reply("a1", "u1", 65),
		),
		checks: checks(
			turns("key opener kind request preview status",
				`s1 s1 command - "/compact"/ok ended`,
				`u1 u1 typed request#1 "continue with step two"/ok ended`),
			turnCounts("2/1/2/0"),
		),
	},
	{
		// Under a prompt policy the text is the marker on every line. The
		// command is known by its column; of the two lines after it only the one
		// with a prompt_source is typed text. The other may be the command's
		// output or anything else, and the command's turn cannot say how it
		// ended.
		name: "a marker line after a command is typed text only with a prompt_source",
		in: session(
			command("c1", "", 0, "model", prompt("P1"), redacted()),
			user("x1", "c1", 1, "", prompt("P1"), redacted()),
			user("u1", "x1", 50, "", prompt("P1"), source("typed"), redacted()),
			reply("a1", "u1", 55, redacted()),
			system("t1", "a1", 56, "turn_duration"),
		),
		checks: checks(
			turns("key kind request preview status",
				`c1 command - "/model"/ok undeterminable(redacted)`,
				`u1 typed request#1 <redacted> ended`),
		),
	},
	{
		// After the split, P1 belongs to its newest turn: the tool result that
		// comes back after P2's turn joins u1's, and so does more text under
		// P1, which opens nothing now that the command is behind.
		name: "after a split the prompt id returns to its newest turn",
		in: session(
			command("c1", "", 0, "review", prompt("P1")),
			call("a0", "c1", 5, "toolu_1", "Bash"),
			typed("u1", "a0", 10, "P1", "also do this"),
			typed("u2", "u1", 20, "P2", "unrelated"),
			reply("a2", "u2", 25),
			result("r1", "a2", 30, "toolu_1", prompt("P1")),
			reply("a3", "r1", 35),
			typed("u3", "a3", 40, "P1", "and this"),
		),
		checks: checks(turns("key prompt span preview inputs",
			`c1 P1(shared) 0..5 "/review"/ok [1:c1:command]+0`,
			`u1 P1(shared) 10..40 "also do this"/ok [2:u1:typed]+0`,
			`u2 P2 20..25 "unrelated"/ok [3:u2:typed]+0`)),
	},
	{
		// A command is not human text, however its text reads: a second command
		// under the same prompt id joins the first one's turn.
		name: "a redacted command after a command does not open a turn",
		in: session(
			command("c1", "", 0, "model", prompt("P1"), redacted()),
			command("c2", "c1", 10, "cost", prompt("P1"), source("typed"), redacted()),
		),
		checks: checks(turns("key span", "c1 0..10")),
	},
	{
		// Rule 1-b is about what a person types. A notification arriving under
		// the command's prompt id is an input, but it is not human text: it
		// joins the command's turn.
		name: "a tagged line after a command does not open a turn",
		in: session(
			command("c1", "", 0, "review", prompt("P1")),
			user("n1", "c1", 10, "<task-notification>\n<task-id>t1</task-id>", prompt("P1"), origin("task-notification")),
			reply("a1", "n1", 15),
		),
		checks: checks(turns("key kind span", "c1 command 0..15")),
	},
}

func TestCommandSplit(t *testing.T) { runCases(t, splitCases) }

// p1b-design §9.1, 공유 prompt_id(N1).
var sharedPromptCases = []tcase{
	{
		// The command's turn and the input's turn share P, and the events under
		// P cost 1 and 2. Neither turn takes them: they are counted once, with
		// the event that has no prompt id and the one whose id no turn has. Q
		// belongs to one turn and pays it.
		//
		// Four subagents: ag-1 and ag-2 start with P, which cannot place them,
		// so ag-1 goes by its tool_use id to the turn holding that call and
		// ag-2, with no such id, by the agent id a tool result names. ag-3 has
		// neither and is unattributed. ag-4 starts with Q.
		name: "a prompt id two turns share pays neither",
		in: session(
			command("c1", "", 0, "review", prompt("P")),
			call("a0", "c1", 5, "toolu_s1", "Agent"),
			result("r0", "a0", 9, "toolu_s1", prompt("P"), func(r *ClaudeRow) {
				r.ToolUseResult = json.RawMessage(`{"agentId":"ag-2","status":"completed"}`)
			}),
			reply("a0b", "r0", 10),
			typed("u1", "a0b", 60, "P", "now the tests"),
			call("a1", "u1", 65, "toolu_s2", "Agent"),
			result("r1", "a1", 70, "toolu_s2", prompt("P")),
			reply("a2", "r1", 75),
			typed("u2", "a2", 100, "Q", "another thing"),
			reply("a3", "u2", 105),

			user("su1", "", 66, "subagent task", prompt("P"), sidechain("ag-1"), func(r *ClaudeRow) { r.ToolUseID = "toolu_s2" }),
			reply("sa1", "su1", 67, sidechain("ag-1"), func(r *ClaudeRow) { r.ToolUseID = "toolu_s2" }),
			user("su2", "", 7, "subagent task", prompt("P"), sidechain("ag-2")),
			reply("sa2", "su2", 8, sidechain("ag-2")),
			user("su3", "", 68, "subagent task", prompt("P"), sidechain("ag-3")),
			reply("sa3", "su3", 69, sidechain("ag-3")),
			user("su4", "", 101, "subagent task", prompt("Q"), sidechain("ag-4")),
			reply("sa4", "su4", 102, sidechain("ag-4")),
		).request("a0", 1).request("a0b", 1).request("a1", 1).request("a2", 1).request("a3", 1).
			request("sa1", 2).request("sa2", 4).request("sa3", 8).request("sa4", 16).
			event(6, "P", 1).event(66, "P", 2).event(106, "Q", 4).event(107, "", 8).event(108, "Z", 16),
		checks: checks(
			turns("key prompt request requests cost",
				"c1 P(shared) request#1 3req/600in unattributable(shared_prompt_id)",
				"u1 P(shared) request#2 3req/400in unattributable(shared_prompt_id)",
				"u2 Q request#3 2req/1700in otel=4"),
			sessionIs("cost", sessionCost, "otel=31 4+27"),
			sessionIs("tokens", tokens, "9: 3500/350/35000/175"),
			sessionIs("unattributed", func(s Session) SubagentUnattributed { return s.Detail.SubagentUnattributed },
				SubagentUnattributed{Requests: 1, Tokens: 8920}),
		),
	},
}

func TestSharedPromptID(t *testing.T) { runCases(t, sharedPromptCases) }

// p1b-design §9.1, 서브에이전트.
var subagentCases = []tcase{
	{
		// Source files distinguish sidechains whose agent IDs are both empty.
		// A head with neither a source file nor an agent ID cannot safely be
		// associated with either thread, even when its row has a prompt ID.
		name: "sidechain source files keep requests with their own turns",
		in: session(
			typed("u1", "", 0, "P1", "first"),
			reply("a1", "u1", 1),
			typed("u2", "a1", 20, "P2", "second"),
			reply("a2", "u2", 21),
			user("s1u", "", 2, "task one", prompt("P1"), sidechain(""), func(r *ClaudeRow) { r.SourceFile = "agent-one.jsonl" }),
			reply("s1a", "s1u", 3, sidechain(""), func(r *ClaudeRow) { r.SourceFile = "agent-one.jsonl" }),
			user("s2u", "", 22, "task two", prompt("P2"), sidechain(""), func(r *ClaudeRow) { r.SourceFile = "agent-two.jsonl" }),
			reply("s2a", "s2u", 23, sidechain(""), func(r *ClaudeRow) { r.SourceFile = "agent-two.jsonl" }),
			user("sxu", "", 24, "unknown thread", prompt("P2"), sidechain("")),
			reply("sxa", "sxu", 25, sidechain("")),
		).request("s1a", 1).request("s2a", 2).request("sxa", 4),
		checks: checks(
			turns("key requests", "u1 1req/100in", "u2 1req/200in"),
			sessionIs("unattributed", func(s Session) SubagentUnattributed { return s.Detail.SubagentUnattributed },
				SubagentUnattributed{Requests: 1, Tokens: 4460}),
		),
	},
	{
		// ag-a starts with P2, so it is u2's even though its tool_use id is the
		// call in u1's turn: the prompt id comes first. ag-b has no prompt id
		// (a pre-P0 file) and goes by that tool_use id. ag-c and ag-e have
		// neither and are named by a tool result, under either spelling of the
		// key; ag-c is named twice and is the first result's. ag-d is named
		// nowhere, and the thread with no agent id at all cannot be named.
		name: "a subagent is placed by prompt id, then tool_use id, then agent id",
		in: session(
			typed("u1", "", 0, "P1", "first"),
			call("a1", "u1", 5, "toolu_a", "Agent"),
			result("r1", "a1", 10, "toolu_a", prompt("P1"), func(r *ClaudeRow) { r.ToolUseResult = json.RawMessage(`{"agentId":"ag-c"}`) }),
			reply("a2", "r1", 15),
			typed("u2", "a2", 100, "P2", "second"),
			call("a3", "u2", 105, "toolu_e", "Task"),
			result("r2", "a3", 110, "toolu_e", prompt("P2"), func(r *ClaudeRow) { r.ToolUseResult = json.RawMessage(`{"agent_id":"ag-e","agentId":"ag-c"}`) }),
			reply("a4", "r2", 115),
			// A tool_use that lost its id is no one's call: the threads with no
			// tool_use id must not be matched to it.
			newRow("assistant", "a5", "a4", 116, model("claude-opus-5"), blocks(`[{"t":"tool_use","n":"Bash"}]`)),

			user("sua", "", 101, "task", prompt("P2"), sidechain("ag-a"), func(r *ClaudeRow) { r.ToolUseID = "toolu_a" }),
			reply("saa", "sua", 102, sidechain("ag-a"), func(r *ClaudeRow) { r.ToolUseID = "toolu_a" }),
			// The thread's first user line is the one read, not this later one.
			result("sra", "saa", 103, "toolu_x", sidechain("ag-a"), preP0()),
			// Only the middle row of ag-b carries the tool_use id.
			user("sub", "", 6, "task", preP0(), sidechain("ag-b")),
			reply("sab", "sub", 7, preP0(), sidechain("ag-b"), func(r *ClaudeRow) { r.ToolUseID = "toolu_a" }),
			reply("sab2", "sab", 17, preP0(), sidechain("ag-b")),
			user("suc", "", 8, "task", preP0(), sidechain("ag-c"), func(r *ClaudeRow) { r.SourceFile = "agent-c.jsonl" }),
			reply("sac", "suc", 9, preP0(), sidechain("ag-c"), func(r *ClaudeRow) { r.SourceFile = "agent-c.jsonl" }),
			user("sud", "", 11, "task", preP0(), sidechain("ag-d")),
			reply("sad", "sud", 12, preP0(), sidechain("ag-d")),
			user("sue", "", 106, "task", preP0(), sidechain("ag-e")),
			reply("sae", "sue", 107, preP0(), sidechain("ag-e")),
			user("sun", "", 13, "task", preP0(), sidechain("")),
			reply("san", "sun", 14, preP0(), sidechain("")),
		).request("a1", 1).request("a2", 1).request("a3", 1).request("a4", 1).
			request("saa", 2).request("sab", 4).request("sac", 8).request("sad", 16).request("sae", 32).request("san", 64),
		checks: checks(
			turns("key requests", "u1 4req/1400in", "u2 4req/3600in"),
			sessionIs("tokens", tokens, "10: 13000/1300/130000/650"),
			sessionIs("unattributed", func(s Session) SubagentUnattributed { return s.Detail.SubagentUnattributed },
				SubagentUnattributed{Requests: 2, Tokens: 89200}),
			// Subagent lines are not activity of the main thread.
			segments("0..116/9/2"),
			sessionIs("activity", activity, "0..116 first record 0"),
		),
	},
}

func TestSubagents(t *testing.T) { runCases(t, subagentCases) }
