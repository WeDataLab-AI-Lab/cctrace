package sessionfacts

import (
	"testing"
	"time"
)

// codexSession is a Codex session as the projection reads it: a user message
// and two usage rows, whose imputed rows are its requests. The imputed tokens
// are net of cache reads, so they are not the rows' own.
func codexSession() *fixture {
	identity := func(r CodexRow) CodexRow {
		r.UserID, r.ProfileEmail, r.BillingProvider, r.AccountID = "user-1", "dev@example.com", "openai", "acct-1"
		r.ProjectHash, r.Branch, r.Entrypoint = "proj", "main", "cli"
		return r
	}
	return &fixture{in: Input{
		Agent: "codex",
		CodexRows: []CodexRow{
			identity(CodexRow{ID: 1, TS: at(0), RecordType: "user", RawType: "response_item", PayloadType: "message", TextHead: "add a test"}),
			identity(CodexRow{ID: 2, TS: at(10), RecordType: "usage", Model: "gpt-6-codex", InputTokens: 5000, OutputTokens: 100, CacheReadTokens: 4000,
				RawType: "token_usage_record", TurnID: "turn-1", RootTurnID: "turn-1", ResponseID: "resp-1"}),
			identity(CodexRow{ID: 3, TS: at(20), RecordType: "usage", Model: "gpt-6-codex", InputTokens: 10000, OutputTokens: 200, CacheReadTokens: 8000,
				RawType: "token_usage_record", TurnID: "turn-1", RootTurnID: "turn-1", ResponseID: "resp-2"}),
		},
		Heads: []Head{
			{ID: 2, TS: at(10), Model: "gpt-6-codex", InputTokens: 1000, OutputTokens: 100, CacheReadTokens: 4000, Imputed: true, HasRate: true, CostUSD: 0.5},
			{ID: 3, TS: at(20), Model: "gpt-6-codex", InputTokens: 2000, OutputTokens: 200, CacheReadTokens: 8000, Imputed: true, HasRate: true, CostUSD: 0.25},
		},
	}}
}

// A Codex session yields root turns and their request sums from the same
// projection as its identity, range, tokens and cost (p1b-design §3.8).
var codexCases = []tcase{
	{
		name: "identity, range, tokens and cost, with a root turn",
		in:   codexSession(),
		checks: checks(
			turns("key status requests", "turn-1 undeterminable(no_lifecycle) 2req/3000in"),
			segments(),
			sessionIs("turn boundaries", func(s Session) string { return s.TurnBoundaries + "|" + counts(s) }, "observed|1/1/1/0"),
			sessionIs("tokens", tokens, "2: 3000/300/12000/0"),
			sessionIs("cost", sessionCost, "estimate=0.75 0.75+0"),
			sessionIs("activity", activity, "0..20 first record 0"),
			sessionIs("owners", owners, "user-1 dev@example.com"),
			sessionIs("account keys", func(s Session) []AccountKey { return s.AccountKeys }, []AccountKey{{BillingProvider: "openai", AccountID: "acct-1"}}),
			sessionIs("place", func(s Session) string {
				return s.Agent + " " + s.ProjectHash + " " + s.RepositoryKey + " " + s.Branch + " " + s.Model
			},
				"codex proj proj main gpt-6-codex"),
			sessionIs("transcript", func(s Session) bool { return s.Transcript }, true),
		),
	},
	{
		// A Codex session's cost is its imputed rows whatever telemetry it sent.
		name: "telemetry does not change a Codex session's cost",
		in:   codexSession().event(30, "", 9).with(func(in *Input) { in.OTEL[0].BillingProvider = "openai" }),
		checks: checks(
			sessionIs("cost", sessionCost, "estimate=0.75 0.75+0"),
			sessionIs("activity", activity, "0..30 first record 0"),
		),
	},
	{
		// Only usage rows are marked as copies, so the copied part is the
		// session's prefix: every row up to the last copied usage row.
		name: "a copied usage row takes the prefix up to it out of the range",
		in:   codexSession().with(func(in *Input) { in.CopyGate, in.CopiedIDs = true, []int64{2} }),
		checks: checks(
			sessionIs("tokens", tokens, "1: 2000/200/8000/0"),
			sessionIs("cost", sessionCost, "estimate=0.25 0.25+0"),
			sessionIs("activity", activity, "20..20 first record 0"),
		),
	},
	{
		// Where the session ran is its latest row's word, and of two rows of one
		// instant the greater id is the later.
		name: "rows of one instant are read in id order",
		in: &fixture{in: Input{Agent: "codex", CodexRows: []CodexRow{
			{ID: 8, TS: at(5), RecordType: "usage", Branch: "second", RepositoryID: "github.com/acme/app", LoginEmailSource: "quota-inferred"},
			{ID: 7, TS: at(5), RecordType: "usage", Branch: "first", ProjectHash: "proj"},
			{ID: 6, TS: at(9), RecordType: "usage"},
			{ID: 5, TS: time.Unix(0, 0).UTC(), RecordType: "session_meta"},
		}}},
		checks: checks(
			// A row with no timestamp of its own is not when anything happened.
			sessionIs("activity", activity, "5..9 first record 5"),
			sessionIs("place", func(s Session) string { return s.ProjectHash + " " + s.RepositoryKey + " " + s.Branch }, "proj github.com/acme/app second"),
			sessionIs("login inferred", func(s Session) bool { return s.LoginEmailInferred }, true),
			sessionIs("cost", sessionCost, "none=null 0+0"),
		),
	},
}

func TestCodex(t *testing.T) { runCases(t, codexCases) }
