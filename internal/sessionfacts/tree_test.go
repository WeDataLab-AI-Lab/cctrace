package sessionfacts

import (
	"strings"
	"testing"
)

// p1b-design §9.1, 트리.
var treeCases = []tcase{
	{
		// Rewinding to a1 and asking something else leaves u2 and its reply on
		// a branch no later line continues. The abandoned turn keeps its row
		// and its cost, and is neither a request nor an input.
		name: "a rewind leaves the sibling branch abandoned",
		in: session(
			typed("u1", "", 0, "P1", "first question"),
			reply("a1", "u1", 5),
			typed("u2", "a1", 10, "P2", "tried this"),
			reply("a2", "u2", 15),
			typed("u3", "a1", 20, "P3", "then this instead"),
			reply("a3", "u3", 25),
		).request("a1", 1, priced(0.25)).request("a2", 2, priced(0.5)).request("a3", 4, priced(1)),
		checks: checks(
			turns("key ordinal branch kind request status requests cost inputs",
				"u1 1 active typed request#1 ended 1req/100in estimate=0.25 [1:u1:typed]+0",
				"u2 2 abandoned typed - ended 1req/200in estimate=0.5 []+0",
				"u3 3 active typed request#2 ended 1req/400in estimate=1 [2:u3:typed]+0"),
			turnCounts("2/2/2/1"),
			sessionIs("tokens", tokens, "3: 700/70/7000/35"),
			sessionIs("cost", sessionCost, "estimate=1.75 1.75+0"),
			sessionIs("last turn", func(s Session) string { return s.LastTurnKey + " " + s.LastTurnStatus }, "u3 ended"),
		),
	},
	{
		// The older compaction format: the boundary starts a new root and its
		// logicalParentUuid names a line of the conversation before it. Joined,
		// the lines after the boundary follow a1 up into u1's turn. Left apart,
		// they would fall to the latest turn opened before them, r1's.
		name: "a compact boundary is joined to the line it continues from",
		in: session(
			typed("u1", "", 0, "P1", "long task"),
			reply("a1", "u1", 5),
			typed("r1", "", 50, "P2", "resumed elsewhere"),
			reply("ra", "r1", 55),
			system("b1", "", 100, "compact_boundary", func(r *ClaudeRow) { r.LogicalParentUUID = "a1" }),
			user("s1", "b1", 101, "This session is being continued", func(r *ClaudeRow) { r.IsCompactSummary = true }),
			reply("a3", "s1", 110),
		),
		checks: checks(turns("key branch span", "u1 active 0..110", "r1 active 50..55")),
	},
	{
		// The newer format points logicalParentUuid at a line the boundary
		// itself heads. Joining them would close a loop, so the boundary stays a
		// root and its lines, with no turn above them, join the latest turn
		// opened before them.
		name: "a compact boundary naming its own descendant stays a root",
		in: session(
			typed("u1", "", 0, "P1", "long task"),
			reply("a1", "u1", 5),
			typed("r1", "", 50, "P2", "resumed elsewhere"),
			reply("ra", "r1", 55),
			system("b1", "", 100, "compact_boundary", func(r *ClaudeRow) { r.LogicalParentUUID = "k1" }),
			user("k1", "b1", 101, "This session is being continued", func(r *ClaudeRow) { r.IsCompactSummary = true }),
			reply("a3", "k1", 110),
		),
		checks: checks(turns("key branch span", "u1 active 0..5", "r1 active 50..110")),
	},
	{
		// A resumed session starts a new root. Each component has its own active
		// path, so the turns before the resume are not abandoned.
		name: "a resume starts a second component and both are active",
		in: session(
			typed("u1", "", 0, "P1", "before"),
			reply("a1", "u1", 5),
			typed("u2", "", 100, "P2", "after the resume"),
			reply("a2", "u2", 105),
		),
		checks: checks(
			turns("key branch request", "u1 active request#1", "u2 active request#2"),
			turnCounts("2/2/2/0"),
		),
	},
	{
		name: "a line whose parent was never stored is a root",
		in: session(
			typed("u1", "never-stored", 0, "P1", "question"),
			reply("a1", "u1", 5),
		),
		checks: checks(turns("key branch request status", "u1 active request#1 ended")),
	},
	{
		// One uuid stored twice, under two profiles: the node is the row with
		// the lesser profile_email, whose parent is u1. The other row names u0,
		// and taken as the node it would move the reply into u0's turn and
		// leave u1 without a response. It is listed first: arrival order does
		// not choose.
		name: "of two rows with one uuid the node is chosen by the rows themselves",
		in: session(
			reply("a1", "u0", 15, func(r *ClaudeRow) { r.ProfileEmail = "zed@example.com" }),
			typed("u0", "", 0, "P0", "earlier"),
			reply("a0", "u0", 5),
			typed("u1", "a0", 10, "P1", "later"),
			reply("a1", "u1", 15),
		),
		checks: checks(turns("key span status", "u0 0..5 ended", "u1 10..15 ended")),
	},
	{
		// The latest leaf is the greatest (ts, uuid). u-y is listed first, so
		// neither arrival order nor row id makes it the later of the two.
		name: "of two leaves stamped together the greater uuid is the later",
		in: session(
			typed("u1", "", 0, "P1", "first"),
			reply("a1", "u1", 5),
			typed("u-y", "a1", 10, "PY", "the greater uuid"),
			typed("u-x", "a1", 10, "PX", "the lesser uuid"),
		),
		checks: checks(turns("key branch request", "u1 active request#1", "u-x abandoned -", "u-y active request#2")),
	},
	{
		// Rows that hold a loop would never reach a root. The loop is cut above
		// its earliest line, x1, whichever row the walk meets first: here it
		// enters at x3, through y0, which hangs from it. Cut above x3 instead,
		// x1 would sit beside w1, the latest leaf, on a branch w1 abandoned.
		// z9 names itself as its parent and is a root.
		//
		// y0 is stamped before the line that opened the turn, so it is the
		// turn's first line and its key.
		name: "a loop of parents is cut above its earliest line",
		in: session(
			reply("x2", "x1", 10),
			typed("x1", "x3", 5, "P1", "question"),
			result("x3", "x2", 15, "toolu_1", prompt("P1")),
			attachment("y0", "x3", 0, `{"type":"plan_mode_exit"}`),
			attachment("w1", "x3", 30, `{"type":"plan_mode_exit"}`),
			attachment("z9", "z9", 40, `{"type":"plan_mode_exit"}`),
		),
		checks: checks(turns("key opener branch span", "y0 x1 active 0..40")),
	},
	{
		// Only user, assistant, system and attachment lines are nodes. u2 hangs
		// from a progress line, so it is a root of its own and stays active when
		// a later sibling branch grows from a1. Were the progress line a node,
		// u2 would be on a branch that u3 abandoned.
		name: "what hangs from a line that is no node is a component of its own",
		in: session(
			typed("u1", "", 0, "P1", "first"),
			reply("a1", "u1", 5),
			newRow("progress", "p1", "a1", 6),
			typed("u2", "p1", 10, "P2", "hangs from the progress line"),
			reply("a2", "u2", 15),
			typed("u3", "a1", 20, "P3", "a later branch"),
			reply("a3", "u3", 25),
		),
		checks: checks(
			turns("key branch", "u1 active", "u2 active", "u3 active"),
			// The progress line is still a record of the main thread.
			segments("0..25/7/3"),
		),
	},
}

