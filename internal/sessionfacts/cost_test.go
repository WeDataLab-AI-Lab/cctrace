package sessionfacts

import (
	"testing"
	"time"
)

// p1b-design §9.1, 비용 상태.
var costCases = []tcase{
	{
		// otel_events holds nothing older than second 100. u1 began before it
		// and none of its events is left: expired. u2 began before it and one
		// event is left: what remains, as partial. u3 began after it. u4 called
		// no API. o0 and o5 are pre-P0 turns with no prompt id to tie an event
		// to, before the boundary or after it: o5's call is in the unattached
		// sum with the event that has no prompt id.
		name: "telemetry cut by retention, turn by turn",
		in: session(
			typed("u1", "", 0, "P1", "expired"),
			reply("a1", "u1", 5),
			user("o0", "a1", 20, "an old client's line", preP0()),
			reply("oa0", "o0", 25, preP0()),
			typed("u2", "oa0", 50, "P2", "straddles the boundary"),
			call("a2", "u2", 55, "toolu_1", "Bash"),
			result("r2", "a2", 110, "toolu_1", prompt("P2")),
			reply("a2b", "r2", 115),
			typed("u3", "a2b", 200, "P3", "after the boundary"),
			reply("a3", "u3", 205),
			typed("u4", "a3", 300, "P4", "no answer yet"),
			user("o5", "u4", 400, "an old client's line", preP0()),
			reply("oa5", "o5", 405, preP0()),
		).request("a1", 1).request("oa0", 1).request("a2", 1).request("a2b", 1).request("a3", 1).request("oa5", 1).
			event(112, "P2", 2).event(206, "P3", 4).event(406, "PX", 8).event(407, "", 1).
			with(func(in *Input) { in.OTELOldest = at(100) }),
		checks: checks(
			turns("key cost",
				"u1 expired",
				"o0 unattributable(pre_p0)",
				"u2 otel_partial=2",
				"u3 otel=4",
				"u4 none",
				"o5 unattributable(pre_p0)"),
			sessionIs("cost", sessionCost, "otel_partial=15 6+9"),
		),
	},
	{
		name: "a session that began after the oldest event is whole",
		in: session(
			typed("u1", "", 200, "P1", "question"),
			reply("a1", "u1", 205),
		).request("a1", 1).event(206, "P1", 4).with(func(in *Input) { in.OTELOldest = at(100) }),
		checks: checks(turns("key cost", "u1 otel=4"), sessionIs("cost", sessionCost, "otel=4 4+0")),
	},
	{
		// The session has telemetry, but none of it is an api_request: it is
		// still an OTEL session, with nothing measured, and no estimate is put
		// in its place.
		name: "an OTEL session with no api_request event",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			reply("a1", "u1", 5),
		).request("a1", 1, priced(0.5)).with(func(in *Input) { in.HasOTEL = true }),
		checks: checks(turns("key cost", "u1 none"), sessionIs("cost", sessionCost, "otel=0 0+0")),
	},
	{
		// No telemetry: the cost is the imputed rows. u2 has a request the
		// 30-minute rewrite has not priced yet, so its sum is not final, and the
		// session's is not either. u3's only request ran on a model with no
		// rate. u5 has one priced request and one that never will be: the priced
		// one is its cost. The subagent no turn was found for is in the
		// session's cost and in the unattached sum.
		name: "estimated, pending, and without a rate",
		in: session(
			typed("u1", "", 0, "P1", "priced"),
			reply("a1", "u1", 5),
			typed("u2", "a1", 10, "P2", "one request pending"),
			call("a2", "u2", 15, "toolu_1", "Bash"),
			result("r2", "a2", 20, "toolu_1", prompt("P2")),
			reply("a2b", "r2", 25),
			typed("u3", "a2b", 30, "P3", "a local model"),
			reply("a3", "u3", 35, model("qwen-local")),
			typed("u4", "a3", 40, "P4", "no answer"),
			typed("u5", "u4", 50, "P5", "mixed"),
			call("a5", "u5", 55, "toolu_2", "Bash"),
			result("r5", "a5", 60, "toolu_2", prompt("P5")),
			reply("a5b", "r5", 65, model("qwen-local")),
			user("sx", "", 70, "task", preP0(), sidechain("ag-lost")),
			reply("sa", "sx", 75, preP0(), sidechain("ag-lost")),
		).request("a1", 1, priced(0.5)).request("a2", 1, priced(0.25)).request("a2b", 1, rated()).
			request("a3", 1).request("a5", 1, priced(1)).request("a5b", 1).request("sa", 1, priced(2)),
		checks: checks(
			turns("key cost",
				"u1 estimate=0.5",
				"u2 estimate_pending=0.25",
				"u3 no_rate",
				"u4 none",
				"u5 estimate=1"),
			sessionIs("cost", sessionCost, "estimate_pending=3.75 1.75+2"),
		),
	},
	{
		// Three events at one instant, listed in an order no sort would leave
		// them in, and one of them read in another zone: the session's last
		// activity is that instant, whichever of the three is met first.
		name: "events of one instant",
		in: session(
			typed("u1", "", 0, "P1", "first"),
			reply("a1", "u1", 5),
			typed("u2", "a1", 10, "P2", "second"),
			reply("a2", "u2", 15),
		).request("a1", 1).request("a2", 1).event(20, "P2", 2).event(20, "P1", 4).event(20, "P1", 1).
			with(func(in *Input) { in.OTEL[1].TS = in.OTEL[1].TS.In(time.FixedZone("KST", 9*60*60)) }),
		checks: checks(
			turns("key cost", "u1 otel=5", "u2 otel=2"),
			sessionIs("cost", sessionCost, "otel=7 7+0"),
			sessionIs("activity", activity, "0..20 first record 0"),
		),
	},
	{
		// A shared prompt id cannot attribute either event, even if no request
		// head is available for either turn. The events still enter the session
		// total and the unattached bucket exactly once.
		name: "shared prompt id with telemetry and no request heads is unattributable",
		in: session(
			command("c1", "", 0, "model", prompt("P")),
			user("o1", "c1", 1, "<local-command-stdout>Set model</local-command-stdout>", prompt("P")),
			typed("u1", "o1", 10, "P", "next question"),
			reply("a1", "u1", 15),
		).event(2, "P", 1).event(16, "P", 2),
		checks: checks(
			turns("key prompt requests cost",
				"c1 P(shared) 0req/0in unattributable(shared_prompt_id)",
				"u1 P(shared) 0req/0in unattributable(shared_prompt_id)"),
			sessionIs("cost", sessionCost, "otel=3 0+3"),
			func(t *testing.T, out Output) {
				var attached float64
				for _, turn := range out.Turns {
					if turn.CostUSD != nil {
						attached += *turn.CostUSD
					}
				}
				if out.Session.CostUSD == nil || attached+out.Session.Detail.Cost.UnattachedTurnUSD != *out.Session.CostUSD {
					t.Errorf("turn cost plus unattached = %g, want session cost %v", attached+out.Session.Detail.Cost.UnattachedTurnUSD, out.Session.CostUSD)
				}
			},
		),
	},
	{
		// A turn that called no API has no cost to place, wherever it sits: the
		// /clear began before the oldest event still stored and the /model is a
		// pre-P0 line, but neither had anything to expire or to attribute.
		name: "a turn that called no API is none before anything else",
		in: session(
			command("c1", "", 0, "clear", prompt("P1")),
			user("o1", "c1", 1, "<local-command-stdout></local-command-stdout>", prompt("P1")),
			command("c0", "o1", 5, "model", preP0()),
			typed("u2", "c0", 200, "P2", "question"),
			reply("a2", "u2", 205),
		).request("a2", 1).event(206, "P2", 4).with(func(in *Input) { in.OTELOldest = at(100) }),
		checks: checks(
			turns("key cost", "c1 none", "c0 none", "u2 otel=4"),
			sessionIs("cost", sessionCost, "otel_partial=4 4+0"),
		),
	},
	{
		// The boundary itself is not before the boundary.
		name: "what began exactly at the oldest event is whole",
		in: session(
			typed("u1", "", 100, "P1", "question"),
			reply("a1", "u1", 105),
		).request("a1", 1).event(106, "P1", 4).with(func(in *Input) { in.OTELOldest = at(100) }),
		checks: checks(turns("key cost", "u1 otel=4"), sessionIs("cost", sessionCost, "otel=4 4+0")),
	},
	{
		// 0.1 + 0.2 + 0.3 and 0.3 + 0.2 + 0.1 differ in their last bit. Each sum
		// is taken in an order the rows fix, so the shuffled runs of
		// TestOutputDoesNotDependOnInputOrder agree on them.
		name: "measured sums whose terms do not add up the same in any order",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			reply("a1", "u1", 5),
		).request("a1", 1).
			event(8, "P1", 0.1).event(9, "P1", 0.2).event(10, "P1", 0.3).
			event(11, "", 0.1).event(12, "", 0.2).event(13, "", 0.3),
		checks: checks(turns("key", "u1"), sessionIs("source", func(s Session) string { return s.CostSource }, "otel")),
	},
	{
		name: "estimated sums whose terms do not add up the same in any order",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			reply("a1", "u1", 5),
			reply("a2", "a1", 6),
			reply("a3", "a2", 7),
			user("sx", "", 8, "task", preP0(), sidechain("ag-lost")),
			reply("s1", "sx", 9, preP0(), sidechain("ag-lost")),
			reply("s2", "s1", 10, preP0(), sidechain("ag-lost")),
			reply("s3", "s2", 11, preP0(), sidechain("ag-lost")),
		).request("a1", 1, priced(0.1)).request("a2", 1, priced(0.2)).request("a3", 1, priced(0.3)).
			request("s1", 1, priced(0.1)).request("s2", 1, priced(0.2)).request("s3", 1, priced(0.3)),
		checks: checks(turns("key", "u1"), sessionIs("source", func(s Session) string { return s.CostSource }, "estimate")),
	},
	{
		name: "every request priced",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			reply("a1", "u1", 5),
		).request("a1", 1, priced(0.5)),
		checks: checks(turns("key cost", "u1 estimate=0.5"), sessionIs("cost", sessionCost, "estimate=0.5 0.5+0")),
	},
	{
		// A request waiting for its first row: the sum so far is zero, which is
		// a value, not an absence.
		name: "pending with nothing priced yet",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			reply("a1", "u1", 5),
		).request("a1", 1, rated()),
		checks: checks(turns("key cost", "u1 estimate_pending=0"), sessionIs("cost", sessionCost, "estimate_pending=0 0+0")),
	},
	{
		// The session row has no state for requests that can never be priced:
		// it has no imputed row, which is none.
		name: "no request has a rate",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			reply("a1", "u1", 5, model("qwen-local")),
		).request("a1", 1),
		checks: checks(turns("key cost", "u1 no_rate"), sessionIs("cost", sessionCost, "none=null 0+0")),
	},
	{
		// Every event has expired and only the login stamped from them is left:
		// the cost is the estimate, noted as such.
		name: "telemetry that has all expired leaves its note",
		in: session(
			typed("u1", "", 0, "P1", "question", func(r *ClaudeRow) { r.LoginEmailSource = "otel" }),
			reply("a1", "u1", 5),
		).request("a1", 1, priced(0.5)),
		checks: checks(turns("key cost", "u1 estimate=0.5"), sessionIs("cost", sessionCost, "estimate=0.5 0.5+0 otel_expired")),
	},
}

