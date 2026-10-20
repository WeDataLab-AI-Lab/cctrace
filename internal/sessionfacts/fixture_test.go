package sessionfacts

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The fixtures are slim rows shaped like what claudeSlimRowsSQL returns for
// real JSONL lines; their text is made up. Every expectation in these tests is
// written out by hand.

var day = time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)

// at is sec seconds into the fixture day.
func at(sec int) time.Time { return day.Add(time.Duration(sec) * time.Second) }

const marker = "[redacted by cctrace client]"

type opt func(*ClaudeRow)

// newRow builds a main-thread row as a collecting client stores it: one user,
// one profile, one login, and the cctraceCollect mark.
func newRow(recordType, uuid, parent string, sec int, opts ...opt) ClaudeRow {
	r := ClaudeRow{
		TS: at(sec), RecordType: recordType, UUID: uuid, ParentUUID: parent,
		UserID: "user-1", ProfileEmail: "dev@example.com", LoginEmail: "dev@example.com",
		BillingProvider: "anthropic", ProjectHash: "proj", Branch: "main",
		CctraceCollect: json.RawMessage(`1`),
	}
	for _, o := range opts {
		o(&r)
	}
	return r
}

// user is a user line whose content is a string.
func user(uuid, parent string, sec int, text string, opts ...opt) ClaudeRow {
	return newRow("user", uuid, parent, sec, append([]opt{func(r *ClaudeRow) {
		r.ContentKind, r.TextHead = "string", text
	}}, opts...)...)
}

// typed is a user line a person typed after P0: it carries a promptId, an
// origin and a promptSource.
func typed(uuid, parent string, sec int, promptID, text string, opts ...opt) ClaudeRow {
	return user(uuid, parent, sec, text, append([]opt{prompt(promptID), origin("human"), source("typed")}, opts...)...)
}

// result is a user line holding the result of one tool_use.
func result(uuid, parent string, sec int, toolUseID string, opts ...opt) ClaudeRow {
	return newRow("user", uuid, parent, sec, append([]opt{blocks(`[{"t":"tool_result","tu":"` + toolUseID + `"}]`)}, opts...)...)
}

// reply is an assistant line that only says something.
func reply(uuid, parent string, sec int, opts ...opt) ClaudeRow {
	return newRow("assistant", uuid, parent, sec, append([]opt{model("claude-opus-5"), blocks(`[{"t":"text"}]`)}, opts...)...)
}

// call is an assistant line that calls one tool.
func call(uuid, parent string, sec int, toolUseID, tool string, opts ...opt) ClaudeRow {
	return newRow("assistant", uuid, parent, sec, append([]opt{model("claude-opus-5"),
		blocks(`[{"t":"text"},{"t":"tool_use","id":"` + toolUseID + `","n":"` + tool + `"}]`)}, opts...)...)
}

func system(uuid, parent string, sec int, subtype string, opts ...opt) ClaudeRow {
	return newRow("system", uuid, parent, sec, append([]opt{func(r *ClaudeRow) { r.Subtype = subtype }}, opts...)...)
}

func attachment(uuid, parent string, sec int, body string, opts ...opt) ClaudeRow {
	return newRow("attachment", uuid, parent, sec, append([]opt{func(r *ClaudeRow) { r.Attachment = json.RawMessage(body) }}, opts...)...)
}

// command is the user line of a slash command: sync read its name into the
// column from the <command-name> tag.
func command(uuid, parent string, sec int, name string, opts ...opt) ClaudeRow {
	return user(uuid, parent, sec, "<command-name>/"+name+"</command-name>\n<command-message>"+name+"</command-message>",
		append([]opt{func(r *ClaudeRow) { r.CommandName, r.CommandInvoke = name, "explicit" }}, opts...)...)
}

func prompt(id string) opt    { return func(r *ClaudeRow) { r.PromptID = id } }
func origin(kind string) opt  { return func(r *ClaudeRow) { r.OriginKind = kind } }
func source(s string) opt     { return func(r *ClaudeRow) { r.PromptSource = s } }
func model(m string) opt      { return func(r *ClaudeRow) { r.Model = m } }
func meta() opt               { return func(r *ClaudeRow) { r.IsMeta = true } }
func sidechain(id string) opt { return func(r *ClaudeRow) { r.IsSidechain, r.AgentID = true, id } }

func blocks(summary string) opt {
	return func(r *ClaudeRow) { r.ContentKind, r.TextHead, r.Blocks = "array", "", json.RawMessage(summary) }
}

