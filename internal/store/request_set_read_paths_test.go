package store

import (
	"context"
	"maps"
	"math"
	"slices"
	"testing"
	"time"
)

// Tests of the read paths that count requests (p1t-design §2.1 rows 1, 3, 4, 5,
// 7, 8, 9 and 10), by the session classes of p1t-design §7 and §8.1. Expected
// values come from the fixture's lines through rsFixture.expect, which states
// the request rule in Go, and from the rules written out beside each test --
// never from the shared SQL.

// rpRated is priced by a weekly bucket at rpScale and by nothing else: it
// matches no published price prefix. rpUnrated matches nothing, so its requests
// get no cost and keep their tokens.
const (
	rpRated   = "rp-rated-model"
	rpUnrated = "rp-unrated-model"
	rpScale   = 1.0 / (1 << 20) // dollars per weighted token; a power of two
)

// rpRate gives rpRated its bucket for the week of rsBase, a Monday.
func rpRate(t *testing.T, s *PgStore) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(), `INSERT INTO model_rate_buckets
		(model, bucket_start, scale, sample_rows) VALUES ($1, $2::date, $3, 20)`,
		rpRated, rsBase.Format(time.DateOnly), rpScale); err != nil {
		t.Fatalf("seed the rate bucket: %v", err)
	}
}

