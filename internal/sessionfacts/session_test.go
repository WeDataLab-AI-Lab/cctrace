package sessionfacts

import (
	"testing"
	"time"
)

func str(s string) *string { return &s }

// titled is a one-request session with the given snapshots.
func titled(snapshots ...Snapshot) *fixture {
	return session(
		typed("u1", "", 0, "P1", "fix the login bug\nit fails on the second try"),
		reply("a1", "u1", 5),
	).with(func(in *Input) {
		for i := range snapshots {
			snapshots[i].UserID, snapshots[i].ProfileEmail = "user-1", "dev@example.com"
			snapshots[i].LoginEmail, snapshots[i].BillingProvider = "dev@example.com", "anthropic"
		}
		in.Snapshots = snapshots
	})
}

func title(s Session) string { return text(s.Title) + " " + s.TitleSource + "/" + s.TitleState }

// p1b-design §9.1, 제목.
var titleCases = []tcase{
	{
		name:   "the custom title comes first",
		in:     titled(Snapshot{CustomTitle: str("Login fix"), AITitle: str("Fix login retries"), AgentName: str("worker")}),
		checks: checks(sessionIs("title", title, "Login fix custom/ok")),
	},
	{
		name:   "an empty key says there was no such line, and the next is read",
		in:     titled(Snapshot{CustomTitle: str(""), AITitle: str("Fix login retries"), AgentName: str("worker")}),
		checks: checks(sessionIs("title", title, "Fix login retries ai/ok")),
	},
	{
		name:   "the agent name is the last of the three",
		in:     titled(Snapshot{CustomTitle: str(""), AITitle: str(""), AgentName: str("worker")}),
		checks: checks(sessionIs("title", title, "worker agent_name/ok")),
	},
	{
		// A default re-enrichment leaves the keys out: withheld. The first
		// request stands in and the state says so.
		name:   "missing keys are withheld, and the first request stands in",
		in:     titled(Snapshot{}),
		checks: checks(sessionIs("title", title, "fix the login bug first_request/withheld")),
	},
	{
		name:   "three empty keys make the first request the title",
		in:     titled(Snapshot{CustomTitle: str(""), AITitle: str(""), AgentName: str("")}),
		checks: checks(sessionIs("title", title, "fix the login bug first_request/ok")),
	},
	{
		// A title read under a strict policy stays hidden, and the first
		// request must not show in its place.
		name:   "a hidden key leaves no title, and the first request does not replace it",
		in:     titled(Snapshot{CustomTitle: str(marker), AITitle: str(""), AgentName: str("")}),
		checks: checks(sessionIs("title", title, "<null> none/redacted")),
	},
	{
		name:   "hidden outweighs withheld",
		in:     titled(Snapshot{AITitle: str(marker), AgentName: str("")}),
		checks: checks(sessionIs("title", title, "<null> none/redacted")),
	},
	{
		name:   "one withheld key among empty ones is withheld",
		in:     titled(Snapshot{AITitle: str(""), AgentName: str("")}),
		checks: checks(sessionIs("title", title, "fix the login bug first_request/withheld")),
	},
	{
		name:   "a key with a value wins over a withheld or hidden one before it",
		in:     titled(Snapshot{CustomTitle: str(marker), AgentName: str("worker")}),
		checks: checks(sessionIs("title", title, "worker agent_name/ok")),
	},
	{
		name:   "a session with no snapshot has every key withheld",
		in:     titled(),
		checks: checks(sessionIs("title", title, "fix the login bug first_request/withheld")),
	},
	{
		name: "a redacted first request leaves no title",
		in: session(
			user("u1", "", 0, "", prompt("P1"), source("typed"), redacted()),
			reply("a1", "u1", 5, redacted()),
		).with(func(in *Input) {
			in.Snapshots = []Snapshot{{UserID: "user-1", ProfileEmail: "dev@example.com", CustomTitle: str(""), AITitle: str(""), AgentName: str("")}}
		}),
		checks: checks(sessionIs("title", title, "<null> none/redacted")),
	},
	{
		name: "no title and no request",
		in: session().with(func(in *Input) {
			in.Snapshots = []Snapshot{{UserID: "user-1", ProfileEmail: "dev@example.com", CustomTitle: str(""), AITitle: str(""), AgentName: str("")}}
		}),
		checks: checks(sessionIs("title", title, "<null> none/none")),
	},
	{
		// The first request, not the first turn: a notification's turn comes
		// earlier and is no one's request.
		name: "the title falls back to the first request, not the first turn",
		in: session(
			user("n1", "", 0, "<task-notification>\n<task-id>t1</task-id>", prompt("P0"), origin("task-notification")),
			typed("u1", "n1", 10, "P1", "what did the task do"),
			reply("a1", "u1", 15),
		).with(func(in *Input) {
			in.Snapshots = []Snapshot{{UserID: "user-1", ProfileEmail: "dev@example.com", CustomTitle: str(""), AITitle: str(""), AgentName: str("")}}
		}),
		checks: checks(sessionIs("title", title, "what did the task do first_request/ok")),
	},
	{
		// Two identities sent a snapshot. The title is read from the one
		// observed last, whichever is listed first.
		name: "of several snapshots the latest observed is read",
		in: titled(
			Snapshot{ObservedAt: at(500), CustomTitle: str("Newer title")},
			Snapshot{ObservedAt: at(100), CustomTitle: str("Older title")},
		),
		checks: checks(sessionIs("title", title, "Newer title custom/ok")),
	},
	{
		// Observed at the same instant, the choice is still the same every
		// time: the greater user_id, then the greater profile_email.
		name: "of snapshots observed together the same one is always read",
		in: session().with(func(in *Input) {
			in.Snapshots = []Snapshot{
				{UserID: "user-1", ProfileEmail: "z@example.com", ObservedAt: at(100), CustomTitle: str("user-1 z")},
				{UserID: "user-2", ProfileEmail: "a@example.com", ObservedAt: at(100), CustomTitle: str("user-2 a")},
				{UserID: "user-2", ProfileEmail: "b@example.com", ObservedAt: at(100), CustomTitle: str("user-2 b")},
			}
		}),
		checks: checks(sessionIs("title", title, "user-2 b custom/ok")),
	},
}