// textBlock is a user line whose content is an array with one text block.
func textBlock(text string) opt {
	return func(r *ClaudeRow) {
		r.ContentKind, r.TextHead, r.Blocks = "array", text, json.RawMessage(`[{"t":"text"}]`)
	}
}

// preP0 is a row stored before the collection rules: no mark, and none of the
// fields they keep.
func preP0() opt {
	return func(r *ClaudeRow) { r.CctraceCollect, r.PromptID, r.OriginKind = nil, "", "" }
}

// redacted is what a prompt policy leaves of a line's content: the marker where
// message.content was, for a tool result as for typed text.
func redacted() opt {
	return func(r *ClaudeRow) {
		r.ContentKind, r.Blocks, r.TextHead = "string", nil, ""
		if r.RecordType == "user" {
			r.TextHead = marker
		}
	}
}

// fixture is an Input under construction.
type fixture struct {
	in  Input
	row map[string]*ClaudeRow
}

// session numbers the rows in the order given, as session_records ids, and
// makes them a Claude session.
func session(rows ...ClaudeRow) *fixture {
	f := &fixture{in: Input{Agent: "claude", Rows: rows}, row: make(map[string]*ClaudeRow)}
	for i := range f.in.Rows {
		r := &f.in.Rows[i]
		r.ID = int64(i + 1)
		if _, dup := f.row[r.UUID]; !dup {
			f.row[r.UUID] = r
		}
	}
	return f
}

// request adds the request whose head line is the assistant row uuid, with the
// columns claudeRequestHeadsSQL would take from that row and 100 input, 10
// output, 1000 cache read and 5 cache create tokens times weight. It has no
// imputed row.
func (f *fixture) request(uuid string, weight int64, opts ...func(*Head)) *fixture {
	r := f.row[uuid]
	if r == nil {
		panic("fixture: no row " + uuid)
	}
	h := Head{ID: r.ID, UUID: r.UUID, TS: r.TS, IsSidechain: r.IsSidechain, AgentID: r.AgentID, SourceFile: r.SourceFile, Model: r.Model,
		InputTokens: 100 * weight, OutputTokens: 10 * weight, CacheReadTokens: 1000 * weight, CacheCreateTokens: 5 * weight}
	for _, o := range opts {
		o(&h)
	}
	f.in.Heads = append(f.in.Heads, h)
	return f
}

// priced gives a request an imputed row.
func priced(usd float64) func(*Head) {
	return func(h *Head) { h.Imputed, h.HasRate, h.CostUSD = true, true, usd }
}

// rated is a request with a rate and no row yet.
func rated() func(*Head) { return func(h *Head) { h.HasRate = true } }

// event adds a visible api_request event with the session's own identity. Its
// presence makes the session an OTEL session.
func (f *fixture) event(sec int, promptID string, usd float64) *fixture {
	f.in.HasOTEL = true
	f.in.OTEL = append(f.in.OTEL, OTELRow{TS: at(sec), PromptID: promptID, CostUSD: usd,
		UserID: "user-1", ProfileEmail: "dev@example.com", LoginEmail: "dev@example.com", BillingProvider: "anthropic"})
	return f
}

func (f *fixture) copies(uuids ...string) *fixture {
	f.in.CopyGate = true
	for _, u := range uuids {
		f.in.CopiedIDs = append(f.in.CopiedIDs, f.row[u].ID)
	}
	return f
}

func (f *fixture) with(change func(*Input)) *fixture {
	change(&f.in)
	return f
}

// turnsBy renders one line per turn with the named columns, for an expectation
// a reader can check against the fixture by eye.
func turnsBy(out Output, columns ...string) []string {
	var lines []string
	for _, t := range out.Turns {
		var cells []string
		for _, c := range columns {
			cells = append(cells, turnCell(t, c))
		}
		lines = append(lines, strings.Join(cells, " "))
	}
	return lines
}