func rsVal(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// rpTotals is what a session adds up to: input and output tokens, imputed cost,
// and the number of requests that cost is over.
type rpTotals struct {
	in, out int64
	cost    float64
	priced  int64
}

// lineOf is the fixture line stored under a row id.
func (f *rsFixture) lineOf(id int64) rsLine {
	for _, l := range f.lines {
		if f.ids[l.tag] == id {
			return l
		}
	}
	return rsLine{}
}

// totals sums the requests of the lines keep selects, by session. A request
// whose head line has rpRated is priced at rpScale with claude_weighted_tokens'
// weights (weighted, postgres_model_rates_test.go); any other is not.
func (f *rsFixture) totals(keep func(rsLine) bool) map[string]rpTotals {
	heads, _ := f.expect(keep, nil)
	out := map[string]rpTotals{}
	for id, v := range heads {
		head := f.lineOf(id)
		sum := out[head.session]
		in, o, cr, cw := rsVal(v[0]), rsVal(v[1]), rsVal(v[2]), rsVal(v[3])
		sum.in += in
		sum.out += o
		if head.model == rpRated {
			sum.cost += weighted(head.model, int(in), int(o), int(cr), int(cw)) * rpScale
			sum.priced++
		}
		out[head.session] = sum
	}
	return out
}

// lineTotals is the input and output of the lines keep selects, by session: what
// a sum over lines reports.
func (f *rsFixture) lineTotals(keep func(rsLine) bool) map[string][2]int64 {
	out := map[string][2]int64{}
	for _, l := range f.lines {
		if keep(l) {
			v := l.tokens()
			sum := out[l.session]
			out[l.session] = [2]int64{sum[0] + rsVal(v[0]), sum[1] + rsVal(v[1])}
		}
	}
	return out
}

func rpCheck(t *testing.T, what string, in, out int64, cost float64, want rpTotals) {
	t.Helper()
	if in != want.in || out != want.out || math.Abs(cost-want.cost) > 1e-12 {
		t.Errorf("%s = (%d, %d, $%.12f), want (%d, %d, $%.12f)", what, in, out, cost, want.in, want.out, want.cost)
	}
}

// leadingProfile is the profile with the most tokens, the first by name on a tie.
func leadingProfile(byProfile map[string]int64) string {
	best := ""
	for profile, tokens := range byProfile {
		if best == "" || tokens > byProfile[best] || (tokens == byProfile[best] && profile < best) {
			best = profile
		}
	}
	return best
}

func summaryOf(t *testing.T, rows []*SessionSummary, sessionID string) *SessionSummary {
	t.Helper()
	for _, r := range rows {
		if r.SessionID == sessionID {
			return r
		}
	}
	t.Fatalf("session %s missing from %d summaries", sessionID, len(rows))
	return nil
}

// segmentTotals adds up a session's account segments.
func segmentTotals(t *testing.T, s *PgStore, sessionID string) (in, out int64, cost float64) {
	t.Helper()
	segments, err := s.SessionAccountSegments(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("SessionAccountSegments(%s): %v", sessionID, err)
	}
	for _, seg := range segments {
		in, out, cost = in+seg.InputTokens, out+seg.OutputTokens, cost+seg.CostUSD
	}
	return in, out, cost
}

func listOverviews(t *testing.T, s *PgStore, f SessionOverviewFilter) []*SessionOverview {
	t.Helper()
	rows, err := s.ListSessionOverviews(context.Background(), f)
	if err != nil {
		t.Fatalf("ListSessionOverviews(%+v): %v", f, err)
	}
	return rows
}

// rpRefreshOverview rebuilds the sessions' overview rows, as a writer of
// session_records does in its own transaction.
func rpRefreshOverview(t *testing.T, s *PgStore, sessions ...string) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		t.Fatal(err)
	}
	if err := refreshSessionOverviewRollups(ctx, tx, sessions); err != nil {
		t.Fatalf("refresh overview: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// rpOffline is two offline Claude sessions (class 가). rp-rated holds every
// shape the rule tells apart -- a response written as three lines, output still
// growing, twins, an id-collision group whose second line has another model, an
// input-only and an output-only line. rp-unrated's model has no rate.
func rpOffline() []rsLine {
	return []rsLine{
		{tag: "rep-1", session: "rp-rated", uuid: "rep-1", msgID: "msg_rep", model: rpRated,
			in: ptrInt(10), out: ptrInt(523), cr: ptrInt(1000), cw: ptrInt(50)},
		{tag: "rep-2", session: "rp-rated", uuid: "rep-2", at: time.Second, msgID: "msg_rep", model: rpRated,
			in: ptrInt(10), out: ptrInt(523), cr: ptrInt(1000), cw: ptrInt(50)},
		{tag: "rep-3", session: "rp-rated", uuid: "rep-3", at: 2 * time.Second, msgID: "msg_rep", model: rpRated,
			in: ptrInt(10), out: ptrInt(523), cr: ptrInt(1000), cw: ptrInt(50)},
		{tag: "grow-1", session: "rp-rated", uuid: "grow-1", at: 3 * time.Second, msgID: "msg_grow", model: rpRated,
			in: ptrInt(7), out: ptrInt(1), cr: ptrInt(900), cw: ptrInt(30)},
		{tag: "grow-2", session: "rp-rated", uuid: "grow-2", at: 4 * time.Second, msgID: "msg_grow", model: rpRated,
			in: ptrInt(7), out: ptrInt(523), cr: ptrInt(900), cw: ptrInt(30)},
		{tag: "twin-empty", session: "rp-rated", at: 5 * time.Second, msgID: "msg_twin", model: rpRated,
			in: ptrInt(100), out: ptrInt(80), cr: ptrInt(50), cw: ptrInt(5)},
		{tag: "twin-uuid", session: "rp-rated", uuid: "twin", at: 5 * time.Second, msgID: "msg_twin", model: rpRated,
			in: ptrInt(100), out: ptrInt(80), cr: ptrInt(50), cw: ptrInt(5)},
		{tag: "collide-1", session: "rp-rated", uuid: "collide-1", at: 6 * time.Second, msgID: "msg_collide",
			model: rpRated, in: ptrInt(100), out: ptrInt(50), cr: ptrInt(5000), cw: ptrInt(10)},
		{tag: "collide-2", session: "rp-rated", uuid: "collide-2", at: 7 * time.Second, msgID: "msg_collide",
			model: rpUnrated, in: ptrInt(3000), out: ptrInt(20), cr: ptrInt(10), cw: ptrInt(300)},
		{tag: "in-only", session: "rp-rated", uuid: "in-only", at: 8 * time.Second, msgID: "msg_in", model: rpRated,
			in: ptrInt(100)},
		{tag: "out-only", session: "rp-rated", uuid: "out-only", at: 9 * time.Second, msgID: "msg_out", model: rpRated,
			out: ptrInt(200)},
		{tag: "un-1", session: "rp-unrated", uuid: "un-1", msgID: "msg_un", model: rpUnrated,
			in: ptrInt(40), out: ptrInt(7), cr: ptrInt(400)},
		{tag: "un-2", session: "rp-unrated", uuid: "un-2", at: time.Second, msgID: "msg_un", model: rpUnrated,
			in: ptrInt(40), out: ptrInt(90), cr: ptrInt(400)},
		{tag: "un-3", session: "rp-unrated", uuid: "un-3", at: 2 * time.Second, msgID: "msg_un3", model: rpUnrated,
			in: ptrInt(5), out: ptrInt(6)},
	}
}

// Class 가, no OTEL: every path of this pass reports a session as the sum of its
// requests -- the list with and without a date filter, the summary, the account
// segments, the usage aggregates -- and the imputed cost is one row per request,
// so the list's cost is the sum of the per-request estimates and CostByModel
// counts requests. The unrated session has no cost and keeps its tokens.
//
// The paths are read twice. Before the refresh -- where a new offline session
// waits for the next 30-minute tick -- nothing is priced and every path reads
// both sessions' session records, so each shape of rp-rated goes through the
// session-record arm of every path. After it, rp-rated reads its imputed rows.
func TestRequestSetReadPaths_OfflineSessionsCountRequests(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	f := insertRequestSet(t, s, rpOffline()...)
	rpRate(t, s)
	want := f.totals(rsAll)
	lines := f.lineTotals(rsAll)
	for _, session := range []string{"rp-rated", "rp-unrated"} {
		if w, l := want[session], lines[session]; w.in == l[0] || w.out == l[1] {
			t.Fatalf("fixture: %s sums to %v by line and (%d, %d) by request -- the rule would not show", session, l, w.in, w.out)
		}
	}
	if want["rp-rated"].priced != 6 || want["rp-unrated"].priced != 0 {
		t.Fatalf("fixture: priced requests %+v, want 6 and 0", want)
	}

	since, until := rsBase.Add(-time.Hour), rsBase.Add(time.Hour)
	readPaths := func(when string, priced bool) {
		t.Helper()
		undated := listOverviews(t, s, SessionOverviewFilter{})
		dated := listOverviews(t, s, SessionOverviewFilter{Since: &since, Until: &until})
		summaries, err := s.ListSessionSummaries(ctx, "", "", 50)
		if err != nil {
			t.Fatalf("ListSessionSummaries: %v", err)
		}
		for session, w := range want {
			if !priced {
				w.cost = 0
			}
			for name, rows := range map[string][]*SessionOverview{"list": undated, "date-filtered list": dated} {
				o := overviewOf(t, rows, session)
				rpCheck(t, when+": "+name+" "+session, o.InputTokens, o.OutputTokens, o.CostUSD, w)
			}
			sum := summaryOf(t, summaries, session)
			rpCheck(t, when+": summary "+session, sum.InputTokens, sum.OutputTokens, 0, rpTotals{in: w.in, out: w.out})
			in, out, cost := segmentTotals(t, s, session)
			rpCheck(t, when+": account segments "+session, in, out, cost, w)
		}

		usage, err := s.UsageAggregates(ctx, SessionOverviewFilter{})
		if err != nil {
			t.Fatalf("UsageAggregates: %v", err)
		}
		if got, w := [2]int64{usage.InputTokens, usage.OutputTokens},
			[2]int64{want["rp-rated"].in + want["rp-unrated"].in, want["rp-rated"].out + want["rp-unrated"].out}; got != w {
			t.Errorf("%s: usage aggregates = %v, want %v", when, got, w)
		}
		byModel := map[string][2]int64{}
		for _, m := range usage.ByModel {
			byModel[m.Model] = [2]int64{m.InputTokens, m.OutputTokens}
		}
		// Every rp-rated request's head has rpRated, the collision group's included.
		if w := (map[string][2]int64{
			rpRated:   {want["rp-rated"].in, want["rp-rated"].out},
			rpUnrated: {want["rp-unrated"].in, want["rp-unrated"].out},
		}); len(byModel) != len(w) || byModel[rpRated] != w[rpRated] || byModel[rpUnrated] != w[rpUnrated] {
			t.Errorf("%s: usage by model = %v, want %v", when, byModel, w)
		}
	}

	readPaths("before the refresh", false)
	if models, err := s.CostByModel(ctx, since, until, "", "", ""); err != nil || len(models) != 0 {
		t.Errorf("before the refresh: CostByModel = %+v, %v; want nothing priced", models, err)
	}

	if err := s.RefreshClaudeImputedCost(ctx); err != nil {
		t.Fatalf("RefreshClaudeImputedCost: %v", err)
	}
	// Path 1: a row per priced request, at its head line, with each field's MAX.
	heads, _ := f.expect(rsAll, nil)
	wantRows := map[int64]rsTokens{}
	for id, v := range heads {
		if f.lineOf(id).model == rpRated {
			wantRows[id] = rsTok(rsVal(v[0]), rsVal(v[1]), rsVal(v[2]), rsVal(v[3]))
		}
	}
	f.compare(t, "claude_imputed_cost", f.rows(t, s, `SELECT srec_id, input_tokens, output_tokens,
		cache_read_tokens, cache_create_tokens FROM claude_imputed_cost`), wantRows)
	readPaths("after the refresh", true)

	models, err := s.CostByModel(ctx, since, until, "", "", "")
	if err != nil {
		t.Fatalf("CostByModel: %v", err)
	}
	if len(models) != 1 || models[0].Model != rpRated {
		t.Fatalf("CostByModel = %+v, want one row for %s", models, rpRated)
	}
	w := want["rp-rated"]
	if m := models[0]; m.RequestCount != w.priced {
		t.Errorf("CostByModel request count = %d, want %d requests", m.RequestCount, w.priced)
	}
	rpCheck(t, "CostByModel", models[0].InputTokens, models[0].OutputTokens, models[0].TotalCost, w)
}

// A request whose lines fall on both sides of midnight counts on both days of
// the date-filtered list (p1t-design §2.4): each day folds the lines it holds.
// The two days together exceed the two-day range by exactly the part both count.
// Unrated, so the list reads the session records rather than an imputed row.
func TestRequestSetReadPaths_DateFilteredListCountsARequestAcrossMidnightOnBothDays(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	midnight := rsBase.Truncate(24 * time.Hour)
	at := func(d time.Duration) time.Duration { return midnight.Add(d).Sub(rsBase) }
	f := insertRequestSet(t, s,
		rsLine{tag: "early", session: "rp-days", uuid: "early", at: at(12 * time.Hour), msgID: "msg_early",
			model: rpUnrated, in: ptrInt(3), out: ptrInt(4)},
		rsLine{tag: "split-1", session: "rp-days", uuid: "split-1", at: at(24*time.Hour - time.Minute),
			msgID: "msg_split", model: rpUnrated, in: ptrInt(10), out: ptrInt(1)},
		rsLine{tag: "split-2", session: "rp-days", uuid: "split-2", at: at(24*time.Hour + time.Minute),
			msgID: "msg_split", model: rpUnrated, in: ptrInt(10), out: ptrInt(523)},
		rsLine{tag: "late", session: "rp-days", uuid: "late", at: at(34 * time.Hour), msgID: "msg_late",
			model: rpUnrated, in: ptrInt(7), out: ptrInt(8)},
	)
	type period struct{ from, to time.Time }
	first, second := period{midnight, midnight.Add(24 * time.Hour)}, period{midnight.Add(24 * time.Hour), midnight.Add(48 * time.Hour)}
	whole := period{midnight, midnight.Add(48 * time.Hour)}
	got, want := map[period][2]int64{}, map[period][2]int64{}
	for _, p := range []period{first, second, whole} {
		keep := func(l rsLine) bool { ts := rsBase.Add(l.at); return !ts.Before(p.from) && ts.Before(p.to) }
		w := f.totals(keep)["rp-days"]
		want[p] = [2]int64{w.in, w.out}
		o := overviewOf(t, listOverviews(t, s, SessionOverviewFilter{Since: &p.from, Until: &p.to}), "rp-days")
		got[p] = [2]int64{o.InputTokens, o.OutputTokens}
		if got[p] != want[p] {
			t.Errorf("list for [%s, %s) = %v, want %v", p.from.Format(time.DateTime), p.to.Format(time.DateTime), got[p], want[p])
		}
	}
	var twice, wantTwice [2]int64
	for i := range twice {
		twice[i] = got[first][i] + got[second][i] - got[whole][i]
		wantTwice[i] = want[first][i] + want[second][i] - want[whole][i]
	}
	if wantTwice == [2]int64{} {
		t.Fatal("fixture: the two days count nothing twice -- no request crosses midnight")
	}
	if twice != wantTwice {
		t.Errorf("first day + second day - both = %v, want %v: the split request's part both days count", twice, wantTwice)
	}
}

// rpOTELEvent is one api_request of an OTEL session.
func rpOTELEvent(session, profile string, at time.Duration, in, out int, cost float64) *OtelEvent {
	return &OtelEvent{Ts: rsBase.Add(at), EventName: "api_request", SessionID: session, UserID: "uid-rs",
		ProfileEmail: profile, Model: "rp-otel-model", CostUSD: &cost, InputTokens: ptrInt(in), OutputTokens: ptrInt(out),
		Agent: "claude", BillingProvider: "anthropic"}
}

// otelTotals sums events by session.
func otelTotals(events []*OtelEvent) map[string]rpTotals {
	out := map[string]rpTotals{}
	for _, e := range events {
		sum := out[e.SessionID]
		sum.in += int64(*e.InputTokens)
		sum.out += int64(*e.OutputTokens)
		sum.cost += *e.CostUSD
		out[e.SessionID] = sum
	}
	return out
}

// Class 나, with OTEL: the list reports a session's OTEL and its JSONL paths its
// requests; OTEL and JSONL differ on purpose. The list falls back field by field:
// OTEL input 0 shows the requests' input, OTEL output 0 their output. With OTEL
// under profile A and JSONL under profile B, B's list falls back to B's requests
// with and without a date filter, and so do B's usage aggregates.
func TestRequestSetReadPaths_OTELSessionsListOTELAndJSONLPathsCountRequests(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	jsonl := func(tag, session, profile string, at time.Duration, msgID string, in, out int) rsLine {
		return rsLine{tag: tag, session: session, uuid: tag, profile: profile, at: at, msgID: msgID,
			model: "rp-otel-model", in: ptrInt(in), out: ptrInt(out), cr: ptrInt(1000)}
	}
	f := insertRequestSet(t, s,
		jsonl("o1-1", "rp-otel", "", 0, "msg_o1", 10, 523),
		jsonl("o1-2", "rp-otel", "", time.Second, "msg_o1", 10, 523),
		jsonl("o1-3", "rp-otel", "", 2*time.Second, "msg_o1", 10, 523),
		jsonl("o2-1", "rp-otel", "", 3*time.Second, "msg_o2", 20, 5),
		jsonl("o2-2", "rp-otel", "", 4*time.Second, "msg_o2", 20, 60),
		jsonl("n-1", "rp-otel-noout", "", 0, "msg_n", 10, 523),
		jsonl("n-2", "rp-otel-noout", "", time.Second, "msg_n", 10, 523),
		jsonl("n-3", "rp-otel-noout", "", 2*time.Second, "msg_n", 10, 523),
		jsonl("m-1", "rp-otel-noin", "", 0, "msg_m", 30, 5),
		jsonl("m-2", "rp-otel-noin", "", time.Second, "msg_m", 30, 5),
		jsonl("ab-1", "rp-otel-ab", "b@example.com", 0, "msg_ab", 10, 523),
		jsonl("ab-2", "rp-otel-ab", "b@example.com", time.Second, "msg_ab", 10, 523),
	)
	events := []*OtelEvent{
		rpOTELEvent("rp-otel", "rs@example.com", 500*time.Millisecond, 1000, 50, 0.25),
		rpOTELEvent("rp-otel", "rs@example.com", 3500*time.Millisecond, 2000, 70, 0.5),
		rpOTELEvent("rp-otel-noout", "rs@example.com", 500*time.Millisecond, 500, 0, 0.125),
		rpOTELEvent("rp-otel-noin", "rs@example.com", 500*time.Millisecond, 0, 40, 0.0625),
		rpOTELEvent("rp-otel-ab", "a@example.com", 500*time.Millisecond, 700, 30, 0.375),
	}
	if err := s.InsertEvents(ctx, events); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	requests, otel := f.totals(rsAll), otelTotals(events)
	// The list's rule, field by field: OTEL unless it is 0, then the requests.
	list := func(o, r rpTotals) rpTotals {
		w := rpTotals{in: o.in, out: o.out, cost: o.cost}
		if w.in == 0 {
			w.in = r.in
		}
		if w.out == 0 {
			w.out = r.out
		}
		return w
	}

	since, until := rsBase.Add(-time.Hour), rsBase.Add(time.Hour)
	summaries, err := s.ListSessionSummaries(ctx, "", "", 50)
	if err != nil {
		t.Fatalf("ListSessionSummaries: %v", err)
	}
	for _, session := range []string{"rp-otel", "rp-otel-noout", "rp-otel-noin", "rp-otel-ab"} {
		w := list(otel[session], requests[session])
		for name, filter := range map[string]SessionOverviewFilter{
			"list": {}, "date-filtered list": {Since: &since, Until: &until},
		} {
			o := overviewOf(t, listOverviews(t, s, filter), session)
			rpCheck(t, name+" "+session, o.InputTokens, o.OutputTokens, o.CostUSD, w)
		}
		sum := summaryOf(t, summaries, session)
		rpCheck(t, "summary "+session, sum.InputTokens, sum.OutputTokens, 0, rpTotals{in: requests[session].in, out: requests[session].out})
	}
	in, out, cost := segmentTotals(t, s, "rp-otel")
	rpCheck(t, "account segments rp-otel", in, out, cost, otel["rp-otel"])

	for name, filter := range map[string]SessionOverviewFilter{
		"profile B list": {ProfileEmail: "b@example.com"}, "profile B date-filtered list": {ProfileEmail: "b@example.com", Since: &since, Until: &until},
	} {
		o := overviewOf(t, listOverviews(t, s, filter), "rp-otel-ab")
		rpCheck(t, name, o.InputTokens, o.OutputTokens, o.CostUSD, rpTotals{in: requests["rp-otel-ab"].in, out: requests["rp-otel-ab"].out})
	}
	o := overviewOf(t, listOverviews(t, s, SessionOverviewFilter{ProfileEmail: "a@example.com"}), "rp-otel-ab")
	rpCheck(t, "profile A list", o.InputTokens, o.OutputTokens, o.CostUSD, otel["rp-otel-ab"])
	usage, err := s.UsageAggregates(ctx, SessionOverviewFilter{ProfileEmail: "b@example.com"})
	if err != nil {
		t.Fatalf("UsageAggregates: %v", err)
	}
	if got, w := [2]int64{usage.InputTokens, usage.OutputTokens}, [2]int64{requests["rp-otel-ab"].in, requests["rp-otel-ab"].out}; got != w {
		t.Errorf("profile B usage aggregates = %v, want the requests' %v", got, w)
	}
}

// Class 라, OTEL partly expired: the list adds only the OTEL that remains, not
// the requests its expired part covered (p1t-design §7; the current behaviour,
// pinned). P1b's source marking is what will say the figure is incomplete.
func TestRequestSetReadPaths_PartlyExpiredOTELListsWhatRemains(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	line := func(tag string, at time.Duration, msgID string, in, out int) rsLine {
		return rsLine{tag: tag, session: "rp-partial", uuid: tag, at: at, msgID: msgID, model: "rp-otel-model",
			in: ptrInt(in), out: ptrInt(out)}
	}
	f := insertRequestSet(t, s,
		line("r1-1", 0, "msg_r1", 100, 10), line("r1-2", time.Second, "msg_r1", 100, 10),
		line("r2-1", time.Minute, "msg_r2", 200, 20), line("r2-2", time.Minute+time.Second, "msg_r2", 200, 20),
		line("r3-1", 2*time.Minute, "msg_r3", 300, 30), line("r3-2", 2*time.Minute+time.Second, "msg_r3", 300, 30),
	)
	// Only the last request's event is left; the others' expired.
	events := []*OtelEvent{rpOTELEvent("rp-partial", "rs@example.com", 2*time.Minute, 333, 33, 0.5)}
	if err := s.InsertEvents(ctx, events); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if r := f.totals(rsAll)["rp-partial"]; r.in == 333 || r.out == 33 {
		t.Fatalf("fixture: the requests sum to (%d, %d), as the remaining OTEL does", r.in, r.out)
	}
	since, until := rsBase.Add(-time.Hour), rsBase.Add(time.Hour)
	for name, filter := range map[string]SessionOverviewFilter{"list": {}, "date-filtered list": {Since: &since, Until: &until}} {
		o := overviewOf(t, listOverviews(t, s, filter), "rp-partial")
		rpCheck(t, name, o.InputTokens, o.OutputTokens, o.CostUSD, otelTotals(events)["rp-partial"])
	}
}

// A Codex session whose rows were attributed to an account after its
// codex_imputed_cost rows were written: in that account's scope the list and the
// usage aggregates fall back to the session records, and a Codex usage row there
// reads its codex_imputed_cost row -- input net of cache reads, the mirrored
// token_count dropped -- not its own counts.
func TestRequestSetReadPaths_CodexFallbackReadsTheImputedTable(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	usage := func(tag string, at time.Duration, raw string, in, out, cr int) rsLine {
		return rsLine{tag: tag, session: "rp-codex", uuid: tag, at: at, agent: "codex", recType: "usage",
			model: "codex-model", raw: raw, in: ptrInt(in), out: ptrInt(out), cr: ptrInt(cr)}
	}
	f := insertRequestSet(t, s,
		usage("record", 0, `{"type":"token_usage_record","payload":{"usage":{"input_tokens":1}}}`, 1000, 100, 400),
		usage("mirror", time.Second, `{"type":"event_msg","payload":{"type":"token_count","info":{}}}`, 1000, 100, 400),
		usage("cache-only", 2*time.Second, "", 500, 0, 500),
	)
	refreshCodexImputed(t, s)
	codex := map[string]rsTokens{"record": rsTok(600, 100, 400, 0), "cache-only": rsTok(0, 0, 500, 0)}
	if _, err := s.pool.Exec(ctx, `UPDATE session_records SET login_email = 'cx@example.com' WHERE session_id = 'rp-codex'`); err != nil {
		t.Fatalf("attribute the session: %v", err)
	}
	rpRefreshOverview(t, s, "rp-codex")

	_, attributed := f.expect(rsAll, codex)
	var want rpTotals
	for _, v := range attributed {
		want.in += rsVal(v[0])
		want.out += rsVal(v[1])
	}
	if lines := f.lineTotals(rsAll)["rp-codex"]; want.in == lines[0] || want.out == lines[1] {
		t.Fatalf("fixture: the rows sum to %v either way", lines)
	}
	since, until := rsBase.Add(-time.Hour), rsBase.Add(time.Hour)
	for name, filter := range map[string]SessionOverviewFilter{
		"list": {LoginEmail: "cx@example.com"}, "date-filtered list": {LoginEmail: "cx@example.com", Since: &since, Until: &until},
	} {
		o := overviewOf(t, listOverviews(t, s, filter), "rp-codex")
		rpCheck(t, name, o.InputTokens, o.OutputTokens, o.CostUSD, want)
	}
	got, err := s.UsageAggregates(ctx, SessionOverviewFilter{LoginEmail: "cx@example.com"})
	if err != nil {
		t.Fatalf("UsageAggregates: %v", err)
	}
	if got.InputTokens != want.in || got.OutputTokens != want.out {
		t.Errorf("usage aggregates = (%d, %d), want (%d, %d)", got.InputTokens, got.OutputTokens, want.in, want.out)
	}
}

// The summary's representative profile is the one with the most tokens, counted
// by request over the whole session: profile A's one response written as three
// lines is 100 input, not 300, so profile B's 200 leads. Filtered to profile A,
// the totals are A's requests while the representative is still the session's.
func TestRequestSetReadPaths_SummaryProfileLeadsByRequestTokens(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	line := func(tag, profile string, at time.Duration, msgID string, in int) rsLine {
		return rsLine{tag: tag, session: "rp-profiles", uuid: tag, profile: profile, at: at, msgID: msgID,
			in: ptrInt(in), out: ptrInt(0)}
	}
	f := insertRequestSet(t, s,
		line("a-1", "a@example.com", 0, "msg_a", 100),
		line("a-2", "a@example.com", time.Second, "msg_a", 100),
		line("a-3", "a@example.com", 2*time.Second, "msg_a", 100),
		line("b-1", "b@example.com", 3*time.Second, "msg_b", 200),
	)
	byRequest, byLine := map[string]int64{}, map[string]int64{}
	heads, _ := f.expect(rsAll, nil)
	for id, v := range heads {
		byRequest[f.lineOf(id).profileEmail()] += rsVal(v[0]) + rsVal(v[1])
	}
	for _, l := range f.lines {
		v := l.tokens()
		byLine[l.profileEmail()] += rsVal(v[0]) + rsVal(v[1])
	}
	want := leadingProfile(byRequest)
	if leadingProfile(byLine) == want {
		t.Fatalf("fixture: %s leads by line as by request", want)
	}

	summaries, err := s.ListSessionSummaries(ctx, "", "", 50)
	if err != nil {
		t.Fatalf("ListSessionSummaries: %v", err)
	}
	if got := summaryOf(t, summaries, "rp-profiles").ProfileEmail; got != want {
		t.Errorf("representative profile = %s, want %s", got, want)
	}
	filtered, err := s.ListSessionSummaries(ctx, "a@example.com", "", 50)
	if err != nil {
		t.Fatalf("ListSessionSummaries for a@example.com: %v", err)
	}
	a := f.totals(func(l rsLine) bool { return l.profileEmail() == "a@example.com" })["rp-profiles"]
	got := summaryOf(t, filtered, "rp-profiles")
	if got.ProfileEmail != want || got.InputTokens != a.in || got.OutputTokens != a.out {
		t.Errorf("filtered to a@example.com: profile %s, tokens (%d, %d); want %s and A's (%d, %d)",
			got.ProfileEmail, got.InputTokens, got.OutputTokens, want, a.in, a.out)
	}
}

// summaries is ListSessionSummaries computed from the fixture: the sessions with
// a row keep selects, the latest such row first, limit of them, each with its
// kept rows' count, first and last ts and greatest model, the requests of its
// kept lines, and the profile with the most request tokens over the whole session.
func (f *rsFixture) summaries(keep func(rsLine) bool, limit int) []SessionSummary {
	bySession := map[string]*SessionSummary{}
	for _, l := range f.lines {
		if !keep(l) {
			continue
		}
		ts := rsBase.Add(l.at)
		sum := bySession[l.session]
		if sum == nil {
			sum = &SessionSummary{SessionID: l.session, StartTime: ts, EndTime: ts}
			bySession[l.session] = sum
		}
		if ts.Before(sum.StartTime) {
			sum.StartTime = ts
		}
		if ts.After(sum.EndTime) {
			sum.EndTime = ts
		}
		if l.model > sum.Model {
			sum.Model = l.model
		}
		sum.RecordCount++
	}
	requests := f.totals(keep)
	out := make([]SessionSummary, 0, len(bySession))
	for session, sum := range bySession {
		sum.InputTokens, sum.OutputTokens = requests[session].in, requests[session].out
		heads, _ := f.expect(func(l rsLine) bool { return l.session == session && l.profileEmail() != "" }, nil)
		byProfile := map[string]int64{}
		for id, v := range heads {
			byProfile[f.lineOf(id).profileEmail()] += rsVal(v[0]) + rsVal(v[1])
		}
		sum.ProfileEmail = leadingProfile(byProfile)
		out = append(out, *sum)
	}
	slices.SortFunc(out, func(a, b SessionSummary) int { return b.EndTime.Compare(a.EndTime) })
	return out[:min(limit, len(out))]
}

// The summary picks its sessions by the latest row the filter keeps, and counts
// only those: three sessions, a limit of two, and a profile filter that reorders
// them. rp-sum-a's latest line is under profile B, so it leads unfiltered and
// comes last filtered to A. A row's count, first and last ts, model and tokens
// are the kept rows'; its representative profile is the whole session's.
func TestRequestSetReadPaths_SummaryListsTheSessionsTheFilterKeepsLatestFirst(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const a, b = "a@example.com", "b@example.com"
	line := func(tag, session, profile string, at time.Duration, msgID, model string, in, out int) rsLine {
		return rsLine{tag: tag, session: session, uuid: tag, profile: profile, at: at, msgID: msgID, model: model,
			in: ptrInt(in), out: ptrInt(out)}
	}
	f := insertRequestSet(t, s,
		line("a-1", "rp-sum-a", a, time.Minute, "msg_a1", "m-a", 10, 1),
		line("a-2", "rp-sum-a", a, time.Minute+time.Second, "msg_a1", "m-a", 10, 50),
		line("a-3", "rp-sum-a", b, 50*time.Minute, "msg_a2", "m-b", 500, 5),
		line("b-1", "rp-sum-b", a, 20*time.Minute, "msg_b1", "m-b1", 30, 3),
		line("b-2", "rp-sum-b", a, 21*time.Minute, "msg_b2", "m-b2", 40, 4),
		line("c-1", "rp-sum-c", a, 5*time.Minute, "msg_c1", "m-c1", 7, 7),
		rsLine{tag: "c-user", session: "rp-sum-c", uuid: "c-user", profile: a, at: 10 * time.Minute, recType: "user"},
		line("c-2", "rp-sum-c", b, 40*time.Minute, "msg_c2", "m-c2", 8, 8),
	)
	const limit = 2
	ids := func(rows []SessionSummary) []string {
		var out []string
		for _, r := range rows {
			out = append(out, r.SessionID)
		}
		return out
	}
	unfiltered := f.summaries(rsAll, limit)
	toA := f.summaries(func(l rsLine) bool { return l.profileEmail() == a }, limit)
	if slices.Equal(ids(unfiltered), ids(toA)) || len(f.summaries(rsAll, 10)) <= limit {
		t.Fatalf("fixture: %v unfiltered and %v filtered to %s, of %d sessions", ids(unfiltered), ids(toA), a,
			len(f.summaries(rsAll, 10)))
	}

	for name, c := range map[string]struct {
		profile string
		want    []SessionSummary
	}{"unfiltered": {"", unfiltered}, "filtered to " + a: {a, toA}} {
		rows, err := s.ListSessionSummaries(ctx, c.profile, "", limit)
		if err != nil {
			t.Fatalf("%s: ListSessionSummaries: %v", name, err)
		}
		got := make([]SessionSummary, len(rows))
		for i, r := range rows {
			got[i] = *r
		}
		if !slices.Equal(ids(got), ids(c.want)) {
			t.Errorf("%s: sessions %v, want %v", name, ids(got), ids(c.want))
			continue
		}
		for i, w := range c.want {
			g := got[i]
			if g.RecordCount != w.RecordCount || !g.StartTime.Equal(w.StartTime) || !g.EndTime.Equal(w.EndTime) ||
				g.Model != w.Model || g.ProfileEmail != w.ProfileEmail || g.InputTokens != w.InputTokens ||
				g.OutputTokens != w.OutputTokens {
				t.Errorf("%s: %s = %d rows, %s to %s, model %q, profile %s, tokens (%d, %d); "+
					"want %d rows, %s to %s, model %q, profile %s, tokens (%d, %d)", name, w.SessionID,
					g.RecordCount, g.StartTime.Sub(rsBase), g.EndTime.Sub(rsBase), g.Model, g.ProfileEmail,
					g.InputTokens, g.OutputTokens,
					w.RecordCount, w.StartTime.Sub(rsBase), w.EndTime.Sub(rsBase), w.Model, w.ProfileEmail,
					w.InputTokens, w.OutputTokens)
			}
		}
	}
}

// The refresh prices offline sessions only. A session with OTEL is measured, and a
// deleted one -- tombstoned, its rows not swept yet -- must not be priced again.
// Both exclusions hold for a whole session, which is why they can sit in the
// request fragment's lineWhere.
func TestRequestSetReadPaths_RefreshLeavesOutOTELAndDeletedSessions(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	f := insertRequestSet(t, s,
		rsLine{tag: "kept", session: "rp-kept", uuid: "kept", msgID: "msg_kept", model: rpRated,
			in: ptrInt(10), out: ptrInt(5)},
		rsLine{tag: "measured", session: "rp-measured", uuid: "measured", msgID: "msg_measured", model: rpRated,
			in: ptrInt(20), out: ptrInt(6)},
		rsLine{tag: "deleted", session: "rp-deleted", uuid: "deleted", msgID: "msg_deleted", model: rpRated,
			in: ptrInt(30), out: ptrInt(7)},
	)
	rpRate(t, s)
	if err := s.InsertEvents(ctx, []*OtelEvent{rpOTELEvent("rp-measured", "rs@example.com", 0, 1, 1, 0.5)}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if _, err := s.DeleteSession(ctx, "rp-deleted", "admin@example.com", "", false, false); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if err := s.RefreshClaudeImputedCost(ctx); err != nil {
		t.Fatalf("RefreshClaudeImputedCost: %v", err)
	}
	f.compare(t, "claude_imputed_cost", f.rows(t, s, `SELECT srec_id, input_tokens, output_tokens,
		cache_read_tokens, cache_create_tokens FROM claude_imputed_cost`),
		map[int64]rsTokens{f.id("kept"): rsTok(10, 5, 0, 0)})
}

// rpUser is a contributor of the Weekly and organization fixtures below: a
// user_id and the profile it writes under.
type rpUser struct{ user, profile string }

var rpA, rpB = rpUser{"uid-a", "a@example.com"}, rpUser{"uid-b", "b@example.com"}

// rpSession is who wrote a session of those fixtures, and in which project.
type rpSession struct {
	who     rpUser
	project string
}

// rpStamp gives each line its session's user, profile and project.
func rpStamp(sessions map[string]rpSession, lines ...rsLine) []rsLine {
	for i, l := range lines {
		s := sessions[l.session]
		lines[i].user, lines[i].profile, lines[i].project = s.who.user, s.who.profile, s.project
	}
	return lines
}

// rpWeek is the length of the week both screens are read for, from rsBase: a
// line at a negative offset is before the week starts.
const rpWeek = 7 * 24 * time.Hour

// rpDonut keeps the lines of u's Weekly donut: in the week, in a project, and
// u's by user_id or by profile, the scope WeeklyInsights reads.
func rpDonut(u rpUser) func(rsLine) bool {
	return func(l rsLine) bool {
		return l.at >= 0 && l.at < rpWeek && l.project != "" && (l.userID() == u.user || l.profileEmail() == u.profile)
	}
}

// rpListed keeps the lines of the organization's projects: in the week and in a
// project. rsLine stores every line with a user_id, so every line has a
// contributor; a test that blanks a line's identity leaves it out itself.
func rpListed(l rsLine) bool { return l.at >= 0 && l.at < rpWeek && l.project != "" }

// rpProject is a project as a screen lists it: its contributors (the
// organization's list only), sessions and tokens.
type rpProject struct{ contributors, sessions, tokens int64 }

// rpScreens is what A's and B's Weekly donuts and the organization's projects
// list for the week, by project hash.
type rpScreens struct{ a, b, org map[string]rpProject }

// projects is a screen's projects computed from the fixture: the lines keep
// selects, by project, with their sessions and the input and output expect
// attributes to them -- requests folded from those lines alone, as the queries
// fold them from the lines their lineWhere keeps. With minUsers above 0 it is the
// organization's list: a project has its contributors, the user_ids behind its
// lines, and one with fewer than minUsers is left out.
func (f *rsFixture) projects(keep func(rsLine) bool, codex map[string]rsTokens, minUsers int64) map[string]rpProject {
	_, attributed := f.expect(keep, codex)
	sessions, users := map[string]map[string]bool{}, map[string]map[string]bool{}
	tokens := map[string]int64{}
	for _, l := range f.lines {
		if !keep(l) {
			continue
		}
		if sessions[l.project] == nil {
			sessions[l.project], users[l.project] = map[string]bool{}, map[string]bool{}
		}
		sessions[l.project][l.session] = true
		users[l.project][l.userID()] = true
		v := attributed[f.id(l.tag)]
		tokens[l.project] += rsVal(v[0]) + rsVal(v[1])
	}
	out := map[string]rpProject{}
	for p := range sessions {
		v := rpProject{sessions: int64(len(sessions[p])), tokens: tokens[p]}
		if minUsers > 0 {
			if v.contributors = int64(len(users[p])); v.contributors < minUsers {
				continue
			}
		}
		out[p] = v
	}
	return out
}

// screens is rpScreens computed from the fixture, the organization holding a
// project to minUsers contributors.
func (f *rsFixture) screens(codex map[string]rsTokens, minUsers int64) rpScreens {
	return rpScreens{f.projects(rpDonut(rpA), codex, 0), f.projects(rpDonut(rpB), codex, 0),
		f.projects(rpListed, codex, minUsers)}
}

// readScreens reads rpScreens from the store.
func readScreens(t *testing.T, s *PgStore, minUsers int64) rpScreens {
	t.Helper()
	ctx := context.Background()
	got := rpScreens{a: map[string]rpProject{}, b: map[string]rpProject{}, org: map[string]rpProject{}}
	for _, donut := range []struct {
		u    rpUser
		into map[string]rpProject
	}{{rpA, got.a}, {rpB, got.b}} {
		insights, err := s.WeeklyInsights(ctx, rsBase, rsBase.Add(rpWeek), donut.u.profile, donut.u.user, "UTC")
		if err != nil {
			t.Fatalf("WeeklyInsights(%s): %v", donut.u.user, err)
		}
		for _, p := range insights.Projects {
			donut.into[p.ProjectHash] = rpProject{sessions: p.SessionCount, tokens: p.TotalTokens}
		}
	}
	insights, err := s.OrganizationInsights(ctx, rsBase, rsBase.Add(rpWeek), minUsers)
	if err != nil {
		t.Fatalf("OrganizationInsights: %v", err)
	}
	if !insights.Available {
		t.Fatalf("OrganizationInsights: not available with %d active users", insights.ActiveUsers)
	}
	for _, p := range insights.Projects {
		got.org[p.ProjectHash] = rpProject{contributors: p.ContributorCount, sessions: p.SessionCount, tokens: p.TotalTokens}
	}
	return got
}

// check compares the screens with want, then with each other: for the same week
// a project of the organization's list adds up to A's and B's donuts. That holds
// where each request takes its MAX from the lines of one donut, as in these
// fixtures; a request with lines in both donuts would count in each.
func (got rpScreens) check(t *testing.T, want rpScreens) {
	t.Helper()
	for _, c := range []struct {
		name      string
		got, want map[string]rpProject
	}{{"A's donut", got.a, want.a}, {"B's donut", got.b, want.b}, {"the organization's projects", got.org, want.org}} {
		if !maps.Equal(c.got, c.want) {
			t.Errorf("%s = %+v, want %+v", c.name, c.got, c.want)
		}
	}
	for project, o := range got.org {
		a, b := got.a[project], got.b[project]
		if a.sessions+b.sessions != o.sessions || a.tokens+b.tokens != o.tokens {
			t.Errorf("%s: the organization lists %d sessions and %d tokens, A's and B's donuts %d and %d",
				project, o.sessions, o.tokens, a.sessions+b.sessions, a.tokens+b.tokens)
		}
	}
}

// Class 가 on the Weekly donut and the organization's projects (p1t-design §2.1
// rows 4 and 5): a project's tokens are its requests' -- a response written as
// three lines once, twins once, an id collision at each field's MAX, a line with
// one side keeping that side. A request whose first line is before the week
// counts in the week with the lines inside it (§2.4), not at its head before the
// start, which would lose it. Sessions count as before. A's donut shows both of
// A's projects; the organization, holding a project to two contributors, lists
// only the one A and B share.
func TestRequestSetReadPaths_WeeklyAndOrganizationProjectsCountRequests(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	f := insertRequestSet(t, s, rpStamp(map[string]rpSession{
		"rp-a1": {rpA, "rp-shared"}, "rp-b1": {rpB, "rp-shared"}, "rp-a2": {rpA, "rp-solo"},
	},
		// Its first line is before the week starts.
		rsLine{tag: "straddle-1", session: "rp-a1", uuid: "straddle-1", at: -time.Second, msgID: "msg_straddle",
			in: ptrInt(40), out: ptrInt(1)},
		rsLine{tag: "straddle-2", session: "rp-a1", uuid: "straddle-2", at: time.Second, msgID: "msg_straddle",
			in: ptrInt(40), out: ptrInt(300)},
		rsLine{tag: "straddle-3", session: "rp-a1", uuid: "straddle-3", at: 2 * time.Second, msgID: "msg_straddle",
			in: ptrInt(40), out: ptrInt(523)},
		rsLine{tag: "rep-1", session: "rp-a1", uuid: "rep-1", at: 10 * time.Second, msgID: "msg_rep",
			in: ptrInt(10), out: ptrInt(1), cr: ptrInt(1000)},
		rsLine{tag: "rep-2", session: "rp-a1", uuid: "rep-2", at: 11 * time.Second, msgID: "msg_rep",
			in: ptrInt(10), out: ptrInt(1), cr: ptrInt(1000)},
		rsLine{tag: "rep-3", session: "rp-a1", uuid: "rep-3", at: 12 * time.Second, msgID: "msg_rep",
			in: ptrInt(10), out: ptrInt(523), cr: ptrInt(1000)},
		rsLine{tag: "twin-empty", session: "rp-a1", at: 20 * time.Second, msgID: "msg_twin",
			in: ptrInt(100), out: ptrInt(80)},
		rsLine{tag: "twin-uuid", session: "rp-a1", uuid: "twin", at: 20 * time.Second, msgID: "msg_twin",
			in: ptrInt(100), out: ptrInt(80)},
		rsLine{tag: "collide-1", session: "rp-a1", uuid: "collide-1", at: 30 * time.Second, msgID: "msg_collide",
			model: "rp-model-a", in: ptrInt(100), out: ptrInt(50)},
		rsLine{tag: "collide-2", session: "rp-a1", uuid: "collide-2", at: 31 * time.Second, msgID: "msg_collide",
			model: "rp-model-b", in: ptrInt(3000), out: ptrInt(20)},
		rsLine{tag: "in-only", session: "rp-a1", uuid: "in-only", at: 40 * time.Second, msgID: "msg_in", in: ptrInt(100)},
		rsLine{tag: "out-only", session: "rp-a1", uuid: "out-only", at: 41 * time.Second, msgID: "msg_out", out: ptrInt(200)},
		rsLine{tag: "b-1", session: "rp-b1", uuid: "b-1", at: 50 * time.Second, msgID: "msg_b", in: ptrInt(7), out: ptrInt(1)},
		rsLine{tag: "b-2", session: "rp-b1", uuid: "b-2", at: 51 * time.Second, msgID: "msg_b", in: ptrInt(7), out: ptrInt(90)},
		rsLine{tag: "solo-1", session: "rp-a2", uuid: "solo-1", at: time.Minute, msgID: "msg_solo",
			in: ptrInt(5), out: ptrInt(6)},
		rsLine{tag: "solo-2", session: "rp-a2", uuid: "solo-2", at: time.Minute + time.Second, msgID: "msg_solo",
			in: ptrInt(5), out: ptrInt(60)},
	)...)
	want := f.screens(nil, 2)
	// Summed by line, or folded over the straddling request's every line before
	// the week is cut, A's shared project would read otherwise.
	byLine := f.lineTotals(rpDonut(rpA))["rp-a1"]
	whole := f.sumAttributed(nil, func(l rsLine) bool { return rpDonut(rpA)(l) && l.project == "rp-shared" })
	if shared := want.a["rp-shared"].tokens; shared == byLine[0]+byLine[1] || shared == whole[0]+whole[1] {
		t.Fatalf("fixture: A's shared project is %d by request, %v by line and %v folded before the week is cut",
			shared, byLine, whole)
	}
	if _, listed := want.org["rp-solo"]; listed || len(want.a) != 2 || len(want.org) != 1 {
		t.Fatalf("fixture: A's donut %+v, the organization's projects %+v -- the contributor rule leaves nothing out",
			want.a, want.org)
	}
	readScreens(t, s, 2).check(t, want)
}

// Classes 마 and 바 on the Weekly donut and the organization's projects: gjc and
// omo rows count as they are -- two gjc rows under one message id are two rows,
// and an omo row left NULL adds 0 -- and a Codex usage row counts its
// codex_imputed_cost row, input net of cache reads. In the week, the mirrored
// token_count counts 0 though the token_usage_record it mirrors is before the
// week starts, so does the repeated snapshot, and the cache-only row adds 0. The
// organization lists every project here, one contributor being enough, and the
// Codex project, A's and B's, is their donuts added.
func TestRequestSetReadPaths_WeeklyAndOrganizationProjectsCountGjcOmoAndCodex(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	snapshot := `{"payload":{"info":{"total_token_usage":{"input_tokens":10,"cached_input_tokens":1,"output_tokens":2}}}}`
	usage := func(tag, session string, at time.Duration, raw string, in, out, cr int) rsLine {
		return rsLine{tag: tag, session: session, uuid: tag, at: at, agent: "codex", recType: "usage",
			model: "codex-model", raw: raw, in: ptrInt(in), out: ptrInt(out), cr: ptrInt(cr)}
	}
	f := insertRequestSet(t, s, rpStamp(map[string]rpSession{
		"rp-gjc": {rpA, "rp-gjc"}, "rp-omo": {rpB, "rp-omo"}, "rp-cx-a": {rpA, "rp-codex"}, "rp-cx-b": {rpB, "rp-codex"},
	},
		rsLine{tag: "gjc-1", session: "rp-gjc", uuid: "gjc-1", at: time.Second, agent: "gjc", msgID: "msg_gjc",
			in: ptrInt(11), out: ptrInt(12), cr: ptrInt(13), cw: ptrInt(14)},
		rsLine{tag: "gjc-2", session: "rp-gjc", uuid: "gjc-2", at: 2 * time.Second, agent: "gjc", msgID: "msg_gjc",
			in: ptrInt(21), out: ptrInt(22), cr: ptrInt(23), cw: ptrInt(24)},
		// Left NULL where the provider double-counts usage.
		rsLine{tag: "omo-null", session: "rp-omo", uuid: "omo-null", at: time.Second, agent: "omo"},
		rsLine{tag: "omo-counted", session: "rp-omo", uuid: "omo-counted", at: 2 * time.Second, agent: "omo",
			in: ptrInt(31), out: ptrInt(32)},
		// Before the week starts.
		usage("cx-record", "rp-cx-a", -10*time.Second,
			`{"type":"token_usage_record","payload":{"usage":{"input_tokens":1}}}`, 1000, 100, 400),
		usage("cx-mirror", "rp-cx-a", time.Second,
			`{"type":"event_msg","payload":{"type":"token_count","info":{}}}`, 1000, 100, 400),
		usage("cx-snapshot", "rp-cx-a", 2*time.Second, snapshot, 50, 5, 0),
		usage("cx-repeat", "rp-cx-a", 3*time.Second, snapshot, 50, 5, 0),
		usage("cx-cache", "rp-cx-a", 4*time.Second, "", 500, 0, 500),
		usage("cx-b", "rp-cx-b", 5*time.Second, "", 70, 7, 20),
	)...)
	refreshCodexImputed(t, s)
	codex := map[string]rsTokens{
		"cx-record": rsTok(600, 100, 400, 0), "cx-snapshot": rsTok(50, 5, 0, 0),
		"cx-cache": rsTok(0, 0, 500, 0), "cx-b": rsTok(50, 7, 20, 0),
	}
	imputed := map[int64]rsTokens{}
	for tag, v := range codex {
		imputed[f.id(tag)] = v
	}
	f.compare(t, "codex_imputed_cost", f.rows(t, s, `SELECT srec_id, input_tokens, output_tokens,
		cache_read_tokens, cache_create_tokens FROM codex_imputed_cost`), imputed)

	written := rpScreens{
		a: map[string]rpProject{
			"rp-gjc":   {sessions: 1, tokens: 11 + 12 + 21 + 22},
			"rp-codex": {sessions: 1, tokens: 50 + 5},
		},
		b: map[string]rpProject{
			"rp-omo":   {sessions: 1, tokens: 31 + 32},
			"rp-codex": {sessions: 1, tokens: 50 + 7},
		},
		org: map[string]rpProject{
			"rp-gjc":   {contributors: 1, sessions: 1, tokens: 11 + 12 + 21 + 22},
			"rp-omo":   {contributors: 1, sessions: 1, tokens: 31 + 32},
			"rp-codex": {contributors: 2, sessions: 2, tokens: 50 + 5 + 50 + 7},
		},
	}
	if rule := f.screens(codex, 1); !maps.Equal(rule.a, written.a) || !maps.Equal(rule.b, written.b) ||
		!maps.Equal(rule.org, written.org) {
		t.Fatalf("fixture: the rule gives %+v, the numbers written beside it %+v", rule, written)
	}
	readScreens(t, s, 1).check(t, written)
}

// Where the filters sit, pinned on one project: the Weekly scope, the project
// filter and the organization's contributor filter are in lineWhere, so a
// request is folded from the lines each keeps. Each of msg_p, msg_s and msg_c
// has a first line a filter drops -- no project, another user's, no identity --
// and still counts, from its other line: in A's donut, and in the organization's
// list, where msg_s keeps both lines, the other user being a contributor too.
// Applied after the fold, a filter would drop the request with its head. A
// session with no identity at all is not on the organization's list.
func TestRequestSetReadPaths_WeeklyAndOrganizationFoldTheLinesTheirFiltersKeep(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	line := func(tag, session string, u rpUser, project string, at time.Duration, msgID string, in, out int) rsLine {
		return rsLine{tag: tag, session: session, uuid: tag, at: at, user: u.user, profile: u.profile,
			project: project, msgID: msgID, in: ptrInt(in), out: ptrInt(out)}
	}
	x, nobody := rpUser{"uid-x", "x@example.com"}, rpUser{}
	f := insertRequestSet(t, s,
		line("p-1", "rp-pin", rpA, "", 0, "msg_p", 10, 1),
		line("p-2", "rp-pin", rpA, "rp-pin", time.Second, "msg_p", 10, 523),
		line("s-1", "rp-pin", x, "rp-pin", 2*time.Second, "msg_s", 20, 2),
		line("s-2", "rp-pin", rpA, "rp-pin", 3*time.Second, "msg_s", 20, 600),
		line("c-1", "rp-pin", nobody, "rp-pin", 4*time.Second, "msg_c", 30, 3),
		line("c-2", "rp-pin", rpA, "rp-pin", 5*time.Second, "msg_c", 30, 700),
		line("anon", "rp-pin-anon", nobody, "rp-pin", 6*time.Second, "msg_anon", 40, 4),
	)
	// rsLine stores an empty identity as uid-rs and rs@example.com, so the lines
	// meant to have none are blanked here, and every screen below leaves them
	// out by tag.
	blank := map[string]bool{"c-1": true, "anon": true}
	if _, err := s.pool.Exec(context.Background(), `UPDATE session_records SET user_id = '', profile_email = ''
		WHERE id = ANY($1::bigint[])`, []int64{f.id("c-1"), f.id("anon")}); err != nil {
		t.Fatalf("blank the identity: %v", err)
	}
	known := func(keep func(rsLine) bool) func(rsLine) bool {
		return func(l rsLine) bool { return keep(l) && !blank[l.tag] }
	}
	want := rpScreens{f.projects(known(rpDonut(rpA)), nil, 0), f.projects(known(rpDonut(rpB)), nil, 0),
		f.projects(known(rpListed), nil, 1)}

	// Each placement reads otherwise. A filter applied after the fold sums, over
	// the lines it keeps, the requests folded from the lines it drops as well;
	// without the contributor filter the session with no identity counts too.
	folded := func(fold, keep func(rsLine) bool) int64 {
		_, attributed := f.expect(fold, nil)
		var sum int64
		for id, v := range attributed {
			if keep(f.lineOf(id)) {
				sum += rsVal(v[0]) + rsVal(v[1])
			}
		}
		return sum
	}
	ofA := known(func(l rsLine) bool { return l.userID() == rpA.user || l.profileEmail() == rpA.profile })
	weekOfA := func(l rsLine) bool { return l.at >= 0 && l.at < rpWeek && ofA(l) }
	inProject := func(l rsLine) bool { return l.project != "" }
	donut, listed := want.a["rp-pin"].tokens, want.org["rp-pin"].tokens
	for name, c := range map[string]struct{ after, lineWhere int64 }{
		"A's donut with the project filter after the fold":            {folded(weekOfA, inProject), donut},
		"A's donut with the scope after the fold":                     {folded(rpListed, ofA), donut},
		"the organization with the contributor filter after the fold": {folded(rpListed, known(rsAll)), listed},
		"the organization without the contributor filter":             {folded(rpListed, rsAll), listed},
	} {
		if c.after == c.lineWhere {
			t.Fatalf("fixture: %s gives %d, as lineWhere does", name, c.after)
		}
	}
	readScreens(t, s, 1).check(t, want)
}