func TestTitle(t *testing.T) { runCases(t, titleCases) }

func owners(s Session) string { return text(s.OwnerUserID) + " " + text(s.OwnerProfileEmail) }

// p1b-design §9.1, 소유.
var ownershipCases = []tcase{
	{
		name: "every row is one user's",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			reply("a1", "u1", 5),
		).event(6, "P1", 1).with(func(in *Input) {
			in.Snapshots = []Snapshot{{UserID: "user-1", ProfileEmail: "dev@example.com", LoginEmail: "dev@example.com", BillingProvider: "anthropic"}}
		}),
		checks: checks(
			sessionIs("owners", owners, "user-1 dev@example.com"),
			sessionIs("user ids", func(s Session) []string { return s.UserIDs }, []string{"user-1"}),
			sessionIs("login emails", func(s Session) []string { return s.LoginEmails }, []string{"dev@example.com"}),
			sessionIs("account keys", func(s Session) []AccountKey { return s.AccountKeys },
				[]AccountKey{{LoginEmail: "dev@example.com", BillingProvider: "anthropic"}}),
		),
	},
	{
		// A row with no user_id is a row its owner could not read either, so
		// the session is no one's.
		name: "an empty user_id among the rows leaves no owner",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			reply("a1", "u1", 5, func(r *ClaudeRow) { r.UserID = "" }),
		),
		checks: checks(
			sessionIs("owners", owners, "<null> dev@example.com"),
			sessionIs("user ids", func(s Session) []string { return s.UserIDs }, []string{"", "user-1"}),
		),
	},
	{
		name: "only empty user_ids leave no owner",
		in: session(
			typed("u1", "", 0, "P1", "question", func(r *ClaudeRow) { r.UserID = "" }),
		),
		checks: checks(
			sessionIs("owners", owners, "<null> dev@example.com"),
			sessionIs("user ids", func(s Session) []string { return s.UserIDs }, []string{""}),
		),
	},
	{
		// Telemetry sent without the managed settings carries Claude Code's own
		// anonymous id.
		name: "an api_request event under another user_id leaves no owner",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			reply("a1", "u1", 5),
		).event(6, "P1", 1).with(func(in *Input) { in.OTEL[0].UserID = "anon-9" }),
		checks: checks(
			sessionIs("owners", owners, "<null> dev@example.com"),
			sessionIs("user ids", func(s Session) []string { return s.UserIDs }, []string{"anon-9", "user-1"}),
		),
	},
	{
		// Each contributing row's account is kept, so a read can hide the
		// session when any of them is excluded later.
		name: "a snapshot under another profile and account",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			reply("a1", "u1", 5, func(r *ClaudeRow) { r.LoginEmailSource = "inferred" }),
		).with(func(in *Input) {
			in.Snapshots = []Snapshot{{UserID: "user-1", ProfileEmail: "other@example.com", LoginEmail: "alt@example.com", BillingProvider: "anthropic", AccountID: "acct-7"}}
		}),
		checks: checks(
			sessionIs("owners", owners, "user-1 <null>"),
			sessionIs("profile emails", func(s Session) []string { return s.ProfileEmails }, []string{"dev@example.com", "other@example.com"}),
			sessionIs("login emails", func(s Session) []string { return s.LoginEmails }, []string{"alt@example.com", "dev@example.com"}),
			sessionIs("account keys", func(s Session) []AccountKey { return s.AccountKeys }, []AccountKey{
				{LoginEmail: "alt@example.com", BillingProvider: "anthropic", AccountID: "acct-7"},
				{LoginEmail: "dev@example.com", BillingProvider: "anthropic"},
			}),
			sessionIs("login inferred", func(s Session) bool { return s.LoginEmailInferred }, true),
		),
	},
	{
		// One login under two billing providers and two accounts is three
		// accounts, in a fixed order.
		name: "account keys are distinct by all three members",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			reply("a1", "u1", 5, func(r *ClaudeRow) { r.BillingProvider = "bedrock" }),
			typed("u2", "a1", 10, "P2", "question", func(r *ClaudeRow) { r.AccountID = "acct-2" }),
			reply("a2", "u2", 15, func(r *ClaudeRow) { r.AccountID = "acct-2" }),
		),
		checks: checks(sessionIs("account keys", func(s Session) []AccountKey { return s.AccountKeys }, []AccountKey{
			{LoginEmail: "dev@example.com", BillingProvider: "anthropic"},
			{LoginEmail: "dev@example.com", BillingProvider: "anthropic", AccountID: "acct-2"},
			{LoginEmail: "dev@example.com", BillingProvider: "bedrock"},
		})),
	},
	{
		name:   "a session with no contributing row has no owner",
		in:     session(),
		checks: checks(sessionIs("owners", owners, "<null> <null>"), sessionIs("transcript", func(s Session) bool { return s.Transcript }, false)),
	},
}