func TestTree(t *testing.T) { runCases(t, treeCases) }

// twice is a session in which the uuid d1 is stored twice, as rows a and b,
// after two turns it could hang from.
func twice(a, b ClaudeRow) *fixture {
	return session(
		b,
		typed("u0", "", 0, "P0", "earlier"),
		reply("a0", "u0", 5),
		typed("u1", "a0", 10, "P1", "later"),
		a,
	)
}

// The node of a uuid stored twice is its least row by (ts, record_type,
// profile_email, parent_uuid), one key at a time (p1b-design §3.4 rule 1). In
// each case the other row would hang the line from the other turn.
var nodeCases = []tcase{
	{
		name:   "the earlier ts",
		in:     twice(reply("d1", "u1", 15), reply("d1", "u0", 16)),
		checks: checks(turns("key span status", "u0 0..5 ended", "u1 10..15 ended")),
	},
	{
		name:   "then the lesser record_type",
		in:     twice(reply("d1", "u1", 15), attachment("d1", "u0", 15, `{"type":"plan_mode_exit"}`)),
		checks: checks(turns("key span status", "u0 0..5 ended", "u1 10..15 ended")),
	},
	{
		name:   "then the lesser profile_email",
		in:     twice(reply("d1", "u1", 15), reply("d1", "u0", 15, func(r *ClaudeRow) { r.ProfileEmail = "zed@example.com" })),
		checks: checks(turns("key span status", "u0 0..5 ended", "u1 10..15 ended")),
	},
	{
		// The row under u0 is listed last here, so it is not the lesser id.
		name:   "then the lesser parent_uuid",
		in:     twice(reply("d1", "u0", 15), reply("d1", "u1", 15)),
		checks: checks(turns("key span status", "u0 0..15 ended", "u1 10..10 no_response")),
	},
	{
		// The storage key makes two rows equal in all four impossible. If they
		// are given anyway, the lesser id is the node -- b here, listed first --
		// so that the choice still does not depend on arrival order.
		name:   "and between rows the rule cannot tell apart, the lesser id",
		in:     twice(typed("d1", "u1", 15, "PA", "one"), typed("d1", "u1", 15, "PB", "other")),
		checks: checks(turns("key prompt", "u0 P0", "u1 P1", "d1 PB")),
	},
}

