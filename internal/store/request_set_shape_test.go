package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"
)

// The request set's second shape (P1t-8). The read paths that sum many rows read
// attributedLinesSQL and claudeRequestHeadsSQL instead of one attributedRowsSQL
// subquery, attributedRowsSQL itself has three arms where it had two, and its
// window is one where it was two. None of that may move a number. The fixture
// carries every kind of row the rule tells apart: request lines in one, two and
// three lines, twins, an id collision, a line with no message id, one whose
// message_id the backfill has not reached, <synthetic>, no model, NULL and 0
// counts, requests with one side only, a request whose lines disagree on login,
// profile, model or project, ones whose first line a filter drops, one cut by
// the period, Codex usage rows with and without an imputed row, Codex's other
// rows, gjc and omo, an excluded session and an excluded account.
//
// Two kinds of test read it.
//
// The fragments' own invariants stay for good: attributedLinesSQL and
// claudeRequestHeadsSQL sum to attributedRowsSQL as it is now
// (LinesAndHeadsSumToTheRows), and attributedLinesUnionSQL is attributedLinesSQL
// under a select list and a WHERE (LinesUnionIsTheLinesUnderSelAndWhere).
//
// The comparisons with SQL frozen as it was at 7fec0d29 are one-time proofs
// that P1t-8 moved no number: the two-arm rows shape
// (AttributedRowsMatchTheTwoArmShape), what the date-filtered list, the usage
// aggregates and the session summaries return
// (ReadPathsMatchTheOneSubqueryShape), and the organization's projects
// (OrganizationProjectsMatchTheOneSubqueryShape). Delete a comparison, with the
// frozen SQL no other comparison reads, at the first change that means to change
// what its read returns. Never edit the frozen SQL to make one pass: a reference
// edited to agree proves nothing.
//
// The frozen shapes are the reference, not the rule: the rule is pinned against
// rsFixture.expect by request_set_sql_test.go and request_set_read_paths_test.go,
// which the new shapes still pass unchanged.

// attributedRowsSQLAt7fec0d29 is attributedRowsSQL as it was before P1t-8: the
// window arm with two windows, and one pass-through arm that joins
// codex_imputed_cost to every row.
func attributedRowsSQLAt7fec0d29(source, lineWhere string, cols []string) string {
	window := fmt.Sprintf(`SELECT %[1]s
		%[5]s AS req_key,
		row_number() OVER (w ORDER BY sr.ts, (sr.uuid = ''), sr.id) AS req_line,
		max(sr.input_tokens) OVER w AS req_input_tokens,
		max(sr.output_tokens) OVER w AS req_output_tokens,
		max(sr.cache_read_tokens) OVER w AS req_cache_read_tokens,
		max(sr.cache_create_tokens) OVER w AS req_cache_create_tokens
	FROM %[2]s sr
	WHERE %[3]s AND (%[4]s)
	WINDOW w AS (PARTITION BY sr.session_id, %[5]s)`,
		selectColumnsSQL("sr", cols), source, claudeRequestLineSQL("sr"), lineWhere, claudeRequestKeySQL("sr"))
	return fmt.Sprintf(`SELECT %[1]s
		a.req_key,
		CASE WHEN a.req_line = 1 THEN a.req_input_tokens ELSE 0 END AS req_input_tokens,
		CASE WHEN a.req_line = 1 THEN a.req_output_tokens ELSE 0 END AS req_output_tokens,
		CASE WHEN a.req_line = 1 THEN a.req_cache_read_tokens ELSE 0 END AS req_cache_read_tokens,
		CASE WHEN a.req_line = 1 THEN a.req_cache_create_tokens ELSE 0 END AS req_cache_create_tokens
	FROM (%[2]s) a
	UNION ALL
	SELECT %[3]s
		NULL::text,
		CASE WHEN sr.agent = 'codex' AND sr.record_type = 'usage' THEN COALESCE(rs_cic.input_tokens, 0)
			WHEN sr.agent = 'codex' THEN 0 ELSE sr.input_tokens END,
		CASE WHEN sr.agent = 'codex' AND sr.record_type = 'usage' THEN COALESCE(rs_cic.output_tokens, 0)
			WHEN sr.agent = 'codex' THEN 0 ELSE sr.output_tokens END,
		CASE WHEN sr.agent = 'codex' AND sr.record_type = 'usage' THEN COALESCE(rs_cic.cache_read_tokens, 0)
			WHEN sr.agent = 'codex' THEN 0 ELSE sr.cache_read_tokens END,
		CASE WHEN sr.agent = 'codex' AND sr.record_type = 'usage' THEN COALESCE(rs_cic.cache_create_tokens, 0)
			WHEN sr.agent = 'codex' THEN 0 ELSE sr.cache_create_tokens END
	FROM (SELECT * FROM %[4]s sr WHERE %[5]s IS NOT TRUE AND (%[6]s)) sr
	LEFT JOIN codex_imputed_cost rs_cic
		ON rs_cic.srec_id = sr.id AND sr.agent = 'codex' AND sr.record_type = 'usage'`,
		selectColumnsSQL("a", cols), window, selectColumnsSQL("sr", cols), source, claudeRequestLineSQL("sr"), lineWhere)
}

// shapeBase is a Monday; the fixture spans its first hours.
var shapeBase = time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)

// shapeCut falls between two lines of one request of sh-cut, so a period that
// starts there folds that request from its later line only.
var shapeCut = shapeBase.Add(2*time.Hour + 30*time.Second)

func shapeInt(n int) *int { return &n }

type shapeWho struct{ user, profile, login string }

var (
	shapeA = shapeWho{"uid-sh-a", "a@sh.example", "la@sh.example"}
	shapeB = shapeWho{"uid-sh-b", "b@sh.example", "lb@sh.example"}
	shapeC = shapeWho{"uid-sh-c", "c@sh.example", ""}
	shapeD = shapeWho{"uid-sh-d", "d@sh.example", "ld@sh.example"} // excluded account
)

// shapeRec is one session_records row of the fixture.
type shapeRec struct {
	session string
	at      time.Duration
	who     shapeWho
	agent   string
	recType string
	model   string
	msgID   string // raw message.id; none when empty
	uuid    string
	project string
	entry   string
	prompt  string
	fork    string
	account string
	raw     map[string]any
	in      *int
	out     *int
	cr      *int
	cw      *int
}