func TestCostStates(t *testing.T) { runCases(t, costCases) }

// The rules that skip copies (p1b-design §3.16): this session was forked from
// another, which keeps u1, a1, u2, q2 and r2. Turn 1 is all the other
// session's; turn 2 opens on a copied line, holds a copied question and answer,
// and goes on with a reply of its own.
func forked() *fixture {
	return session(
		typed("u1", "", 0, "P1", "the original question"),
		reply("a1", "u1", 5),
		typed("u2", "a1", 10, "P2", "asked just before the fork"),
		call("q2", "u2", 11, "toolu_q", "AskUserQuestion"),
		result("r2", "q2", 12, "toolu_q", prompt("P2")),
		reply("a2", "r2", 15),
		typed("u3", "a2", 2000, "P3", "asked in the fork"),
		reply("a3", "u3", 2005),
	).request("a1", 1, priced(0.5)).request("q2", 8, priced(4)).request("a2", 2, priced(0.25)).request("a3", 4, priced(1))
}

var copyCases = []tcase{
	{
		// With the predicate on: the wholly copied turn has no row, the partly
		// copied one stays, marked, and is no request, and the copied lines are
		// in no sum, input, segment or range. first_record_at alone counts
		// them: retention has to see the oldest row that was read.
		name: "with the copy predicate on, copies are skipped",
		in:   forked().copies("u1", "a1", "u2", "q2", "r2"),
		checks: checks(
			turns("key ordinal copied kind preview request requests cost inputs",
				`u2 1 true typed "asked just before the fork"/ok - 1req/200in estimate=0.25 []+0`,
				`u3 2 false typed "asked in the fork"/ok request#1 1req/400in estimate=1 [1:u3:typed]+0`),
			turnCounts("2/1/1/0"),
			sessionIs("tokens", tokens, "2: 600/60/6000/30"),
			sessionIs("cost", sessionCost, "estimate=1.25 1.25+0"),
			segments("15..15/1/0", "2000..2005/2/1"),
			sessionIs("activity", activity, "15..2005 first record 0"),
		),
	},
	{
		// The marks are written before the predicate is opened. Until then the
		// facts count copies like every other screen.
		name: "with the copy predicate off, the marks change nothing",
		in:   forked().copies("u1", "a1", "u2", "q2", "r2").with(func(in *Input) { in.CopyGate = false }),
		checks: checks(
			turns("key ordinal copied request requests inputs",
				"u1 1 false request#1 1req/100in [1:u1:typed]+0",
				"u2 2 false request#2 2req/1000in [2:u2:typed,3:r2:answer]+0",
				"u3 3 false request#3 1req/400in [4:u3:typed]+0"),
			turnCounts("3/3/4/0"),
			sessionIs("tokens", tokens, "4: 1500/150/15000/75"),
			sessionIs("cost", sessionCost, "estimate=5.75 5.75+0"),
			segments("0..15/6/2", "2000..2005/2/1"),
			sessionIs("activity", activity, "0..2005 first record 0"),
		),
	},
	{
		// The marks may not have reached a subagent's lines yet. Its turn has
		// no row, so its requests belong to no turn: they stay in the session's
		// sums and in the cost no turn took.
		name: "a request whose turn is all copies belongs to no turn",
		in: session(
			typed("u1", "", 0, "P1", "the original question"),
			call("a1", "u1", 5, "toolu_s", "Agent"),
			user("su", "", 6, "task", preP0(), sidechain("ag-1"), func(r *ClaudeRow) { r.ToolUseID = "toolu_s" }),
			reply("sa", "su", 7, preP0(), sidechain("ag-1"), func(r *ClaudeRow) { r.ToolUseID = "toolu_s" }),
			typed("u2", "a1", 100, "P2", "asked in the fork"),
			reply("a2", "u2", 105),
		).request("a1", 1, priced(0.5)).request("sa", 2, priced(0.25)).request("a2", 4, priced(1)).copies("u1", "a1"),
		checks: checks(
			turns("key requests cost", "u2 1req/400in estimate=1"),
			sessionIs("tokens", tokens, "2: 600/60/6000/30"),
			sessionIs("cost", sessionCost, "estimate=1.25 1+0.25"),
			sessionIs("unattributed", func(s Session) SubagentUnattributed { return s.Detail.SubagentUnattributed },
				SubagentUnattributed{Requests: 1, Tokens: 2230}),
		),
	},
	{
		// The rule is about any of the turn's lines, not its first: a turn with
		// a copied line is marked and is no request. Its own input line is
		// still an input.
		name: "a turn copied in part is marked wherever the copies are",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			reply("a1", "u1", 5),
			reply("a2", "a1", 6),
		).request("a1", 1).request("a2", 2).copies("a1"),
		checks: checks(
			turns("key copied request requests inputs", "u1 true - 1req/200in [1:u1:typed]+0"),
			turnCounts("1/0/1/0"),
		),
	},
}

func TestCopies(t *testing.T) { runCases(t, copyCases) }