func turnCell(t Turn, column string) string {
	switch column {
	case "key":
		return t.TurnKey
	case "opener":
		return t.OpenerUUID
	case "ordinal":
		return fmt.Sprint(t.Ordinal)
	case "branch":
		return t.Branch
	case "kind":
		return t.InputKind
	case "request":
		if t.RequestNo != nil {
			return fmt.Sprintf("request#%d", *t.RequestNo)
		}
		if t.IsRequest {
			return "request#?"
		}
		return "-"
	case "status":
		if t.StatusReason != "" {
			return t.Status + "(" + t.StatusReason + ")"
		}
		return t.Status
	case "preview":
		if t.Preview == nil {
			return "<" + t.PreviewState + ">"
		}
		return fmt.Sprintf("%q/%s", *t.Preview, t.PreviewState)
	case "span":
		return fmt.Sprintf("%d..%d", int(t.StartedAt.Sub(day)/time.Second), int(t.EndedAt.Sub(day)/time.Second))
	case "prompt":
		switch {
		case t.PromptIDShared:
			return t.PromptID + "(shared)"
		case t.PromptID == "":
			return "-"
		}
		return t.PromptID
	case "requests":
		return fmt.Sprintf("%dreq/%din", t.APIRequests, t.InputTokens)
	case "cost":
		s := t.CostSource
		if t.CostReason != "" {
			s += "(" + t.CostReason + ")"
		}
		if t.CostUSD != nil {
			s += fmt.Sprintf("=%g", *t.CostUSD)
		}
		return s
	case "inputs":
		var in []string
		for _, i := range t.Detail.Inputs {
			in = append(in, fmt.Sprintf("%d:%s:%s", i.N, i.UUID, i.Kind))
		}
		return fmt.Sprintf("[%s]+%d", strings.Join(in, ","), t.Detail.ExtraInputs)
	case "images":
		return fmt.Sprintf("%dimg", t.ImageCount)
	case "copied":
		return fmt.Sprint(t.OpenerCopied)
	case "login":
		return t.LoginEmail
	}
	panic("unknown column " + column)
}

func equal(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n got  %#v\n want %#v", what, got, want)
	}
}

// count is a nullable count as text, "null" for nil.
func count(n *int) string {
	if n == nil {
		return "null"
	}
	return fmt.Sprint(*n)
}

// counts is the session's "turns/requests/inputs/abandoned".
func counts(s Session) string {
	return count(s.Turns) + "/" + count(s.Requests) + "/" + count(s.Inputs) + "/" + count(s.AbandonedTurns)
}

func text(s *string) string {
	if s == nil {
		return "<null>"
	}
	return *s
}

func usd(f *float64) string {
	if f == nil {
		return "null"
	}
	return fmt.Sprint(*f)
}

// tcase is one fixture and what Compute must make of it.
type tcase struct {
	name   string
	in     *fixture
	checks []check
}

type check func(t *testing.T, out Output)

func checks(c ...check) []check { return c }

func runCases(t *testing.T, cases []tcase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := Compute(tc.in.in)
			for _, c := range tc.checks {
				c(t, out)
			}
		})
	}
}

// turns expects the session's turn rows, one line each, in the named columns.
func turns(columns string, want ...string) check {
	return func(t *testing.T, out Output) {
		t.Helper()
		equal(t, "turns ("+columns+")", turnsBy(out, strings.Fields(columns)...), want)
	}
}

// sessionIs expects one value read from the session row.
func sessionIs[T any](what string, read func(Session) T, want T) check {
	return func(t *testing.T, out Output) {
		t.Helper()
		equal(t, what, read(out.Session), want)
	}
}

func turnCounts(want string) check { return sessionIs("turns/requests/inputs/abandoned", counts, want) }

// segments expects the activity segments as "start..end/records/human_inputs",
// in seconds of the fixture day.
func segments(want ...string) check {
	return func(t *testing.T, out Output) {
		t.Helper()
		var got []string
		for _, g := range out.Segments {
			got = append(got, fmt.Sprintf("%d..%d/%d/%d", int(g.SegStart.Sub(day)/time.Second), int(g.SegEnd.Sub(day)/time.Second), g.Records, g.HumanInputs))
		}
		equal(t, "segments", got, want)
	}
}

// second is an instant as seconds of the fixture day, "null" for nil.
func second(ts *time.Time) string {
	if ts == nil {
		return "null"
	}
	return fmt.Sprint(int(ts.Sub(day) / time.Second))
}

// activity is the session's "first..last activity, first record".
func activity(s Session) string {
	return second(s.FirstActivityAt) + ".." + second(s.LastActivityAt) + " first record " + second(s.FirstRecordAt)
}

// sessionCost is the session's "source=cost attached+unattached note".
func sessionCost(s Session) string {
	c := s.Detail.Cost
	return strings.TrimSpace(fmt.Sprintf("%s=%s %g+%g %s", s.CostSource, usd(s.CostUSD), c.TurnAttachedUSD, c.UnattachedTurnUSD, c.Note))
}

// tokens is the session's "requests: input/output/cache read/cache create".
func tokens(s Session) string {
	return fmt.Sprintf("%d: %d/%d/%d/%d", s.APIRequests, s.InputTokens, s.OutputTokens, s.CacheReadTokens, s.CacheCreateTokens)
}