// shapeFixture stores the rows, the OTEL events, the exclusions, the projects
// and the weekly Claude rates, then the imputed Codex costs. Returns nothing:
// every test reads the database.
func shapeFixture(t *testing.T, s *PgStore) {
	t.Helper()
	ctx := context.Background()
	var recs []shapeRec
	add := func(r shapeRec) { recs = append(recs, r) }
	claude := func(session string, at time.Duration, who shapeWho, model, msg, uuid string, in, out, cr, cw *int) shapeRec {
		return shapeRec{session: session, at: at, who: who, agent: "claude", recType: "assistant", model: model,
			msgID: msg, uuid: uuid, project: "proj-sh-1", entry: "cli", in: in, out: out, cr: cr, cw: cw}
	}
	user := func(session string, at time.Duration, who shapeWho, uuid, prompt string) shapeRec {
		return shapeRec{session: session, at: at, who: who, agent: "claude", recType: "user", uuid: uuid,
			project: "proj-sh-1", entry: "cli", prompt: prompt}
	}

	// sh-otel: an OTEL session (its events below) with every request shape.
	add(user("sh-otel", 0, shapeA, "o-u1", "typed"))
	for i, out := range []int{1, 1, 523} {
		add(claude("sh-otel", time.Duration(10+i)*time.Second, shapeA, "m-sonnet", "msg_o_three",
			fmt.Sprintf("o-3-%d", i), shapeInt(40), shapeInt(out), shapeInt(9000), shapeInt(60)))
	}
	add(claude("sh-otel", 20*time.Second, shapeA, "m-sonnet", "msg_o_twin", "", shapeInt(70), shapeInt(5), nil, nil))
	add(claude("sh-otel", 20*time.Second, shapeA, "m-sonnet", "msg_o_twin", "o-twin", shapeInt(70), shapeInt(80), nil, nil))
	add(claude("sh-otel", 30*time.Second, shapeA, "m-sonnet", "msg_o_coll", "o-coll-1", shapeInt(100), shapeInt(50), shapeInt(5000), shapeInt(10)))
	add(claude("sh-otel", 31*time.Second, shapeA, "m-opus", "msg_o_coll", "o-coll-2", shapeInt(3000), shapeInt(20), shapeInt(10), shapeInt(300)))
	add(claude("sh-otel", 40*time.Second, shapeA, "m-sonnet", "", "o-noid", shapeInt(11), shapeInt(12), nil, nil))
	add(claude("sh-otel", 41*time.Second, shapeA, "<synthetic>", "msg_o_syn", "o-syn", shapeInt(0), shapeInt(0), nil, nil))
	add(claude("sh-otel", 50*time.Second, shapeA, "m-sonnet", "msg_o_inonly", "o-in", shapeInt(100), nil, nil, nil))
	add(claude("sh-otel", 51*time.Second, shapeA, "m-sonnet", "msg_o_outonly", "o-out", nil, shapeInt(200), nil, nil))

	// sh-offline: no OTEL; a headless session.
	for i := 0; i < 4; i++ {
		at := time.Hour + time.Duration(i)*time.Minute
		r := user("sh-offline", at, shapeB, fmt.Sprintf("f-u%d", i), "")
		r.entry = "sdk-cli"
		add(r)
		for l := 0; l < 1+i%3; l++ {
			c := claude("sh-offline", at+time.Duration(5+l)*time.Second, shapeB, "m-haiku", fmt.Sprintf("msg_f_%d", i),
				fmt.Sprintf("f-a%d-%d", i, l), shapeInt(200+i), shapeInt(1+300*l), shapeInt(100), shapeInt(0))
			c.entry = "sdk-cli"
			add(c)
		}
	}

	// sh-split: one request whose lines disagree on login, profile and model --
	// the head's count -- and one whose head has no login while its later line has.
	// The head of the first is B's, which sorts after A's: only the request's
	// tokens make B the session's leading account and profile.
	add(claude("sh-split", 90*time.Minute, shapeB, "m-opus", "msg_s_1", "s-1a", shapeInt(500), shapeInt(1), nil, nil))
	add(claude("sh-split", 90*time.Minute+time.Second, shapeA, "m-sonnet", "msg_s_1", "s-1b", shapeInt(500), shapeInt(700), nil, nil))
	add(claude("sh-split", 91*time.Minute, shapeC, "m-sonnet", "msg_s_2", "s-2a", shapeInt(60), shapeInt(1), nil, nil))
	add(claude("sh-split", 91*time.Minute+time.Second, shapeA, "m-sonnet", "msg_s_2", "s-2b", shapeInt(60), shapeInt(90), nil, nil))

	// sh-nulls: requests whose every count is NULL, and other rows with NULL counts.
	add(claude("sh-nulls", 100*time.Minute, shapeB, "m-haiku", "msg_n_1", "n-1", nil, nil, nil, nil))
	add(claude("sh-nulls", 101*time.Minute, shapeB, "m-haiku", "msg_n_2", "n-2", nil, nil, nil, nil))
	add(shapeRec{session: "sh-nulls", at: 102 * time.Minute, who: shapeB, agent: "claude", recType: "system", uuid: "n-sys"})

	// sh-cut: a request with a line on each side of shapeCut.
	add(claude("sh-cut", 2*time.Hour, shapeA, "m-sonnet", "msg_c_1", "c-1a", shapeInt(800), shapeInt(1), shapeInt(10), shapeInt(1)))
	add(claude("sh-cut", 2*time.Hour+time.Minute, shapeA, "m-sonnet", "msg_c_1", "c-1b", shapeInt(800), shapeInt(950), shapeInt(10), shapeInt(1)))
	add(claude("sh-cut", 2*time.Hour+2*time.Minute, shapeA, "m-sonnet", "msg_c_2", "c-2", shapeInt(30), shapeInt(40), nil, nil))

	// sh-msgnull: lines whose message_id the backfill has not reached (set below).
	add(claude("sh-msgnull", 110*time.Minute, shapeB, "m-haiku", "msg_mn_1", "mn-1", shapeInt(9), shapeInt(1), nil, nil))
	add(claude("sh-msgnull", 110*time.Minute+time.Second, shapeB, "m-haiku", "msg_mn_1", "mn-2", shapeInt(9), shapeInt(99), nil, nil))

	// Codex: growing snapshots, one repeat the defences drop, other Codex rows that
	// carry counts, and a session with no account.
	codex := func(session string, at time.Duration, who shapeWho, account string, k int, total int) shapeRec {
		return shapeRec{session: session, at: at, who: who, agent: "codex", recType: "usage", model: "gpt-5.5",
			uuid: fmt.Sprintf("%s-use-%d", session, k), project: "xproj-sh", entry: "cli", account: account,
			raw: map[string]any{"type": "event_msg", "payload": map[string]any{"type": "token_count", "info": map[string]any{
				"total_token_usage": map[string]any{"input_tokens": 1000 * total, "cached_input_tokens": 600 * total, "output_tokens": 50 * total}}}},
			in: shapeInt(1000), out: shapeInt(50), cr: shapeInt(600)}
	}
	for k, total := range []int{1, 2, 2, 3, 4} {
		add(codex("sh-codex", 3*time.Hour+time.Duration(k)*time.Minute, shapeA, "acct-sh-1", k, total))
		add(shapeRec{session: "sh-codex", at: 3*time.Hour + time.Duration(k)*time.Minute - 10*time.Second, who: shapeA,
			agent: "codex", recType: "message", model: "gpt-5.5", uuid: fmt.Sprintf("sh-codex-msg-%d", k), project: "xproj-sh",
			entry: "cli", account: "acct-sh-1", in: shapeInt(77), out: shapeInt(7)})
	}
	for k := 0; k < 3; k++ {
		r := codex("sh-codex-noacct", 3*time.Hour+30*time.Minute+time.Duration(k)*time.Minute, shapeB, "", k, k+1)
		r.entry = "codex_exec"
		add(r)
	}
	// sh-mixed: Claude lines under A's login and Codex rows whose account id is
	// that login, so one account key comes from two agents. Those Codex rows are
	// written under B's login, which sorts after A's: the key is not the group's
	// MAX(login_email), and only the key finds the request's tokens. With them the
	// account leads the session's second Codex account; without them it does not.
	for k := 0; k < 2; k++ {
		add(codex("sh-mixed", 4*time.Hour+time.Duration(k)*time.Minute, shapeB, shapeA.login, k, k+1))
		add(codex("sh-mixed", 4*time.Hour+time.Duration(2+k)*time.Minute, shapeA, "acct-sh-2", 2+k, k+1))
	}
	add(claude("sh-mixed", 4*time.Hour+5*time.Minute, shapeA, "m-sonnet", "msg_x_1", "x-1", shapeInt(10), shapeInt(20), nil, nil))
	add(claude("sh-mixed", 4*time.Hour+5*time.Minute+time.Second, shapeA, "m-sonnet", "msg_x_1", "x-2", shapeInt(10), shapeInt(25), nil, nil))

	// gjc and omo keep their own counts.
	add(shapeRec{session: "sh-gjc", at: 5 * time.Hour, who: shapeB, agent: "gjc", recType: "assistant", model: "glm-5",
		uuid: "g-1", project: "proj-sh-2", in: shapeInt(300), out: shapeInt(30)})
	add(shapeRec{session: "sh-gjc", at: 5*time.Hour + time.Minute, who: shapeB, agent: "omo", recType: "assistant", model: "kimi",
		uuid: "g-2", project: "proj-sh-2", in: shapeInt(400), out: shapeInt(40)})

	// sh-fork forks off sh-otel; sh-gone is excluded; sh-hidden is the excluded account's.
	f := claude("sh-fork", 6*time.Hour, shapeA, "m-sonnet", "msg_fk_1", "fk-1", shapeInt(3), shapeInt(4), nil, nil)
	f.fork = "sh-otel"
	add(f)
	add(claude("sh-gone", 6*time.Hour+time.Minute, shapeA, "m-sonnet", "msg_g_1", "gone-1", shapeInt(5), shapeInt(6), nil, nil))
	add(claude("sh-hidden", 6*time.Hour+2*time.Minute, shapeD, "m-sonnet", "msg_h_1", "hid-1", shapeInt(7), shapeInt(8), nil, nil))

	// sh-inlead and sh-outlead: B leads A in each, 900 tokens to 600. In the first
	// B's requests carry input alone and A's output alone, in the second the other
	// way round. A sum that goes NULL where a side is missing drops that login's
	// requests from the ranking, and A, the first by name, leads instead.
	add(claude("sh-inlead", 8*time.Hour, shapeB, "m-haiku", "msg_il_1", "il-1", shapeInt(500), nil, nil, nil))
	add(claude("sh-inlead", 8*time.Hour+time.Second, shapeB, "m-haiku", "msg_il_2", "il-2", shapeInt(400), nil, nil, nil))
	add(claude("sh-inlead", 8*time.Hour+2*time.Second, shapeA, "m-haiku", "msg_il_3", "il-3", nil, shapeInt(600), nil, nil))
	add(claude("sh-outlead", 8*time.Hour+time.Minute, shapeB, "m-haiku", "msg_ol_1", "ol-1", nil, shapeInt(900), nil, nil))
	add(claude("sh-outlead", 8*time.Hour+time.Minute+time.Second, shapeA, "m-haiku", "msg_ol_2", "ol-2", shapeInt(600), nil, nil, nil))

	// sh-nomodel: a request whose lines name no model, and a row that is not a
	// request line and counts tokens under none; the usage read lists both as
	// unknown. sh-zero: m-zero's requests count nothing, 0 in one and NULL in the
	// other, so the usage read has no row for that model.
	add(claude("sh-nomodel", 8*time.Hour+10*time.Minute, shapeB, "", "msg_nm_1", "nm-1", shapeInt(13), shapeInt(1), nil, nil))
	add(claude("sh-nomodel", 8*time.Hour+10*time.Minute+time.Second, shapeB, "", "msg_nm_1", "nm-2", shapeInt(13), shapeInt(17), nil, nil))
	add(shapeRec{session: "sh-nomodel", at: 8*time.Hour + 10*time.Minute + 2*time.Second, who: shapeB, agent: "claude", recType: "user",
		uuid: "nm-u", project: "proj-sh-1", entry: "cli", in: shapeInt(5), out: shapeInt(2)})
	add(claude("sh-zero", 8*time.Hour+20*time.Minute, shapeB, "m-zero", "msg_z_1", "z-1", shapeInt(0), shapeInt(0), nil, nil))
	add(claude("sh-zero", 8*time.Hour+20*time.Minute+time.Second, shapeB, "m-zero", "msg_z_1", "z-2", shapeInt(0), shapeInt(0), nil, nil))
	add(claude("sh-zero", 8*time.Hour+21*time.Minute, shapeB, "m-zero", "msg_z_2", "z-3", nil, nil, nil, nil))

	// The organization's projects; the projects table is filled below. Two
	// checkouts of one repository, proj-sh-repo-1 and -2, the second's id stored
	// with spaces around it. sh-span: a request whose lines are in two projects,
	// the larger output on the line that is not its head. sh-noproj and sh-anon: a
	// request whose first line has no project, and one whose first line has no
	// contributor, each folded from its second line. sh-reqonly: a project with
	// request lines alone, and no projects row. sh-local: local repository ids,
	// one in upper case, which merge nothing. sh-both-*: one hash under two
	// agents, each with a repository of its own.
	in := func(project string, r shapeRec) shapeRec { r.project = project; return r }
	add(in("proj-sh-repo-1", claude("sh-span", 9*time.Hour, shapeA, "m-sonnet", "msg_sp_1", "sp-1", shapeInt(100), shapeInt(10), nil, nil)))
	add(in("proj-sh-local-1", claude("sh-span", 9*time.Hour+time.Second, shapeA, "m-sonnet", "msg_sp_1", "sp-2", shapeInt(100), shapeInt(400), nil, nil)))
	add(in("", claude("sh-noproj", 9*time.Hour+10*time.Minute, shapeB, "m-haiku", "msg_np_1", "np-1", shapeInt(50), shapeInt(5), nil, nil)))
	add(in("proj-sh-repo-2", claude("sh-noproj", 9*time.Hour+10*time.Minute+time.Second, shapeB, "m-haiku", "msg_np_1", "np-2", shapeInt(50), shapeInt(70), nil, nil)))
	add(in("proj-sh-local-2", claude("sh-anon", 9*time.Hour+20*time.Minute, shapeWho{}, "m-sonnet", "msg_an_1", "an-1", shapeInt(30), shapeInt(3), nil, nil)))
	add(in("proj-sh-local-2", claude("sh-anon", 9*time.Hour+20*time.Minute+time.Second, shapeC, "m-sonnet", "msg_an_1", "an-2", shapeInt(30), shapeInt(80), nil, nil)))
	add(in("proj-sh-unlisted", claude("sh-reqonly", 9*time.Hour+30*time.Minute, shapeB, "m-haiku", "msg_ro_1", "ro-1", shapeInt(20), shapeInt(2), nil, nil)))
	add(in("proj-sh-unlisted", claude("sh-reqonly", 9*time.Hour+30*time.Minute+time.Second, shapeB, "m-haiku", "msg_ro_1", "ro-2", shapeInt(20), shapeInt(44), nil, nil)))
	add(in("proj-sh-local-1", claude("sh-local", 9*time.Hour+40*time.Minute, shapeB, "m-haiku", "msg_lc_1", "lc-1", shapeInt(7), shapeInt(9), nil, nil)))
	add(in("proj-sh-both", claude("sh-both-claude", 9*time.Hour+50*time.Minute, shapeA, "m-sonnet", "msg_bc_1", "bc-1", shapeInt(40), shapeInt(4), nil, nil)))
	add(in("proj-sh-both", claude("sh-both-claude", 9*time.Hour+50*time.Minute+time.Second, shapeA, "m-sonnet", "msg_bc_1", "bc-2", shapeInt(40), shapeInt(60), nil, nil)))
	add(in("proj-sh-both", codex("sh-both-codex", 9*time.Hour+55*time.Minute, shapeB, "acct-sh-1", 0, 1)))

	records := make([]*SessionRecord, 0, len(recs))
	for i, r := range recs {
		raw := map[string]any{"tag": fmt.Sprintf("sh-%d", i)}
		for k, v := range r.raw {
			raw[k] = v
		}
		if r.msgID != "" {
			raw["message"] = map[string]any{"id": r.msgID}
		}
		b, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		bp := "anthropic"
		if r.agent == "codex" {
			bp = "openai"
		}
		records = append(records, &SessionRecord{
			Ts: shapeBase.Add(r.at), SessionID: r.session, ProjectHash: r.project, RecordType: r.recType,
			ProfileEmail: r.who.profile, LoginEmail: r.who.login, UserID: r.who.user, Model: r.model,
			InputTokens: r.in, OutputTokens: r.out, CacheReadTokens: r.cr, CacheCreateTokens: r.cw,
			Raw: json.RawMessage(b), Agent: r.agent, BillingProvider: bp, UUID: r.uuid,
			ForkedFromSession: r.fork, SourceFile: r.session + ".jsonl", PromptSource: r.prompt,
			Entrypoint: r.entry, CctraceVersion: "v-sh", AccountID: r.account,
		})
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatalf("insert: %v", err)
	}

	otel := func(at time.Duration, name string, who shapeWho, model string, in, out int, cost float64) *OtelEvent {
		return &OtelEvent{Ts: shapeBase.Add(at), EventName: name, SessionID: "sh-otel", UserID: who.user,
			ProfileEmail: who.profile, LoginEmail: who.login, Model: model, CostUSD: &cost,
			InputTokens: &in, OutputTokens: &out, Agent: "claude", BillingProvider: "anthropic", ServiceVersion: "2.0.1"}
	}
	events := []*OtelEvent{
		otel(12*time.Second, "api_request", shapeA, "m-sonnet", 40, 523, 0.01),
		otel(32*time.Second, "api_request", shapeA, "m-opus", 3000, 50, 0.02),
		otel(52*time.Second, "tool_result", shapeA, "", 0, 0, 0),
	}
	only := otel(7*time.Hour, "api_request", shapeB, "m-sonnet", 15, 25, 0.005)
	only.SessionID = "sh-otel-only"
	events = append(events, only)
	if err := s.InsertEvents(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	for _, stmt := range []string{
		`UPDATE session_records SET message_id = NULL WHERE session_id = 'sh-msgnull'`,
		`UPDATE session_records SET login_email_source = 'inferred' WHERE session_id = 'sh-offline' AND record_type = 'assistant'`,
		`INSERT INTO excluded_sessions (session_id) VALUES ('sh-gone')`,
		`INSERT INTO excluded_accounts (login_email, reason, created_by) VALUES ('` + shapeD.login + `', 'shape', 'shape')`,
		`INSERT INTO projects (project_hash, project_name, agent, repository_id) VALUES
			('proj-sh-1', 'Shape One', 'claude', ''), ('proj-sh-2', 'Shape Two', 'gjc', ''), ('xproj-sh', 'Shape Codex', 'codex', ''),
			('proj-sh-repo-1', 'Shape Repo', 'claude', 'example.com/sh/repo'),
			('proj-sh-repo-2', 'Shape Repo Worktree', 'claude', ' example.com/sh/repo '),
			('proj-sh-local-1', 'Shape Local', 'claude', 'local:sh:checkout'),
			('proj-sh-local-2', 'Shape Local Copy', 'claude', 'LOCAL:sh:checkout'),
			('proj-sh-both', 'Shape Both', 'claude', 'example.com/sh/both-claude'),
			('proj-sh-both', 'Shape Both', 'codex', 'example.com/sh/both-codex')
			ON CONFLICT DO NOTHING`,
	} {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	// Weekly rates for the models whose requests count tokens, as rpRate gives
	// rpRated its own: RefreshClaudeImputedCost then prices the offline sessions.
	for _, model := range []string{"m-sonnet", "m-opus", "m-haiku"} {
		if _, err := s.pool.Exec(ctx, `INSERT INTO model_rate_buckets (model, bucket_start, scale, sample_rows)
			VALUES ($1, $2::date, $3, 20)`, model, shapeBase.Format(time.DateOnly), rpScale); err != nil {
			t.Fatalf("seed the rate bucket of %s: %v", model, err)
		}
	}
	if err := s.RefreshCodexImputedCost(ctx); err != nil {
		t.Fatalf("RefreshCodexImputedCost: %v", err)
	}
}

// shapeRows reads a fragment's rows as id -> "req_key|tokens", for the sources
// and lineWheres the tests try.
func shapeRows(t *testing.T, s *PgStore, sql string, args ...any) map[int64]string {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), `SELECT id, req_key, req_input_tokens, req_output_tokens,
		req_cache_read_tokens, req_cache_create_tokens FROM (`+sql+`) x`, args...)
	if err != nil {
		t.Fatalf("query: %v\n%s", err, sql)
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var key *string
		var v rsTokens
		if err := rows.Scan(&id, &key, &v[0], &v[1], &v[2], &v[3]); err != nil {
			t.Fatal(err)
		}
		k := "NULL"
		if key != nil {
			k = *key
		}
		if _, dup := out[id]; dup {
			t.Fatalf("row %d returned twice", id)
		}
		out[id] = k + "|" + v.String()
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// shapeLineWheres are the scopes the fragment tests fold the fixture over: all,
// a period that cuts a request, identities, and a source that answers NULL for
// gjc's agent.
var shapeLineWheres = []struct {
	name, source, lineWhere string
	args                    []any
}{
	{"all of session_records", "session_records", "TRUE", nil},
	{"visible rows", "visible_session_records", "TRUE", nil},
	{"a period that cuts a request", "visible_session_records", "sr.ts >= $1 AND sr.ts < $2",
		[]any{shapeCut, shapeBase.Add(24 * time.Hour)}},
	{"one user", "visible_session_records", "sr.user_id = $1", []any{shapeA.user}},
	{"one login over a period", "visible_session_records", "sr.login_email = $1 AND sr.ts >= $2",
		[]any{shapeB.login, shapeBase}},
	{"a NULL agent", `(SELECT id, session_id, ts, uuid, message_id, raw, model, record_type, NULLIF(agent, 'gjc') AS agent,
		input_tokens, output_tokens, cache_read_tokens, cache_create_tokens, login_email, user_id FROM session_records)`, "TRUE", nil},
}

// The three-arm attributedRowsSQL gives every row the key and the tokens the
// two-arm shape gave it, over every scope.
func TestRequestSetShape_AttributedRowsMatchTheTwoArmShape(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	shapeFixture(t, s)
	for _, c := range shapeLineWheres {
		was := shapeRows(t, s, attributedRowsSQLAt7fec0d29(c.source, c.lineWhere, []string{"id"}), c.args...)
		got := shapeRows(t, s, attributedRowsSQL(c.source, c.lineWhere, []string{"id"}), c.args...)
		if len(was) == 0 {
			t.Fatalf("%s: the reference shape returned no rows", c.name)
		}
		for id, w := range was {
			if g, ok := got[id]; !ok || g != w {
				t.Errorf("%s: row %d = %q, want %q", c.name, id, g, w)
			}
		}
		for id, g := range got {
			if _, ok := was[id]; !ok {
				t.Errorf("%s: row %d = %q is not in the reference shape", c.name, id, g)
			}
		}
	}
}

// attributedLinesSQL has attributedRowsSQL's rows with every request line at 0,
// and those plus claudeRequestHeadsSQL's requests sum to attributedRowsSQL's
// tokens by session, by session and login, and by model. Held against
// attributedRowsSQL as it is now, not a frozen copy: this is the invariant a
// read that takes the two fragments relies on, for as long as one does.
func TestRequestSetShape_LinesAndHeadsSumToTheRows(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	shapeFixture(t, s)
	ctx := context.Background()
	for _, c := range shapeLineWheres {
		cols := []string{"id", "session_id", "login_email", "model", "record_type", "agent"}
		attributed := attributedRowsSQL(c.source, c.lineWhere, cols)

		// Row for row: the same rows, the request lines -- the rows with a request
		// key -- at 0, every other row as it is there.
		q := `SELECT w.id, l.id IS NOT NULL,
				w.req_key IS NOT NULL,
				w.req_input_tokens IS NOT DISTINCT FROM l.req_input_tokens
					AND w.req_output_tokens IS NOT DISTINCT FROM l.req_output_tokens
					AND w.req_cache_read_tokens IS NOT DISTINCT FROM l.req_cache_read_tokens
					AND w.req_cache_create_tokens IS NOT DISTINCT FROM l.req_cache_create_tokens,
				COALESCE(l.req_input_tokens, -1) = 0 AND COALESCE(l.req_output_tokens, -1) = 0
					AND COALESCE(l.req_cache_read_tokens, -1) = 0 AND COALESCE(l.req_cache_create_tokens, -1) = 0
			FROM (` + attributed + `) w
			FULL JOIN (` + attributedLinesSQL(c.source, c.lineWhere, cols) + `) l ON l.id = w.id`
		rows, err := s.pool.Query(ctx, q, c.args...)
		if err != nil {
			t.Fatalf("%s: %v\n%s", c.name, err, q)
		}
		n := 0
		for rows.Next() {
			var id *int64
			var inLines, isRequest, same, zero bool
			if err := rows.Scan(&id, &inLines, &isRequest, &same, &zero); err != nil {
				t.Fatal(err)
			}
			n++
			switch {
			case id == nil || !inLines:
				t.Errorf("%s: row %v is in one shape only", c.name, id)
			case isRequest && !zero:
				t.Errorf("%s: request line %d is not 0 in the lines shape", c.name, *id)
			case !isRequest && !same:
				t.Errorf("%s: row %d changed its tokens in the lines shape", c.name, *id)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Fatalf("%s: no rows", c.name)
		}

		// Sums: lines plus heads against the rows, by three groupings.
		heads := claudeRequestHeadsSQL(c.source, c.lineWhere, []string{"session_id", "login_email", "model"})
		lines := attributedLinesSQL(c.source, c.lineWhere, cols)
		for _, key := range []string{"session_id", "session_id || '|' || COALESCE(login_email, '<null>')", "COALESCE(model, '<null>')"} {
			sum := func(sql, inCol, outCol string) map[string]string {
				rows, err := s.pool.Query(ctx, `SELECT `+key+` AS k, SUM(`+inCol+`), SUM(`+outCol+`)
					FROM (`+sql+`) x GROUP BY 1`, c.args...)
				if err != nil {
					t.Fatalf("%s by %s: %v", c.name, key, err)
				}
				defer rows.Close()
				out := map[string]string{}
				for rows.Next() {
					var k string
					var a, b *int64
					if err := rows.Scan(&k, &a, &b); err != nil {
						t.Fatal(err)
					}
					out[k] = fmt.Sprintf("%d/%d", rsVal(a), rsVal(b))
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				return out
			}
			want := sum(attributed, "req_input_tokens", "req_output_tokens")
			gotLines := sum(lines, "req_input_tokens", "req_output_tokens")
			gotHeads := sum(heads, "input_tokens", "output_tokens")
			for k, w := range want {
				var a, b, ha, hb int64
				fmt.Sscanf(gotLines[k], "%d/%d", &a, &b)
				fmt.Sscanf(gotHeads[k], "%d/%d", &ha, &hb)
				if got := fmt.Sprintf("%d/%d", a+ha, b+hb); got != w {
					t.Errorf("%s by %s, %s: lines %s + heads %s = %s, want %s", c.name, key, k, gotLines[k], gotHeads[k], got, w)
				}
			}
			for k := range gotHeads {
				if _, ok := want[k]; !ok {
					t.Errorf("%s by %s: heads add a group %s the rows do not have", c.name, key, k)
				}
			}
		}
	}
}

// attributedLinesUnionSQL is attributedLinesSQL under the caller's select list
// and WHERE, arm by arm: the same rows, each once.
func TestRequestSetShape_LinesUnionIsTheLinesUnderSelAndWhere(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	shapeFixture(t, s)
	cols := []string{"id", "session_id", "login_email", "model", "record_type", "agent"}
	sel := `id, COALESCE(login_email, '') || '/' || COALESCE(model, '') AS who,
		req_input_tokens, req_output_tokens, req_cache_read_tokens, req_cache_create_tokens`
	for _, c := range shapeLineWheres {
		for _, where := range []string{"", "sr.session_id NOT IN ('sh-otel', 'sh-codex')"} {
			cond := ""
			if where != "" {
				cond = " WHERE " + where
			}
			read := func(sql string) map[int64]string {
				rows, err := s.pool.Query(context.Background(), `SELECT id, who, req_input_tokens, req_output_tokens,
					req_cache_read_tokens, req_cache_create_tokens FROM (`+sql+`) x`, c.args...)
				if err != nil {
					t.Fatalf("%s: %v\n%s", c.name, err, sql)
				}
				defer rows.Close()
				out := map[int64]string{}
				for rows.Next() {
					var id int64
					var who string
					var v rsTokens
					if err := rows.Scan(&id, &who, &v[0], &v[1], &v[2], &v[3]); err != nil {
						t.Fatal(err)
					}
					if _, dup := out[id]; dup {
						t.Fatalf("%s: row %d returned twice", c.name, id)
					}
					out[id] = who + " " + v.String()
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				return out
			}
			want := read(`SELECT ` + sel + ` FROM (` + attributedLinesSQL(c.source, c.lineWhere, cols) + `) sr` + cond)
			got := read(attributedLinesUnionSQL(c.source, c.lineWhere, cols, sel, where))
			if len(want) == 0 {
				t.Fatalf("%s %q: no rows", c.name, where)
			}
			for id, w := range want {
				if g, ok := got[id]; !ok || g != w {
					t.Errorf("%s %q: row %d = %q, want %q", c.name, where, id, g, w)
				}
			}
			for id := range got {
				if _, ok := want[id]; !ok {
					t.Errorf("%s %q: row %d is extra", c.name, where, id)
				}
			}
		}
	}
}

// The three read paths answer what their one-subquery shapes answered, frozen
// below as they were at 7fec0d29, over every filter they take. Read before and
// after the offline sessions' imputed rows exist, since those enter the events
// side of the list and the usage aggregates.
//
// Everything is compared as JSON text but the list's cost, which is compared
// within 1e-12 dollars. A session's cost is a sum of doubles, added in the order
// its plan reads them, and the two statements need not read them in one order.
// A Claude rate that is a power of two does not make that sum exact here: the
// Codex imputed costs and the OTEL costs it is added to are not dyadic.
func TestRequestSetShape_ReadPathsMatchTheOneSubqueryShape(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	shapeFixture(t, s)
	ctx := context.Background()
	whole := [2]time.Time{shapeBase.Add(-time.Hour), shapeBase.Add(24 * time.Hour)}
	cut := [2]time.Time{shapeCut, shapeBase.Add(24 * time.Hour)}
	identities := []SessionOverviewFilter{{}, {UserID: shapeA.user}, {ProfileEmail: shapeB.profile},
		{LoginEmail: shapeA.login}, {LoginEmail: shapeB.login}}
	same := func(what string, got, want any) {
		t.Helper()
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(want)
		if string(g) != string(w) {
			t.Errorf("%s:\n got  %s\n want %s", what, g, w)
		}
	}
	sameList := func(what string, got, want []*SessionOverview) {
		t.Helper()
		for i := 0; i < len(got) && i < len(want); i++ {
			if math.Abs(got[i].CostUSD-want[i].CostUSD) <= 1e-12 {
				got[i].CostUSD = want[i].CostUSD
			}
		}
		same(what, got, want)
	}
	check := func(stage string) {
		for _, period := range [][2]time.Time{whole, cut} {
			for _, id := range identities {
				for _, extra := range []SessionOverviewFilter{{}, {FoldLineage: true}, {Source: "interactive"},
					{Source: "headless"}, {ProjectHashes: []string{"proj-sh-1", "xproj-sh"}}, {Agent: "codex"}, {Agent: "other"}} {
					f := id
					since, until := period[0], period[1]
					f.Since, f.Until, f.Limit = &since, &until, 1000
					f.FoldLineage, f.Source, f.ProjectHashes, f.Agent = extra.FoldLineage, extra.Source, extra.ProjectHashes, extra.Agent
					what := fmt.Sprintf("%s: %+v", stage, f)
					got, err := s.ListSessionOverviews(ctx, f)
					if err != nil {
						t.Fatalf("%s: list: %v", what, err)
					}
					want, err := listSessionOverviewsAt7fec0d29(ctx, s, f)
					if err != nil {
						t.Fatalf("%s: frozen list: %v", what, err)
					}
					if len(want) == 0 && extra.Agent == "" && extra.Source == "" && !extra.FoldLineage && f.ProjectHashes == nil {
						t.Errorf("%s: the frozen list is empty", what)
					}
					sameList("list "+what, got, want)
					if extra.FoldLineage || extra.Source != "" || extra.Agent != "" {
						continue
					}
					gotU, err := s.UsageAggregates(ctx, f)
					if err != nil {
						t.Fatalf("%s: usage: %v", what, err)
					}
					wantU, err := usageAggregatesAt7fec0d29(ctx, s, f)
					if err != nil {
						t.Fatalf("%s: frozen usage: %v", what, err)
					}
					same("usage "+what, gotU, wantU)
				}
			}
		}
		for _, c := range []struct {
			profile, user string
			limit         int
		}{{"", "", 100}, {"", shapeA.user, 100}, {shapeB.profile, "", 100}, {"", shapeB.user, 2}, {"", "", 1}} {
			what := fmt.Sprintf("%s: summaries %+v", stage, c)
			got, err := s.ListSessionSummaries(ctx, c.profile, c.user, c.limit)
			if err != nil {
				t.Fatalf("%s: %v", what, err)
			}
			want, err := listSessionSummariesAt7fec0d29(ctx, s, c.profile, c.user, c.limit)
			if err != nil {
				t.Fatalf("%s: frozen: %v", what, err)
			}
			if len(want) == 0 {
				t.Errorf("%s: the frozen summaries are empty", what)
			}
			same(what, got, want)
		}
	}
	check("before the Claude imputation")
	if err := s.RefreshClaudeImputedCost(ctx); err != nil {
		t.Fatalf("RefreshClaudeImputedCost: %v", err)
	}
	var imputed int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM claude_imputed_cost`).Scan(&imputed); err != nil {
		t.Fatalf("count claude_imputed_cost: %v", err)
	}
	if imputed == 0 {
		t.Fatal("RefreshClaudeImputedCost wrote no row: the second reading would repeat the first")
	}
	check("after the Claude imputation")
}

// listSessionOverviewsAt7fec0d29 is ListSessionOverviews' date-filtered query as
// it was at 7fec0d29: attributedRowsSQL as one subquery under session_union.
func listSessionOverviewsAt7fec0d29(ctx context.Context, s *PgStore, f SessionOverviewFilter) ([]*SessionOverview, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	var conds, srConds []string
	var args []interface{}
	n := 0
	if f.UserID != "" {
		n++
		conds = append(conds, fmt.Sprintf("e.user_id = $%d", n))
		srConds = append(srConds, fmt.Sprintf("sr.user_id = $%d", n))
		args = append(args, f.UserID)
	} else if f.ProfileEmail != "" {
		n++
		conds = append(conds, fmt.Sprintf("e.profile_email = $%d", n))
		srConds = append(srConds, fmt.Sprintf("sr.profile_email = $%d", n))
		args = append(args, f.ProfileEmail)
	} else if f.LoginEmail != "" {
		n++
		conds = append(conds, fmt.Sprintf("e.login_email = $%d", n))
		srConds = append(srConds, fmt.Sprintf("sr.login_email = $%d", n))
		args = append(args, f.LoginEmail)
	}
	if f.Since != nil {
		n++
		conds = append(conds, fmt.Sprintf("e.ts >= $%d", n))
		srConds = append(srConds, fmt.Sprintf("sr.ts >= $%d", n))
		args = append(args, *f.Since)
	}
	if f.Until != nil {
		n++
		conds = append(conds, fmt.Sprintf("e.ts < $%d", n))
		srConds = append(srConds, fmt.Sprintf("sr.ts < $%d", n))
		args = append(args, *f.Until)
	}
	whereClause := "WHERE e.session_id != '' AND NOT EXISTS (" +
		"SELECT 1 FROM excluded_sessions xs WHERE xs.session_id = e.session_id)"
	if len(conds) > 0 {
		whereClause += " AND " + strings.Join(conds, " AND ")
	}
	srLineWhere := "TRUE"
	if len(srConds) > 0 {
		srLineWhere = strings.Join(srConds, " AND ")
	}
	srSource := attributedRowsSQLAt7fec0d29("visible_session_records", srLineWhere, []string{
		"session_id", "profile_email", "user_id", "login_email", "model", "agent", "ts", "source_file",
		"prompt_source", "entrypoint", "forked_from_session", "project_hash", "cctrace_version",
		"account_id", "login_email_source"})
	n++
	limitClause := fmt.Sprintf("$%d", n)
	args = append(args, limit)
	n++
	offsetClause := fmt.Sprintf("$%d", n)
	args = append(args, offset)
	var wheres []string
	if f.FoldLineage {
		wheres = append(wheres, "NOT has_fork")
	}
	wheres = append(wheres, "(has_api_request OR has_sync)")
	wheres = append(wheres, sessionOverviewFilters(f.Source)...)
	if len(f.ProjectHashes) > 0 {
		n++
		wheres = append(wheres, fmt.Sprintf("project_hash = ANY($%d)", n))
		args = append(args, f.ProjectHashes)
	}
	if clause := appendAgentFilter(f.Agent, &n, &args); clause != "" {
		wheres = append(wheres, clause)
	}
	whereFilter := "WHERE " + strings.Join(wheres, " AND ")
	q := fmt.Sprintf(`WITH session_union AS (
		SELECT session_id, profile_email, user_id, login_email, model, agent, ts,
			'otel' AS src, ''::text AS source_file, ''::text AS prompt_source, ''::text AS entrypoint, ''::text AS forked_from_session, ''::text AS project_hash,
			input_tokens, output_tokens, cost_usd::double precision AS cost_usd, event_name,
			''::text AS cctrace_version, service_version AS claude_version, ''::text AS account_id,
			''::text AS login_email_source
		FROM visible_events e %s
		UNION ALL
		SELECT session_id, profile_email, COALESCE(user_id,''), COALESCE(login_email,''), COALESCE(model,''), COALESCE(agent,'claude'), ts,
			'srec', COALESCE(source_file,''), COALESCE(prompt_source,''), COALESCE(entrypoint,''), COALESCE(forked_from_session,''), COALESCE(project_hash,''),
			req_input_tokens, req_output_tokens, 0::double precision, ''::text AS event_name,
			COALESCE(cctrace_version,''), ''::text AS claude_version, COALESCE(account_id,''),
			COALESCE(login_email_source,'')
		FROM (%s) sr WHERE sr.session_id != ''
	),
	acct AS (
		SELECT session_id,
			CASE WHEN agent = 'codex' THEN account_id ELSE login_email END AS account_key,
			SUM(COALESCE(input_tokens,0) + COALESCE(output_tokens,0)) AS toks,
			MAX(login_email) AS login_email
		FROM session_union
		WHERE COALESCE(NULLIF(CASE WHEN agent = 'codex' THEN account_id ELSE login_email END, ''), '') <> ''
		GROUP BY session_id, 2
	),
	top_acct AS (
		SELECT DISTINCT ON (session_id) session_id, login_email
		FROM acct ORDER BY session_id, toks DESC, account_key
	),
	acct_count AS (
		SELECT session_id, COUNT(*) AS account_count FROM acct GROUP BY session_id
	),
	grouped AS (
		SELECT
			s.session_id,
			MAX(s.profile_email) AS profile_email,
			COALESCE(MAX(s.user_id), '') AS user_id,
			COALESCE(MAX(ta.login_email), '') AS login_email,
			COALESCE(MAX(ac.account_count), 0) AS account_count,
			COALESCE(MAX(CASE WHEN s.model != '' THEN s.model END), '') AS model,
			COALESCE(MAX(s.agent), 'claude') AS agent,
			`+sessionStartExpr("s.ts")+` AS start_time,
			`+sessionEndExpr("s.ts")+` AS end_time,
			COALESCE(NULLIF(SUM(s.input_tokens) FILTER (WHERE s.src = 'otel'), 0), SUM(s.input_tokens) FILTER (WHERE s.src = 'srec'), 0) AS input_tokens,
			COALESCE(NULLIF(SUM(s.output_tokens) FILTER (WHERE s.src = 'otel'), 0), SUM(s.output_tokens) FILTER (WHERE s.src = 'srec'), 0) AS output_tokens,
			COALESCE(SUM(s.cost_usd) FILTER (WHERE s.src = 'otel'), 0) AS cost_usd,
			COUNT(*) AS event_count,
			bool_or(s.src = 'srec') AS has_sync,
			bool_or(s.event_name = 'api_request') AS has_api_request,
			COALESCE(MAX(s.project_hash) FILTER (WHERE s.project_hash <> ''), '') AS project_hash,
			COALESCE(MAX(s.entrypoint) FILTER (WHERE s.entrypoint <> ''), '') AS entrypoint,
			bool_or(s.source_file <> '') AS has_enriched,
			bool_or(s.prompt_source IN ('typed','paste','queued','suggestion_accepted')) AS has_genuine,
			bool_or(s.forked_from_session <> '') AS has_fork,
			COALESCE(MAX(s.cctrace_version) FILTER (WHERE s.cctrace_version <> ''), '') AS cctrace_version,
			COALESCE(MAX(s.claude_version) FILTER (WHERE s.claude_version <> ''), '') AS claude_version,
			`+loginEmailInferredExpr+` AS login_email_inferred
		FROM session_union s
		LEFT JOIN top_acct ta ON ta.session_id = s.session_id
		LEFT JOIN acct_count ac ON ac.session_id = s.session_id
		GROUP BY s.session_id
	)
	SELECT
		g.session_id, g.profile_email, g.user_id, g.login_email, g.account_count, g.model, g.agent,
		g.start_time, g.end_time, g.input_tokens, g.output_tokens, g.cost_usd,
		g.event_count, g.has_sync, g.project_hash,
		COALESCE(pn.project_name, '') AS project_name,
		g.entrypoint, g.has_enriched, g.cctrace_version, g.claude_version,
		g.login_email_inferred
	FROM grouped g
	LEFT JOIN LATERAL (
		SELECT p.project_name FROM projects p
		WHERE p.project_hash = g.project_hash AND p.agent = COALESCE(NULLIF(g.agent, ''), 'claude') AND g.project_hash <> ''
		LIMIT 1
	) pn ON true
	%s
	ORDER BY g.end_time DESC, g.session_id DESC
	LIMIT %s OFFSET %s`, whereClause, srSource, whereFilter, limitClause, offsetClause)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]*SessionOverview, 0)
	for rows.Next() {
		so := &SessionOverview{}
		if err := rows.Scan(
			&so.SessionID, &so.ProfileEmail, &so.UserID, &so.LoginEmail, &so.AccountCount, &so.Model, &so.Agent,
			&so.StartTime, &so.EndTime,
			&so.InputTokens, &so.OutputTokens, &so.CostUSD,
			&so.EventCount, &so.HasSync,
			&so.ProjectHash, &so.ProjectName, &so.Entrypoint, &so.HasEnriched,
			&so.CctraceVersion, &so.ClaudeVersion, &so.LoginEmailInferred,
		); err != nil {
			return nil, err
		}
		result = append(result, so)
	}
	return result, rows.Err()
}

// usageAggregatesAt7fec0d29 is UsageAggregates as it was at 7fec0d29.
func usageAggregatesAt7fec0d29(ctx context.Context, s *PgStore, f SessionOverviewFilter) (*UsageAggregate, error) {
	var eventConds, recordConds []string
	var args []interface{}
	n := 0
	addIdentity := func(column, value string) {
		if value == "" {
			return
		}
		n++
		eventConds = append(eventConds, fmt.Sprintf("e.%s = $%d", column, n))
		recordConds = append(recordConds, fmt.Sprintf("sr.%s = $%d", column, n))
		args = append(args, value)
	}
	if f.UserID != "" {
		addIdentity("user_id", f.UserID)
	} else if f.ProfileEmail != "" {
		addIdentity("profile_email", f.ProfileEmail)
	} else if f.LoginEmail != "" {
		addIdentity("login_email", f.LoginEmail)
	}
	if f.Since != nil {
		n++
		eventConds = append(eventConds, fmt.Sprintf("e.ts >= $%d", n))
		recordConds = append(recordConds, fmt.Sprintf("sr.ts >= $%d", n))
		args = append(args, *f.Since)
	}
	if f.Until != nil {
		n++
		eventConds = append(eventConds, fmt.Sprintf("e.ts < $%d", n))
		recordConds = append(recordConds, fmt.Sprintf("sr.ts < $%d", n))
		args = append(args, *f.Until)
	}
	eventWhere := "WHERE e.session_id != ''"
	recordWhere := "sr.session_id != ''"
	if len(eventConds) > 0 {
		eventWhere += " AND " + strings.Join(eventConds, " AND ")
		recordWhere += " AND " + strings.Join(recordConds, " AND ")
	}
	sessionWhere := "WHERE (has_api_request OR has_sync)"
	if len(f.ProjectHashes) > 0 {
		n++
		sessionWhere += fmt.Sprintf(` AND EXISTS (
			SELECT 1 FROM visible_session_records project_record
			WHERE project_record.session_id = session_scope.session_id
				AND project_record.project_hash = ANY($%d)
		)`, n)
		args = append(args, f.ProjectHashes)
	}
	q := fmt.Sprintf(`WITH session_union AS (
		SELECT session_id, model, ts, 'otel'::text AS src, input_tokens, output_tokens,
			cache_read_tokens, COALESCE(agent, 'claude') AS agent, event_name
		FROM visible_events e %s
		UNION ALL
		SELECT session_id, COALESCE(model,''), ts, 'srec'::text, req_input_tokens, req_output_tokens,
			req_cache_read_tokens, COALESCE(agent, 'claude'), ''::text
		FROM (%s) sr
	), session_scope AS (
		SELECT session_id,
			MIN(ts) AS start_time, MAX(ts) AS end_time,
			bool_or(src = 'srec') AS has_sync,
			bool_or(event_name = 'api_request') AS has_api_request,
			COALESCE(SUM(input_tokens) FILTER (WHERE src = 'otel'), 0)
				+ COALESCE(SUM(output_tokens) FILTER (WHERE src = 'otel'), 0)
				+ COALESCE(SUM(cache_read_tokens) FILTER (WHERE src = 'otel' AND agent = 'codex'), 0) != 0 AS has_otel_tokens
		FROM session_union GROUP BY session_id
	), eligible_sessions AS (
		SELECT * FROM session_scope %s
	), model_totals AS (
		SELECT COALESCE(NULLIF(u.model,''), 'unknown') AS model,
			COALESCE(SUM(u.input_tokens),0)::bigint AS input_tokens,
			COALESCE(SUM(u.output_tokens),0)::bigint AS output_tokens
		FROM session_union u
		JOIN eligible_sessions e USING (session_id)
		WHERE ((e.has_otel_tokens AND u.src = 'otel') OR (NOT e.has_otel_tokens AND u.src = 'srec'))
			AND (COALESCE(u.input_tokens,0) != 0 OR COALESCE(u.output_tokens,0) != 0
				OR (u.agent = 'codex' AND COALESCE(u.cache_read_tokens,0) != 0))
		GROUP BY COALESCE(NULLIF(u.model,''), 'unknown')
	), totals AS (
		SELECT COUNT(*)::bigint AS session_count,
			COALESCE((SELECT SUM(input_tokens) FROM model_totals),0)::bigint AS input_tokens,
			COALESCE((SELECT SUM(output_tokens) FROM model_totals),0)::bigint AS output_tokens,
			COALESCE(FLOOR(SUM(GREATEST(EXTRACT(EPOCH FROM (end_time - start_time)), 0)))::bigint, 0) AS work_time_seconds
		FROM eligible_sessions
	)
	SELECT t.session_count, t.input_tokens, t.output_tokens, t.work_time_seconds,
		COALESCE(m.model,''), COALESCE(m.input_tokens,0), COALESCE(m.output_tokens,0)
	FROM totals t LEFT JOIN model_totals m ON true
	ORDER BY m.model`, eventWhere,
		attributedRowsSQLAt7fec0d29("visible_session_records", recordWhere, []string{"session_id", "model", "ts", "agent"}),
		sessionWhere)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := &UsageAggregate{ByModel: make([]*ModelUsageAggregate, 0)}
	for rows.Next() {
		model := &ModelUsageAggregate{}
		if err := rows.Scan(
			&result.SessionCount, &result.InputTokens, &result.OutputTokens, &result.WorkTimeSeconds,
			&model.Model, &model.InputTokens, &model.OutputTokens,
		); err != nil {
			return nil, err
		}
		if model.Model != "" {
			result.ByModel = append(result.ByModel, model)
		}
	}
	return result, rows.Err()
}

// listSessionSummariesAt7fec0d29 is ListSessionSummaries as it was at 7fec0d29.
func listSessionSummariesAt7fec0d29(ctx context.Context, s *PgStore, profileEmail, userID string, limit int) ([]*SessionSummary, error) {
	if limit <= 0 {
		limit = 100
	}
	conds := []string{"sr.session_id != ''"}
	args := []interface{}{}
	if profileEmail != "" {
		args = append(args, profileEmail)
		conds = append(conds, fmt.Sprintf("sr.profile_email = $%d", len(args)))
	}
	if userID != "" {
		args = append(args, userID)
		conds = append(conds, fmt.Sprintf("sr.user_id = $%d", len(args)))
	}
	args = append(args, limit)
	filter := strings.Join(conds, " AND ")
	listed := "sr.session_id IN (SELECT session_id FROM listed)"
	q := `WITH listed AS (
    SELECT sr.session_id FROM visible_session_records sr
    WHERE ` + filter + `
    GROUP BY sr.session_id ORDER BY MAX(sr.ts) DESC LIMIT $` + fmt.Sprint(len(args)) + `
), dominant AS (
    SELECT DISTINCT ON (p2.session_id) p2.session_id, p2.profile_email
    FROM (` + attributedRowsSQLAt7fec0d29("visible_session_records", listed+" AND sr.profile_email <> ''",
		[]string{"session_id", "profile_email"}) + `) p2
    GROUP BY p2.session_id, p2.profile_email
    ORDER BY p2.session_id, SUM(COALESCE(p2.req_input_tokens,0) + COALESCE(p2.req_output_tokens,0)) DESC, p2.profile_email
)
SELECT
    sr.session_id,
    COALESCE(MAX(d.profile_email), '') as profile_email,
    (SELECT COUNT(DISTINCT NULLIF(CASE WHEN COALESCE(a2.agent,'claude') = 'codex'
                                       THEN a2.account_id ELSE a2.login_email END, ''))
       FROM visible_session_records a2 WHERE a2.session_id = sr.session_id) as account_count,
    MAX(sr.project_hash) as project_hash,
    COALESCE(MAX(p.project_name), '') as project_name,
    COALESCE(MAX(CASE WHEN sr.model != '' THEN sr.model ELSE NULL END), '') as model,
    ` + sessionStartExpr("sr.ts") + ` as start_time,
    ` + sessionEndExpr("sr.ts") + ` as end_time,
    COALESCE(SUM(sr.req_input_tokens), 0) as input_tokens,
    COALESCE(SUM(sr.req_output_tokens), 0) as output_tokens,
    COUNT(*) as record_count
FROM (` + attributedRowsSQLAt7fec0d29("visible_session_records", listed+" AND "+filter,
		[]string{"session_id", "ts", "agent", "project_hash", "model"}) + `) sr
LEFT JOIN projects p ON p.agent = COALESCE(NULLIF(sr.agent, ''), 'claude') AND p.project_hash = sr.project_hash
LEFT JOIN dominant d ON d.session_id = sr.session_id
GROUP BY sr.session_id ORDER BY MAX(sr.ts) DESC`
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]*SessionSummary, 0)
	for rows.Next() {
		ss := &SessionSummary{}
		if err := rows.Scan(&ss.SessionID, &ss.ProfileEmail, &ss.AccountCount, &ss.ProjectHash,
			&ss.ProjectName, &ss.Model, &ss.StartTime, &ss.EndTime,
			&ss.InputTokens, &ss.OutputTokens, &ss.RecordCount); err != nil {
			return nil, err
		}
		result = append(result, ss)
	}
	return result, rows.Err()
}

// OrganizationInsights lists the projects its one-subquery statement listed,
// frozen below as it was at 7fec0d29, in the same order: over the whole fixture
// and over periods that cut a request at their start and at their end, and with
// every contributor floor the fixture's projects tell apart.
func TestRequestSetShape_OrganizationProjectsMatchTheOneSubqueryShape(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	shapeFixture(t, s)
	ctx := context.Background()
	listed := map[int64]int{} // by contributor floor, over the whole fixture
	for i, period := range [][2]time.Time{
		{shapeBase.Add(-time.Hour), shapeBase.Add(24 * time.Hour)},
		{shapeCut, shapeBase.Add(24 * time.Hour)},
		{shapeBase.Add(-time.Hour), shapeCut},
	} {
		for _, minUsers := range []int64{1, 2, 3} {
			what := fmt.Sprintf("%s to %s, %d contributors", period[0].Sub(shapeBase), period[1].Sub(shapeBase), minUsers)
			want, err := organizationProjectsAt7fec0d29(ctx, s, period[0], period[1], minUsers)
			if err != nil {
				t.Fatalf("%s: frozen: %v", what, err)
			}
			if minUsers == 1 && len(want) == 0 {
				t.Errorf("%s: the frozen statement lists no project", what)
			}
			// Two projects equal in tokens and in hash would be in the order
			// the plan left them in.
			for i := 1; i < len(want); i++ {
				if want[i].TotalTokens == want[i-1].TotalTokens && want[i].ProjectHash == want[i-1].ProjectHash {
					t.Fatalf("fixture: %s: two projects tie at %d tokens under %s", what, want[i].TotalTokens, want[i].ProjectHash)
				}
			}
			if i == 0 {
				listed[minUsers] = len(want)
			}
			got, err := s.OrganizationInsights(ctx, period[0], period[1], minUsers)
			if err != nil {
				t.Fatalf("%s: %v", what, err)
			}
			if !got.Available {
				t.Fatalf("%s: not available with %d active users", what, got.ActiveUsers)
			}
			g, _ := json.Marshal(got.Projects)
			w, _ := json.Marshal(want)
			if string(g) != string(w) {
				t.Errorf("%s:\n got  %s\n want %s", what, g, w)
			}
		}
	}
	if listed[1] <= listed[2] || listed[2] <= listed[3] || listed[3] == 0 {
		t.Errorf("fixture: %v projects by contributor floor -- a floor leaves nothing out, or leaves nothing", listed)
	}
}

// organizationProjectsAt7fec0d29 is OrganizationInsights' project statement as
// it was at 7fec0d29: attributedRowsSQL as one subquery, joined to projects and
// grouped by repository identity.
func organizationProjectsAt7fec0d29(ctx context.Context, s *PgStore, since, until time.Time, minUsers int64) ([]OrganizationProjectInsight, error) {
	rows, err := s.pool.Query(ctx, `
WITH scoped_projects AS (
	SELECT sr.project_hash,
		sr.session_id,
		sr.req_input_tokens AS input_tokens,
		sr.req_output_tokens AS output_tokens,
		COALESCE(NULLIF(sr.user_id, ''), NULLIF(sr.profile_email, ''), NULLIF(sr.login_email, '')) AS contributor,
		CASE WHEN BTRIM(COALESCE(p.repository_id, '')) <> ''
				AND LOWER(BTRIM(p.repository_id)) NOT LIKE 'local:%'
			THEN BTRIM(p.repository_id) ELSE '' END AS identity_repository_id,
		CASE WHEN BTRIM(COALESCE(p.repository_id, '')) <> ''
				AND LOWER(BTRIM(p.repository_id)) NOT LIKE 'local:%'
			THEN '' ELSE sr.project_hash END AS identity_group
	FROM (`+attributedRowsSQLAt7fec0d29("visible_session_records", `sr.ts >= $1 AND sr.ts < $2 AND sr.project_hash <> ''
		AND COALESCE(NULLIF(sr.user_id, ''), NULLIF(sr.profile_email, ''), NULLIF(sr.login_email, '')) IS NOT NULL`,
		[]string{"project_hash", "session_id", "agent", "user_id", "profile_email", "login_email"})+`) sr
	LEFT JOIN projects p
		ON p.agent = COALESCE(NULLIF(sr.agent, ''), 'claude')
		AND p.project_hash = sr.project_hash
), project_groups AS (
	SELECT identity_repository_id, identity_group,
		MIN(project_hash) AS project_hash,
		ARRAY_AGG(DISTINCT project_hash ORDER BY project_hash) AS project_hashes,
		COUNT(DISTINCT contributor) AS contributors,
		COUNT(DISTINCT session_id) AS session_count,
		COALESCE(SUM(COALESCE(input_tokens, 0) + COALESCE(output_tokens, 0)), 0) AS total_tokens
	FROM scoped_projects
	GROUP BY identity_repository_id, identity_group
)
SELECT project_hash, project_hashes, contributors, session_count, total_tokens
FROM project_groups
WHERE contributors >= $3
ORDER BY total_tokens DESC, project_hash ASC`, since, until, minUsers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []OrganizationProjectInsight
	for rows.Next() {
		row := OrganizationProjectInsight{}
		if err := rows.Scan(&row.ProjectHash, &row.ProjectHashes, &row.ContributorCount, &row.SessionCount, &row.TotalTokens); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// The fixture's sessions are all there for the paths to read: a check that the
// comparisons above compare something.
func TestRequestSetShape_FixtureReachesEveryKindOfRow(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	shapeFixture(t, s)
	var kinds []string
	rows, err := s.pool.Query(context.Background(), `SELECT DISTINCT
		CASE WHEN `+claudeRequestLineSQL("sr")+` THEN 'request line'
			WHEN sr.agent = 'codex' AND sr.record_type = 'usage' AND c.srec_id IS NOT NULL THEN 'codex usage, imputed'
			WHEN sr.agent = 'codex' AND sr.record_type = 'usage' THEN 'codex usage, dropped'
			WHEN sr.agent = 'codex' THEN 'codex other'
			ELSE sr.agent || ' ' || sr.record_type END
		FROM session_records sr LEFT JOIN codex_imputed_cost c ON c.srec_id = sr.id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, k)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"request line", "codex usage, imputed", "codex usage, dropped", "codex other",
		"claude user", "claude system", "claude assistant", "gjc assistant", "omo assistant"} {
		if !slices.Contains(kinds, want) {
			t.Errorf("the fixture has no %q row; it has %v", want, kinds)
		}
	}
}