func TestOwnership(t *testing.T) { runCases(t, ownershipCases) }

// p1b-design §9.1, 활동.
var activityCases = []tcase{
	{
		// Claude Code writes the away_summary on its own, an hour after the
		// person left: it is neither activity nor the end of the session.
		name: "an away_summary is outside the segments and the range",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			reply("a1", "u1", 60),
			system("w1", "a1", 3660, "away_summary"),
		),
		checks: checks(
			segments("0..60/2/1"),
			sessionIs("activity", activity, "0..60 first record 0"),
			sessionIs("active seconds", func(s Session) int { return s.ActiveSeconds }, 60),
		),
	},
	{
		// 899 seconds of silence is one sitting; 900 is two.
		name: "fifteen minutes of silence end a segment",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			reply("a1", "u1", 899),
			typed("u2", "a1", 1799, "P2", "back again"),
			reply("a2", "u2", 1800),
		),
		checks: checks(
			segments("0..899/2/1", "1799..1800/2/1"),
			sessionIs("active seconds", func(s Session) int { return s.ActiveSeconds }, 900),
		),
	},
	{
		// The metadata line has no timestamp or uuid of its own, and z0 has a
		// uuid but no timestamp: neither is in the tree, a segment or the range.
		// The telemetry starts the range and the subagent's lines end it, but
		// neither is in a segment; the abandoned branch is activity like any
		// other.
		name: "what a segment and the range take in",
		in: session(
			newRow("ai-title", "content-0123abcd", "", 0, func(r *ClaudeRow) { r.TS = time.Unix(0, 0).UTC() }),
			user("z0", "", 0, "a line with a uuid and no timestamp", preP0(), func(r *ClaudeRow) { r.TS = time.Unix(0, 0).UTC() }),
			typed("u1", "", 100, "P1", "question"),
			reply("a1", "u1", 105),
			typed("u2", "a1", 110, "P2", "rewound from here"),
			typed("u3", "a1", 120, "P3", "asked this instead"),
			user("su", "", 2000, "task", sidechain("ag-1"), prompt("P3")),
			reply("sa", "su", 2100, sidechain("ag-1")),
		).event(50, "P3", 1),
		checks: checks(
			turns("key branch", "u1 active", "u2 abandoned", "u3 active"),
			segments("100..120/4/2"),
			sessionIs("activity", activity, "50..2100 first record 100"),
		),
	},
	{
		// A queue-operation line has a timestamp and no uuid, so sync keys it by
		// a digest. It is no line of the conversation and is in no segment, but
		// it is a dated row of the session and within its range.
		name: "a line keyed by a digest is in the range and in no segment",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			reply("a1", "u1", 5),
			newRow("queue-operation", "content-89abcdef", "", 600),
			user("content-0f0f0f0f", "", 300, "a user line that came without a uuid", preP0()),
		),
		checks: checks(turns("key", "u1"), segments("0..5/2/1"), sessionIs("activity", activity, "0..600 first record 0")),
	},
	{
		// Main user and assistant rows stored before the uuid column cannot
		// reveal their turn boundaries, but remain session activity.
		name: "rows without a uuid make boundaries undeterminable",
		in: session(
			user("", "", 0, "an old question", preP0()),
			reply("", "", 5, preP0()),
		),
		checks: checks(turns("key"), turnCounts("null/null/null/null"),
			sessionIs("turn boundaries", func(s Session) string { return s.TurnBoundaries + " " + s.TurnBoundariesReason }, "undeterminable pre_lineage"),
			segments("0..5/2/0")),
	},
	{
		name: "a keyed turn cannot mask unkeyed main conversation rows",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			reply("a1", "u1", 5),
			user("", "", 10, "old question", preP0()),
			reply("", "", 15, preP0()),
		).request("a1", 1, priced(0.5)),
		checks: checks(turns("key"), turnCounts("null/null/null/null"),
			sessionIs("turn boundaries", func(s Session) string { return s.TurnBoundaries + " " + s.TurnBoundariesReason }, "undeterminable pre_lineage"),
			sessionIs("tokens", tokens, "1: 100/10/1000/5"),
			sessionIs("cost", sessionCost, "estimate=0.5 0+0.5"),
			segments("0..15/4/0")),
	},
	{
		name: "unkeyed subagent rows do not erase main turn boundaries",
		in: session(
			typed("u1", "", 0, "P1", "question"),
			reply("a1", "u1", 5),
			user("", "", 10, "subagent input", preP0(), sidechain("ag-1")),
		),
		checks: checks(turns("key", "u1"), turnCounts("1/1/1/0")),
	},
	{
		name:   "a segment of one line starts and ends at the same instant",
		in:     session(typed("u1", "", 42, "P1", "question")),
		checks: checks(segments("42..42/1/1"), sessionIs("active seconds", func(s Session) int { return s.ActiveSeconds }, 0)),
	},
	{
		name: "a session known only by its telemetry",
		in:   session().event(10, "P1", 1).event(20, "P1", 2),
		checks: checks(
			segments(),
			sessionIs("activity", activity, "10..20 first record null"),
			sessionIs("transcript", func(s Session) bool { return s.Transcript }, false),
			sessionIs("cost", sessionCost, "otel=3 0+3"),
			turnCounts("0/0/0/0"),
		),
	},
	{
		name:   "a session with nothing has no range",
		in:     session(),
		checks: checks(segments(), sessionIs("activity", activity, "null..null first record null"), sessionIs("cost", sessionCost, "none=null 0+0")),
	},
	{
		// Where the session ran is what its latest row says; the repository key
		// is the repository id, or the project hash without one.
		name: "project, repository key, branch and model",
		in: session(
			typed("u1", "", 0, "P1", "question", func(r *ClaudeRow) { r.Branch = "main" }),
			reply("a1", "u1", 5, model("claude-haiku-4-5")),
			typed("u2", "a1", 10, "P2", "after a checkout", func(r *ClaudeRow) { r.Branch, r.RepositoryID = "feature/login", "github.com/acme/app" }),
			reply("a2", "u2", 15, model("claude-opus-5"), func(r *ClaudeRow) { r.Branch = "" }),
			user("su", "", 16, "task", sidechain("ag-1"), prompt("P2"), func(r *ClaudeRow) { r.Branch = "feature/login" }),
			reply("sa", "su", 17, sidechain("ag-1"), model("claude-haiku-4-5"), func(r *ClaudeRow) { r.Branch = "" }),
		).request("a1", 1).request("a2", 1).request("sa", 1),
		checks: checks(
			sessionIs("place", func(s Session) string { return s.ProjectHash + " " + s.RepositoryKey + " " + s.Branch + " " + s.Model },
				"proj github.com/acme/app feature/login claude-opus-5"),
			sessionIs("agent", func(s Session) string { return s.Agent + " " + s.Coverage }, "claude full"),
			sessionIs("rules version", func(s Session) int { return s.RulesVersion }, baseRulesVersion),
		),
	},
	{
		name: "without a repository id the repository key is the project hash",
		in:   session(typed("u1", "", 0, "P1", "question")),
		checks: checks(sessionIs("place", func(s Session) string { return s.ProjectHash + " " + s.RepositoryKey + " " + s.Branch + "|" + s.Model },
			"proj proj main|")),
	},
}

func TestActivity(t *testing.T) { runCases(t, activityCases) }