func TestNodeOfATwiceStoredUUID(t *testing.T) { runCases(t, nodeCases) }

// p1b-design §9.1, 턴 경계.
var boundaryCases = []tcase{
	{
		// PA comes back on a tool result after PB's turn: it is still PA's turn,
		// not a third one.
		name: "a prompt id that returns after another turn joins its own turn",
		in: session(
			typed("u1", "", 0, "PA", "task a"),
			call("a1", "u1", 5, "toolu_1", "Bash"),
			typed("u2", "a1", 10, "PB", "side question"),
			reply("a2", "u2", 15),
			result("r1", "a2", 20, "toolu_1", prompt("PA")),
			reply("a3", "r1", 25),
		),
		checks: checks(
			turns("key prompt span status", "u1 PA 0..25 ended", "u2 PB 10..15 ended"),
			turnCounts("2/2/2/0"),
		),
	},
	{
		// A new promptId can first appear on a meta line or on a tool result.
		// That line opens the turn and is its key, but the source and preview
		// are the first input line's. A turn with no input line at all has
		// neither and is no request.
		name: "a turn opened by a meta line or a tool result is previewed by its first input",
		in: session(
			user("m1", "", 0, "<local-command-caveat>Caveat: the messages below</local-command-caveat>", meta(), prompt("P1")),
			typed("u1", "m1", 1, "P1", "real question\nwith a second line"),
			reply("a1", "u1", 5),
			result("r2", "", 10, "toolu_9", prompt("P2")),
			typed("u2", "r2", 11, "P2", "after the result"),
			reply("a2", "u2", 15),
			result("r3", "", 20, "toolu_8", prompt("P3")),
			reply("a3", "r3", 25),
		),
		checks: checks(
			turns("key opener kind preview request",
				`m1 m1 typed "real question"/ok request#1`,
				`r2 r2 typed "after the result"/ok request#2`,
				`r3 r3 unknown <none> -`),
			turnCounts("3/2/2/0"),
		),
	},
	{
		// Pre-P0 lines have no promptId, so each is judged by what it is. The
		// lines that open nothing follow their parent into the turn above.
		name: "without a prompt id a line opens a turn by its shape",
		in: session(
			user("s01", "", 0, "plain text", preP0()),
			user("s02", "s01", 10, "", preP0(), textBlock("text in a block")),
			user("s03", "s02", 20, "", preP0(), blocks(`[{"t":"input_text"}]`), func(r *ClaudeRow) { r.TextHead = "input text" }),
			result("s04", "s03", 30, "toolu_1", preP0()),
			user("s05", "s04", 40, "<local-command-stdout>done</local-command-stdout>", preP0()),
			user("s06", "s05", 50, "<local-command-caveat>Caveat</local-command-caveat>", preP0()),
			user("s07", "s06", 60, "<bash-stdout>out</bash-stdout>", preP0()),
			user("s08", "s07", 70, "<bash-stderr>err</bash-stderr>", preP0()),
			user("s09", "s08", 80, "[Request interrupted by user]", preP0()),
			user("s10", "s09", 90, "<task-notification>\n<task-id>t1</task-id>", preP0()),
			user("s11", "s10", 100, "<bash-input>ls</bash-input>", preP0()),
			user("s12", "s11", 110, "scaffolding", preP0(), meta()),
			user("s13", "s12", 120, "This session is being continued", preP0(), func(r *ClaudeRow) { r.IsCompactSummary = true }),
			command("s14", "s13", 130, "review", preP0()),
			user("s15", "s14", 140, "run the deploy skill", preP0(), func(r *ClaudeRow) { r.CommandName, r.CommandInvoke = "deploy", "implicit" }),
			user("s16", "s15", 150, "", preP0(), blocks(`[{"t":"image"}]`)),
			// A tool result with a text block beside it is still a tool result.
			user("s17", "s16", 160, "", preP0(), blocks(`[{"t":"tool_result","tu":"toolu_2"},{"t":"text"}]`), func(r *ClaudeRow) { r.TextHead = "and also this" }),
		),
		checks: checks(
			turns("key kind request span",
				"s01 unknown request#1 0..0",
				"s02 unknown request#2 10..10",
				"s03 unknown request#3 20..80",
				"s10 notification - 90..90",
				"s11 command - 100..120",
				"s14 command - 130..130",
				"s15 unknown request#4 140..160"),
			turnCounts("7/4/7/0"),
		),
	},
	{
		// The second turn was opened after a /login: a turn's login is its
		// opening line's.
		name: "lines with and without a prompt id in one session",
		in: session(
			user("o1", "", 0, "old question", preP0()),
			reply("oa", "o1", 5, preP0()),
			typed("n1", "oa", 100, "P1", "new question", func(r *ClaudeRow) { r.LoginEmail = "second@example.com" }),
			reply("na", "n1", 105),
		),
		checks: checks(turns("key prompt kind request login",
			"o1 - unknown request#1 dev@example.com",
			"n1 P1 typed request#2 second@example.com")),
	},
	{
		// h1, h2 and z3 start components of their own with no turn above them:
		// each joins the latest turn opened by its time, and z3, stamped at the
		// very instant u2 opened, joins u2's. a0 comes before any turn and
		// belongs to none, but it is activity all the same.
		name: "a line with no turn above it joins the latest turn opened before it",
		in: session(
			attachment("a0", "", 0, `{"type":"model","identity":{"modelId":"claude-opus-5"}}`),
			typed("u1", "", 10, "P1", "first"),
			reply("a1", "u1", 15),
			system("t1", "a1", 16, "turn_duration"),
			attachment("h1", "never-stored", 20, `{"type":"plan_mode_exit"}`),
			typed("u2", "a1", 30, "P2", "second"),
			reply("a2", "u2", 35),
			attachment("h2", "", 40, `{"type":"plan_mode_exit"}`),
			attachment("z3", "", 30, `{"type":"plan_mode_exit"}`),
		),
		checks: checks(
			turns("key span", "u1 10..20", "u2 30..40"),
			segments("0..40/9/2"),
		),
	},
	{
		// The order is (ts, uuid): the uuid decides between lines of one
		// instant, whichever is listed first.
		name: "lines stamped at the same instant are read in uuid order",
		in: session(
			typed("u-b", "", 0, "PB", "second by uuid"),
			typed("u-a", "", 0, "PA", "first by uuid"),
			reply("a-b", "u-b", 5),
			reply("a-a", "u-a", 5),
		).request("a-b", 1, priced(0.25)).request("a-a", 2, priced(0.5)),
		checks: checks(
			turns("key ordinal request inputs cost",
				"u-a 1 request#1 [1:u-a:typed]+0 estimate=0.5",
				"u-b 2 request#2 [2:u-b:typed]+0 estimate=0.25"),
			sessionIs("last turn", func(s Session) string { return s.LastTurnKey }, "u-b"),
		),
	},
	{
		// A turn starts at its first line. The attachment under u-a is stamped
		// before u-a itself, so u-a's turn starts first though u-b was opened
		// first.
		name: "turns are numbered by their first lines",
		in: session(
			typed("u-b", "", 3, "PB", "opened first"),
			typed("u-a", "", 5, "PA", "started first"),
			attachment("y0", "u-a", 0, `{"type":"plan_mode_exit"}`),
		),
		checks: checks(turns("key opener ordinal span request", "y0 u-a 1 0..5 request#1", "u-b u-b 2 3..3 request#2")),
	},
	{
		// The preview is the first line of the input's text, cut to 160 bytes
		// without splitting a character: 53 three-byte characters are 159.
		name: "the preview is the first line, 160 bytes at most",
		in: session(
			typed("v1", "", 0, "P1", strings.Repeat("a", 200)),
			typed("v2", "", 10, "P2", strings.Repeat("가", 100)),
			typed("v3", "", 20, "P3", "\n\n  indented first line  \r\nsecond line"),
			typed("v4", "", 30, "P4", " \n\t\n"),
			typed("v5", "", 40, "P5", ""),
		),
		checks: checks(turns("key preview",
			`v1 "`+strings.Repeat("a", 160)+`"/ok`,
			`v2 "`+strings.Repeat("가", 53)+`"/ok`,
			`v3 "indented first line"/ok`,
			`v4 <none>`,
			`v5 <none>`)),
	},
}

func TestTurnBoundaries(t *testing.T) { runCases(t, boundaryCases) }
