package store

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"cctrace/internal/activitylabel"
)

// Tests of the facts written at sync time that count requests (p1t-design §2.1
// rows 2 and 6, §6.2, §6.3) -- task segment tokens and plugin invocation facts --
// and of the plugin raw query beside them. Expected values come from the
// fixture's lines through rsFixture.expect and the windows stated below in Go,
// checked against numbers written out beside each fixture -- never from the
// shared SQL.

// rsTurn is a typed human turn: a user row whose message content is a string.
// It opens a task segment after a 30-minute gap or a project change, and ends a
// command's response window.
func rsTurn(tag, session, agent string, at time.Duration) rsLine {
	return rsLine{tag: tag, session: session, uuid: tag, at: at, agent: agent, recType: "user",
		raw: `{"message":{"role":"user","content":"please add a test for this"}}`}
}

// rsHuman is whether a line is a human turn: every user row of Codex, and a user
// row of any other agent whose message content is a string.
func rsHuman(l rsLine) bool {
	if l.recordType() != "user" {
		return false
	}
	var raw struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	_ = json.Unmarshal([]byte(l.raw), &raw)
	return l.agentName() == "codex" || strings.HasPrefix(string(raw.Message.Content), `"`)
}

func (f *rsFixture) line(tag string) rsLine {
	for _, l := range f.lines {
		if l.tag == tag {
			return l
		}
	}
	return rsLine{}
}

func (f *rsFixture) sessions() []string {
	var out []string
	for _, l := range f.lines {
		if !slices.Contains(out, l.session) {
			out = append(out, l.session)
		}
	}
	return out
}

// sumAttributed adds up the input and output expect attributes to the lines in
// selects.
func (f *rsFixture) sumAttributed(codex map[string]rsTokens, in func(rsLine) bool) [2]int64 {
	_, attributed := f.expect(rsAll, codex)
	var sum [2]int64
	for _, l := range f.lines {
		if in(l) {
			v := attributed[f.id(l.tag)]
			sum[0] += rsVal(v[0])
			sum[1] += rsVal(v[1])
		}
	}
	return sum
}

// segmentTokens is what each task segment should hold, by the tag of the typed
// turn that opens it: what expect attributes to the rows of its session from its
// start up to the start of the session's next segment. opens lists those turns,
// each session's in start order.
func (f *rsFixture) segmentTokens(codex map[string]rsTokens, opens ...string) map[string][2]int64 {
	out := map[string][2]int64{}
	for i, tag := range opens {
		open := f.line(tag)
		end := time.Duration(math.MaxInt64)
		for _, next := range opens[i+1:] {
			if f.line(next).session == open.session {
				end = f.line(next).at
				break
			}
		}
		out[tag] = f.sumAttributed(codex, func(l rsLine) bool {
			return l.session == open.session && l.at >= open.at && l.at < end
		})
	}
	return out
}

// segmentFactTokens reads the tokens of the sessions' stored task segments, by
// the tag of the typed turn that opens each -- boundary_record_id is its row id.
func (f *rsFixture) segmentFactTokens(t *testing.T, s *PgStore, sessions ...string) map[string][2]int64 {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), `SELECT boundary_record_id, input_tokens, output_tokens
		FROM task_segment_facts WHERE session_id = ANY($1::text[])`, sessions)
	if err != nil {
		t.Fatalf("read task segment facts: %v", err)
	}
	defer rows.Close()
	out := map[string][2]int64{}
	for rows.Next() {
		var id int64
		var tokens [2]int64
		if err := rows.Scan(&id, &tokens[0], &tokens[1]); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[f.tags[id]] = tokens
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read task segment facts: %v", err)
	}
	return out
}

// staleSegmentTokens overwrites the token columns of the sessions' segments with
// a value no count produces.
func staleSegmentTokens(t *testing.T, s *PgStore, sessions ...string) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(), `UPDATE task_segment_facts SET input_tokens = -1, output_tokens = -1
		WHERE session_id = ANY($1::text[])`, sessions); err != nil {
		t.Fatalf("stale the task segment tokens: %v", err)
	}
}

// rsSegmentFixture is task segments of every class of p1t-design §8.1: Claude
// offline, gjc, omo and Codex. codex is the imputation each Codex usage row
// should have, opens the typed turns that open the segments, and written the
// tokens each segment adds up to by the rule, worked out by hand.
func rsSegmentFixture() (lines []rsLine, codex map[string]rsTokens, opens []string, written map[string][2]int64) {
	record := `{"type":"token_usage_record","payload":{"usage":{"input_tokens":1}}}`
	tokenCount := `{"type":"event_msg","payload":{"type":"token_count","info":{}}}`
	snapshot := `{"payload":{"info":{"total_token_usage":{"input_tokens":10,"cached_input_tokens":1,"output_tokens":2}}}}`
	usage := func(tag string, at time.Duration, raw string, in, out, cr int) rsLine {
		return rsLine{tag: tag, session: "sf-codex", uuid: tag, at: at, agent: "codex", recType: "usage",
			model: "codex-model", raw: raw, in: ptrInt(in), out: ptrInt(out), cr: ptrInt(cr)}
	}
	lines = []rsLine{
		// Before the session's first segment: in none.
		{tag: "pre", session: "sf-claude", uuid: "pre", at: -time.Minute, msgID: "msg_pre", in: ptrInt(50), out: ptrInt(50)},
		rsTurn("cl-open", "sf-claude", "", 0),
		{tag: "rep-1", session: "sf-claude", uuid: "rep-1", at: time.Second, msgID: "msg_rep",
			in: ptrInt(10), out: ptrInt(1), cr: ptrInt(100)},
		{tag: "rep-2", session: "sf-claude", uuid: "rep-2", at: 2 * time.Second, msgID: "msg_rep",
			in: ptrInt(10), out: ptrInt(523), cr: ptrInt(100)},
		{tag: "twin-empty", session: "sf-claude", at: 3 * time.Second, msgID: "msg_twin", in: ptrInt(100), out: ptrInt(80)},
		{tag: "twin-uuid", session: "sf-claude", uuid: "TW", at: 3 * time.Second, msgID: "msg_twin",
			in: ptrInt(100), out: ptrInt(80)},
		{tag: "collide-1", session: "sf-claude", uuid: "collide-1", at: 4 * time.Second, msgID: "msg_collide",
			model: "model-a", in: ptrInt(100), out: ptrInt(50)},
		{tag: "collide-2", session: "sf-claude", uuid: "collide-2", at: 5 * time.Second, msgID: "msg_collide",
			model: "model-b", in: ptrInt(3000), out: ptrInt(20)},
		// Claude's other rows and <synthetic> lines pass through as they are.
		{tag: "tool", session: "sf-claude", uuid: "tool", at: 6 * time.Second, recType: "user",
			raw: `{"message":{"content":[{"type":"tool_result","content":"ok"}]}}`, in: ptrInt(5)},
		{tag: "synthetic", session: "sf-claude", uuid: "synthetic", at: 7 * time.Second, msgID: "msg_rep",
			model: "<synthetic>", in: ptrInt(7), out: ptrInt(7)},
		rsTurn("cl-next", "sf-claude", "", 40*time.Minute),
		{tag: "in-only", session: "sf-claude", uuid: "in-only", at: 41 * time.Minute, msgID: "msg_in", in: ptrInt(100)},
		{tag: "out-only", session: "sf-claude", uuid: "out-only", at: 42 * time.Minute, msgID: "msg_out", out: ptrInt(200)},

		rsTurn("gjc-open", "sf-gjc", "gjc", 0),
		{tag: "gjc-1", session: "sf-gjc", uuid: "gjc-1", at: time.Second, agent: "gjc", msgID: "msg_gjc",
			in: ptrInt(11), out: ptrInt(12), cr: ptrInt(13), cw: ptrInt(14)},
		{tag: "gjc-2", session: "sf-gjc", uuid: "gjc-2", at: 2 * time.Second, agent: "gjc", msgID: "msg_gjc",
			in: ptrInt(21), out: ptrInt(22), cr: ptrInt(23), cw: ptrInt(24)},

		rsTurn("omo-open", "sf-omo", "omo", 0),
		// Left NULL where the provider double-counts usage.
		{tag: "omo-null", session: "sf-omo", uuid: "omo-null", at: time.Second, agent: "omo"},
		{tag: "omo-counted", session: "sf-omo", uuid: "omo-counted", at: 2 * time.Second, agent: "omo",
			in: ptrInt(31), out: ptrInt(32)},

		rsTurn("cx-open", "sf-codex", "codex", 0),
		usage("cx-record", time.Second, record, 1000, 100, 400),
		usage("cx-cache", 2*time.Second, "", 500, 0, 500),
		usage("cx-snapshot", 3*time.Second, snapshot, 50, 5, 0),
		{tag: "cx-assistant", session: "sf-codex", uuid: "cx-assistant", at: 4 * time.Second, agent: "codex",
			in: ptrInt(5), out: ptrInt(5)},
		rsTurn("cx-next", "sf-codex", "codex", 40*time.Minute),
		// What these repeat and mirror is in the segment before.
		usage("cx-repeat", 41*time.Minute, snapshot, 50, 5, 0),
		usage("cx-mirror", 42*time.Minute, tokenCount, 1000, 100, 400),
		usage("cx-live", 43*time.Minute, "", 70, 7, 20),
	}
	codex = map[string]rsTokens{
		"cx-record":   rsTok(600, 100, 400, 0),
		"cx-cache":    rsTok(0, 0, 500, 0),
		"cx-snapshot": rsTok(50, 5, 0, 0),
		"cx-live":     rsTok(50, 7, 20, 0),
	}
	opens = []string{"cl-open", "cl-next", "gjc-open", "omo-open", "cx-open", "cx-next"}
	written = map[string][2]int64{
		// The response once, the twins once, the collision's MAX, the tool row's
		// input and the <synthetic> line.
		"cl-open":  {10 + 100 + 3000 + 5 + 7, 523 + 80 + 50 + 7},
		"cl-next":  {100, 200},
		"gjc-open": {32, 34},
		"omo-open": {31, 32},
		"cx-open":  {600 + 50, 100 + 5},
		"cx-next":  {50, 7},
	}
	return lines, codex, opens, written
}

// p1t-design §6.2 and §8.1: a task segment's tokens are its requests', each at its
// head line -- a response written as several lines once, twins once, an id
// collision at each field's MAX, a line with one side keeping that side, Claude's
// other rows and <synthetic> lines as they are -- gjc and omo at their own counts,
// with an omo row left NULL as 0, and Codex usage behind the whole-session
// defences: the repeated snapshot and the mirrored token_count in the second
// segment count 0, though what they repeat and mirror is in the first. A line
// before the first segment counts in none. Step 8's UPDATE recounts the stored
// segments to the same numbers.
func TestTaskSegmentFacts_TokensCountRequests(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	lines, codex, opens, written := rsSegmentFixture()
	f := insertRequestSet(t, s, lines...)
	want := f.segmentTokens(codex, opens...)
	if !maps.Equal(want, written) {
		t.Fatalf("fixture: the rule gives %v, the numbers written beside it %v", want, written)
	}
	sessions := f.sessions()
	if got := f.segmentFactTokens(t, s, sessions...); !maps.Equal(got, want) {
		t.Errorf("task segment tokens = %v, want %v", got, want)
	}

	staleSegmentTokens(t, s, sessions...)
	if _, err := s.pool.Exec(context.Background(), taskSegmentTokensUpdateSQL, sessions); err != nil {
		t.Fatalf("taskSegmentTokensUpdateSQL: %v", err)
	}
	if got := f.segmentFactTokens(t, s, sessions...); !maps.Equal(got, want) {
		t.Errorf("task segment tokens after the step 8 update = %v, want %v", got, want)
	}
}

// p1t-design §8.2: typed turns in two projects at one instant T open two
// segments there, [T, T) and [T, next); which turn opens which is the
// segmentation's tie order, so the test reads it back. A request whose head is at
// T goes to [T, next), and [T, T) counts 0, as the range join gave it; the
// segment before T does not take it either. Step 8's UPDATE agrees.
func TestTaskSegmentFacts_RowAtTwoSegmentStartsGoesToTheOneThatRunsOn(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const session = "sf-same"
	at := 2 * time.Hour
	turn := func(tag string, when time.Duration, project string) rsLine {
		l := rsTurn(tag, session, "", when)
		l.project = project
		return l
	}
	f := insertRequestSet(t, s,
		turn("before", 0, "p1"),
		rsLine{tag: "r0", session: session, uuid: "r0", at: time.Minute, msgID: "msg_r0", in: ptrInt(3), out: ptrInt(4)},
		turn("same-a", at, "p1"),
		turn("same-b", at, "p2"),
		rsLine{tag: "h-1", session: session, uuid: "h-1", at: at, msgID: "msg_h", in: ptrInt(50), out: ptrInt(60)},
		rsLine{tag: "h-2", session: session, uuid: "h-2", at: at + time.Second, msgID: "msg_h", in: ptrInt(50), out: ptrInt(70)},
		rsLine{tag: "x", session: session, uuid: "x", at: at + 5*time.Minute, msgID: "msg_x", in: ptrInt(9), out: ptrInt(9)},
		turn("after", at+3*time.Hour, "p2"),
		rsLine{tag: "y", session: session, uuid: "y", at: at + 3*time.Hour + time.Second, msgID: "msg_y",
			in: ptrInt(1), out: ptrInt(2)},
	)

	rows, err := s.pool.Query(ctx, `SELECT boundary_record_id, start_ts, end_ts FROM task_segment_facts
		WHERE session_id = $1`, session)
	if err != nil {
		t.Fatalf("read the segments: %v", err)
	}
	type bound struct {
		from, to time.Duration
		open     bool
	}
	bounds := map[string]bound{}
	for rows.Next() {
		var id int64
		var start time.Time
		var end *time.Time
		if err := rows.Scan(&id, &start, &end); err != nil {
			t.Fatalf("scan: %v", err)
		}
		b := bound{from: start.Sub(rsBase), open: end == nil}
		if end != nil {
			b.to = end.Sub(rsBase)
		}
		bounds[f.tags[id]] = b
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("read the segments: %v", err)
	}
	empty, runsOn := "same-a", "same-b"
	if bounds["same-b"].to == at {
		empty, runsOn = runsOn, empty
	}
	if want := (map[string]bound{"before": {0, at, false}, empty: {at, at, false}, runsOn: {at, at + 3*time.Hour, false},
		"after": {at + 3*time.Hour, 0, true}}); !maps.Equal(bounds, want) {
		t.Fatalf("fixture: segments %v, want %v -- the test needs [T, T) beside [T, next)", bounds, want)
	}

	want := map[string][2]int64{}
	for tag, b := range bounds {
		want[tag] = f.sumAttributed(nil, func(l rsLine) bool { return l.at >= b.from && (b.open || l.at < b.to) })
	}
	if want[runsOn] != [2]int64{50 + 9, 70 + 9} || want[empty] != [2]int64{} {
		t.Fatalf("fixture: the rule gives %v", want)
	}
	if got := f.segmentFactTokens(t, s, session); !maps.Equal(got, want) {
		t.Errorf("task segment tokens = %v, want %v", got, want)
	}

	staleSegmentTokens(t, s, session)
	if _, err := s.pool.Exec(ctx, taskSegmentTokensUpdateSQL, []string{session}); err != nil {
		t.Fatalf("taskSegmentTokensUpdateSQL: %v", err)
	}
	if got := f.segmentFactTokens(t, s, session); !maps.Equal(got, want) {
		t.Errorf("task segment tokens after the step 8 update = %v, want %v", got, want)
	}
}

// commandTokens is what each command should hold, by its tag: what expect
// attributes to the rows of its session in its response window -- after the
// command, or from it for an assistant row, up to the session's next human turn
// -- and, for a command on a line of a Claude request, to every line of that
// request wherever it lies.
func (f *rsFixture) commandTokens(codex map[string]rsTokens, commands ...string) map[string][2]int64 {
	key := func(l rsLine) string {
		switch {
		case !l.isRequestLine():
			return ""
		case l.msgID != "":
			return l.msgID
		}
		return fmt.Sprintf("row:%d", f.id(l.tag))
	}
	out := map[string][2]int64{}
	for _, tag := range commands {
		c := f.line(tag)
		end := time.Duration(math.MaxInt64)
		for _, l := range f.lines {
			if l.session == c.session && rsHuman(l) && l.at > c.at {
				end = min(end, l.at)
			}
		}
		out[tag] = f.sumAttributed(codex, func(l rsLine) bool {
			inWindow := (l.at > c.at || (l.at == c.at && c.recordType() == "assistant")) && l.at < end
			return l.session == c.session && (inWindow || (key(c) != "" && key(l) == key(c)))
		})
	}
	return out
}

// commandFactTokens reads plugin_invocation_facts, by the tag of each command's
// row.
func (f *rsFixture) commandFactTokens(t *testing.T, s *PgStore) map[string][2]int64 {
	t.Helper()
	rows, err := s.pool.Query(context.Background(),
		`SELECT source_record_id, input_tokens, output_tokens FROM plugin_invocation_facts`)
	if err != nil {
		t.Fatalf("read plugin invocation facts: %v", err)
	}
	defer rows.Close()
	out := map[string][2]int64{}
	for rows.Next() {
		var id int64
		var tokens [2]int64
		if err := rows.Scan(&id, &tokens[0], &tokens[1]); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[f.tags[id]] = tokens
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read plugin invocation facts: %v", err)
	}
	return out
}

// pluginUsageTokens reads PluginUsage for [since, an hour after rsBase), with the
// fact marker there (the facts) or not (the raw query), by command name and agent.
func pluginUsageTokens(t *testing.T, s *PgStore, since time.Time, facts bool) map[string][2]int64 {
	t.Helper()
	setMarker(t, s, pluginInvocationFactsBackfill, facts)
	out := map[string][2]int64{}
	for key, totals := range pluginUsageTotals(t, s, since, rsBase.Add(time.Hour)) {
		out[key] = [2]int64{totals.input, totals.output}
	}
	return out
}

// rsPluginFixture is plugin commands of every class of p1t-design §8.1, each the
// only command of its session: codex is the imputation each Codex usage row
// should have, commands the command rows, and written the tokens each command
// adds up to by the rule, worked out by hand.
func rsPluginFixture() (lines []rsLine, codex map[string]rsTokens, commands []string, written map[string][2]int64) {
	command := func(tag, session, agent, name string) rsLine {
		l := rsTurn(tag, session, agent, 0)
		l.command = name
		return l
	}
	usage := func(tag string, at time.Duration, raw string, in, out, cr int) rsLine {
		return rsLine{tag: tag, session: "pf-codex", uuid: tag, at: at, agent: "codex", recType: "usage",
			model: "codex-model", raw: raw, in: ptrInt(in), out: ptrInt(out), cr: ptrInt(cr)}
	}
	snapshot := `{"payload":{"info":{"total_token_usage":{"input_tokens":10,"cached_input_tokens":1,"output_tokens":2}}}}`
	lines = []rsLine{
		command("review", "pf-claude", "", "pf-review"),
		{tag: "rep-1", session: "pf-claude", uuid: "rep-1", at: time.Second, msgID: "msg_rep",
			in: ptrInt(10), out: ptrInt(1), cr: ptrInt(100)},
		{tag: "rep-2", session: "pf-claude", uuid: "rep-2", at: 2 * time.Second, msgID: "msg_rep",
			in: ptrInt(10), out: ptrInt(523), cr: ptrInt(100)},
		{tag: "twin-empty", session: "pf-claude", at: 3 * time.Second, msgID: "msg_twin", in: ptrInt(100), out: ptrInt(80)},
		{tag: "twin-uuid", session: "pf-claude", uuid: "TW", at: 3 * time.Second, msgID: "msg_twin",
			in: ptrInt(100), out: ptrInt(80)},
		{tag: "collide-1", session: "pf-claude", uuid: "collide-1", at: 4 * time.Second, msgID: "msg_collide",
			model: "model-a", in: ptrInt(100), out: ptrInt(50)},
		{tag: "collide-2", session: "pf-claude", uuid: "collide-2", at: 5 * time.Second, msgID: "msg_collide",
			model: "model-b", in: ptrInt(3000), out: ptrInt(20)},
		{tag: "in-only", session: "pf-claude", uuid: "in-only", at: 6 * time.Second, msgID: "msg_in", in: ptrInt(100)},
		{tag: "out-only", session: "pf-claude", uuid: "out-only", at: 7 * time.Second, msgID: "msg_out", out: ptrInt(200)},
		{tag: "tool", session: "pf-claude", uuid: "tool", at: 8 * time.Second, recType: "user",
			raw: `{"message":{"content":[{"type":"tool_result","content":"ok"}]}}`, in: ptrInt(5)},
		rsTurn("human", "pf-claude", "", 10*time.Second),
		{tag: "after", session: "pf-claude", uuid: "after", at: 11 * time.Second, msgID: "msg_after",
			in: ptrInt(999), out: ptrInt(999)},

		command("gjc-cmd", "pf-gjc", "gjc", "pf-gjc"),
		{tag: "gjc-1", session: "pf-gjc", uuid: "gjc-1", at: time.Second, agent: "gjc", msgID: "msg_gjc",
			in: ptrInt(11), out: ptrInt(12)},
		{tag: "gjc-2", session: "pf-gjc", uuid: "gjc-2", at: 2 * time.Second, agent: "gjc", msgID: "msg_gjc",
			in: ptrInt(21), out: ptrInt(22)},
		rsTurn("gjc-human", "pf-gjc", "gjc", 5*time.Second),
		{tag: "gjc-after", session: "pf-gjc", uuid: "gjc-after", at: 6 * time.Second, agent: "gjc",
			in: ptrInt(99), out: ptrInt(99)},

		command("omo-cmd", "pf-omo", "omo", "pf-omo"),
		{tag: "omo-null", session: "pf-omo", uuid: "omo-null", at: time.Second, agent: "omo"},
		{tag: "omo-counted", session: "pf-omo", uuid: "omo-counted", at: 2 * time.Second, agent: "omo",
			in: ptrInt(31), out: ptrInt(32)},
		rsTurn("omo-human", "pf-omo", "omo", 5*time.Second),

		// Before the command, and before the period start of a read from -5s.
		usage("cx-record", -10*time.Second, `{"type":"token_usage_record","payload":{"usage":{"input_tokens":1}}}`,
			1000, 100, 400),
		command("cx-cmd", "pf-codex", "codex", "pf-codex"),
		usage("cx-mirror", time.Second, `{"type":"event_msg","payload":{"type":"token_count","info":{}}}`, 1000, 100, 400),
		usage("cx-snapshot", 2*time.Second, snapshot, 50, 5, 0),
		usage("cx-repeat", 3*time.Second, snapshot, 50, 5, 0),
		usage("cx-cache", 4*time.Second, "", 500, 0, 500),
		{tag: "cx-assistant", session: "pf-codex", uuid: "cx-assistant", at: 5 * time.Second, agent: "codex",
			in: ptrInt(5), out: ptrInt(5)},
		rsTurn("cx-human", "pf-codex", "codex", 10*time.Second),
		usage("cx-after", 11*time.Second, "", 70, 7, 20),
	}
	codex = map[string]rsTokens{
		"cx-record":   rsTok(600, 100, 400, 0),
		"cx-snapshot": rsTok(50, 5, 0, 0),
		"cx-cache":    rsTok(0, 0, 500, 0),
		"cx-after":    rsTok(50, 7, 20, 0),
	}
	commands = []string{"review", "gjc-cmd", "omo-cmd", "cx-cmd"}
	written = map[string][2]int64{
		// The response once, the twins once, the collision's MAX, the one-sided
		// lines' sides and the tool row's input.
		"review":  {10 + 100 + 3000 + 100 + 5, 523 + 80 + 50 + 200},
		"gjc-cmd": {32, 34},
		"omo-cmd": {31, 32},
		"cx-cmd":  {50, 5},
	}
	return lines, codex, commands, written
}

// p1t-design §6.3 and §8.1: a command's tokens are the requests of its response
// window, each at its head line, by class -- in the facts written at sync time,
// and in the raw query PluginUsage reads before the facts are built, which gives
// the facts' numbers for the same commands. Both read the Codex rows behind their
// own defences, so codex_imputed_cost stays empty here, as it is for a session
// its refresh has not reached. The token_usage_record that makes the mirror 0 is
// before the command, and in the second read before the period start too.
func TestPluginFacts_TokensCountRequests(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	keepMarker(t, s, pluginInvocationFactsBackfill)
	lines, codex, commands, written := rsPluginFixture()
	f := insertRequestSet(t, s, lines...)
	want := f.commandTokens(codex, commands...)
	if !maps.Equal(want, written) {
		t.Fatalf("fixture: the rule gives %v, the numbers written beside it %v", want, written)
	}
	if got := f.commandFactTokens(t, s); !maps.Equal(got, want) {
		t.Errorf("plugin invocation facts = %v, want %v", got, want)
	}

	var imputed int
	if err := s.pool.QueryRow(context.Background(), `SELECT count(*) FROM codex_imputed_cost`).Scan(&imputed); err != nil {
		t.Fatalf("count codex_imputed_cost: %v", err)
	}
	if imputed != 0 {
		t.Fatalf("codex_imputed_cost holds %d row(s); the test needs it empty", imputed)
	}
	byName := map[string][2]int64{}
	for _, tag := range commands {
		byName[f.line(tag).command+"/"+f.line(tag).agentName()] = want[tag]
	}
	for _, from := range []time.Duration{-time.Hour, -5 * time.Second} {
		for path, facts := range map[string]bool{"facts": true, "raw query": false} {
			if got := pluginUsageTokens(t, s, rsBase.Add(from), facts); !maps.Equal(got, byName) {
				t.Errorf("PluginUsage from %s, %s = %v, want %v", from, path, got, byName)
			}
		}
	}
}

// p1t-design §8.2: an implicit command on the second line of a response gets that
// response, whose head is before the command, and no other: not the response
// interleaved between its lines. So does the raw query read from a period start
// between the head and the command, where the head is before the lookup start.
func TestPluginFacts_ImplicitCommandGetsItsResponseAcrossTheLookupStart(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	keepMarker(t, s, pluginInvocationFactsBackfill)
	const session = "pf-implicit"
	line := func(tag string, at time.Duration, msgID string, in, out int) rsLine {
		return rsLine{tag: tag, session: session, uuid: tag, at: at, msgID: msgID, in: ptrInt(in), out: ptrInt(out)}
	}
	command := line("r-2", 13*time.Second, "msg_r", 30, 2)
	command.command = "pf-implicit"
	f := insertRequestSet(t, s,
		rsTurn("u0", session, "", 0),
		line("r-1", 10*time.Second, "msg_r", 30, 1),
		line("q-1", 12*time.Second, "msg_q", 7, 7),
		command,
		line("q-2", 14*time.Second, "msg_q", 7, 9),
		line("r-3", 15*time.Second, "msg_r", 30, 600),
		rsTurn("u1", session, "", 20*time.Second),
	)
	want := f.commandTokens(nil, "r-2")
	if want["r-2"] != [2]int64{30, 600} {
		t.Fatalf("fixture: the rule gives %v, want the response's MAX (30, 600)", want)
	}
	if got := f.commandFactTokens(t, s); !maps.Equal(got, want) {
		t.Errorf("plugin invocation facts = %v, want %v", got, want)
	}
	for path, facts := range map[string]bool{"facts": true, "raw query": false} {
		got := pluginUsageTokens(t, s, rsBase.Add(11*time.Second), facts)
		if w := (map[string][2]int64{"pf-implicit/claude": want["r-2"]}); !maps.Equal(got, w) {
			t.Errorf("PluginUsage from between the head and the command, %s = %v, want %v", path, got, w)
		}
	}
}

// An implicit command on a response's head line is in its own window and in its
// own request at once: the head counts once, not once for each way it qualifies.
func TestPluginFacts_CommandOnTheHeadLineCountsItsResponseOnce(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	keepMarker(t, s, pluginInvocationFactsBackfill)
	const session = "pf-head"
	head := rsLine{tag: "h-1", session: session, uuid: "h-1", at: 10 * time.Second, msgID: "msg_h", command: "pf-head",
		in: ptrInt(40), out: ptrInt(1)}
	f := insertRequestSet(t, s,
		rsTurn("u0", session, "", 0),
		head,
		rsLine{tag: "h-2", session: session, uuid: "h-2", at: 11 * time.Second, msgID: "msg_h", in: ptrInt(40), out: ptrInt(900)},
		rsTurn("u1", session, "", 20*time.Second),
	)
	want := f.commandTokens(nil, "h-1")
	if want["h-1"] != [2]int64{40, 900} {
		t.Fatalf("fixture: the rule gives %v, want the response's MAX (40, 900) once", want)
	}
	if got := f.commandFactTokens(t, s); !maps.Equal(got, want) {
		t.Errorf("plugin invocation facts = %v, want %v", got, want)
	}
	for path, facts := range map[string]bool{"facts": true, "raw query": false} {
		if got, w := pluginUsageTokens(t, s, rsBase, facts), (map[string][2]int64{"pf-head/claude": want["h-1"]}); !maps.Equal(got, w) {
			t.Errorf("PluginUsage, %s = %v, want %v", path, got, w)
		}
	}
}

// p1t-design §8.2 on stored segments, through step 8's UPDATE, where the test sets
// each segment's start and end itself: a row at T goes to whichever segment
// starting at T ends later -- of [T, T) and [T, next) the second, of [T, T) and an
// open [T, NULL) the open one -- [T, T) counts 0, and the segment before T does not
// take the row either.
func TestTaskSegmentTokens_UpdateGivesARowAtTwoStartsToTheLaterEnd(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	at := time.Hour
	line := func(tag, session string, when time.Duration, msgID string, in, out int) rsLine {
		return rsLine{tag: tag, session: session, uuid: tag, at: when, msgID: msgID, in: ptrInt(in), out: ptrInt(out)}
	}
	f := insertRequestSet(t, s,
		line("closed-0", "sf-closed", 10*time.Minute, "msg_c0", 3, 4),
		line("closed-at-1", "sf-closed", at, "msg_c", 50, 60),
		line("closed-at-2", "sf-closed", at+time.Second, "msg_c", 50, 70),
		line("closed-next", "sf-closed", 2*at+time.Minute, "msg_c2", 9, 9),
		line("open-0", "sf-open", 10*time.Minute, "msg_o0", 3, 4),
		line("open-at-1", "sf-open", at, "msg_o", 50, 60),
		line("open-at-2", "sf-open", at+time.Second, "msg_o", 50, 70),
	)
	type segment struct {
		session  string
		from, to time.Duration
		open     bool
	}
	// Negative keys: no session_records id is, so no stored segment shares one.
	segments := map[int64]segment{
		-1: {"sf-closed", 0, at, false}, -2: {"sf-closed", at, at, false},
		-3: {"sf-closed", at, 2 * at, false}, -4: {"sf-closed", 2 * at, 0, true},
		-11: {"sf-open", 0, at, false}, -12: {"sf-open", at, at, false}, -13: {"sf-open", at, 0, true},
	}
	sessions := []string{"sf-closed", "sf-open"}
	clear := func() {
		if _, err := s.pool.Exec(context.Background(), `DELETE FROM task_segment_facts WHERE session_id = ANY($1::text[])`,
			sessions); err != nil {
			t.Fatalf("clear the sessions' segments: %v", err)
		}
	}
	clear()
	// task_segment_facts is not among the tables truncateTables empties.
	t.Cleanup(clear)
	want := map[int64][2]int64{}
	for id, seg := range segments {
		var end *time.Time
		if !seg.open {
			e := rsBase.Add(seg.to)
			end = &e
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO task_segment_facts
				(boundary_record_id, session_id, start_ts, end_ts, input_tokens, output_tokens)
			VALUES ($1, $2, $3, $4, -1, -1)`, id, seg.session, rsBase.Add(seg.from), end); err != nil {
			t.Fatalf("store segment %d: %v", id, err)
		}
		want[id] = f.sumAttributed(nil, func(l rsLine) bool {
			return l.session == seg.session && l.at >= seg.from && (seg.open || l.at < seg.to)
		})
	}
	if written := (map[int64][2]int64{-1: {3, 4}, -2: {}, -3: {50, 70}, -4: {9, 9},
		-11: {3, 4}, -12: {}, -13: {50, 70}}); !maps.Equal(want, written) {
		t.Fatalf("fixture: the rule gives %v, the numbers written beside it %v", want, written)
	}

	if _, err := s.pool.Exec(ctx, taskSegmentTokensUpdateSQL, sessions); err != nil {
		t.Fatalf("taskSegmentTokensUpdateSQL: %v", err)
	}
	rows, err := s.pool.Query(ctx, `SELECT boundary_record_id, input_tokens, output_tokens FROM task_segment_facts
		WHERE session_id = ANY($1::text[])`, sessions)
	if err != nil {
		t.Fatalf("read the segments: %v", err)
	}
	got := map[int64][2]int64{}
	for rows.Next() {
		var id int64
		var tokens [2]int64
		if err := rows.Scan(&id, &tokens[0], &tokens[1]); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[id] = tokens
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("read the segments: %v", err)
	}
	if !maps.Equal(got, want) {
		t.Errorf("segment tokens after the step 8 update = %v, want %v", got, want)
	}
}

// A line without a session belongs to none, so the fact refreshes drop the empty
// id whichever caller hands it in -- a sync batch with metadata lines, or a
// retention pass, which reads its sessions from the fact tables -- and never read
// every such line of the table as one session. A fact stored under the empty id,
// as a retention pass would find one, is left as it is.
func TestFactRefreshesLeaveTheEmptySessionAlone(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	exec := func(stmt string) {
		t.Helper()
		if _, err := s.pool.Exec(context.Background(), stmt); err != nil {
			t.Fatalf("%s: %v", firstLine(stmt), err)
		}
	}
	// task_segment_facts is not among the tables truncateTables empties.
	clear := func() { exec(`DELETE FROM task_segment_facts WHERE session_id = ''`) }
	clear()
	t.Cleanup(clear)
	exec(`INSERT INTO plugin_invocation_facts (source_record_id, session_id, command_ts, command_name)
		VALUES (-100, '', now(), 'sentinel')`)
	exec(`INSERT INTO task_segment_facts (boundary_record_id, session_id, start_ts) VALUES (-100, '', now())`)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := refreshPluginInvocationFacts(ctx, tx, []string{"", "other"}); err != nil {
		t.Fatalf("refreshPluginInvocationFacts: %v", err)
	}
	if err := refreshTaskSegmentFacts(ctx, tx, []string{"", "other"}); err != nil {
		t.Fatalf("refreshTaskSegmentFacts: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	for _, table := range []string{"plugin_invocation_facts", "task_segment_facts"} {
		var n int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE session_id = ''`).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 1 {
			t.Errorf("%s holds %d fact(s) under the empty id after a refresh named it, want the one stored: "+
				"the refresh took the empty id for a session", table, n)
		}
	}
}

// bulkSessionRecords writes sessions sessions of 40 rows each straight into
// session_records -- a typed turn, then Claude request lines in pairs, or Codex
// usage rows in every tenth session -- and ANALYZEs the table, so the planner
// knows a scan of the whole table costs far more than one session's rows read
// through an index on session_id.
func bulkSessionRecords(t *testing.T, s *PgStore, sessions int) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records
			(ts, session_id, record_type, profile_email, user_id, agent, model, uuid, message_id, raw,
			 input_tokens, output_tokens, cache_read_tokens)
		SELECT $1::timestamptz + s * interval '1 hour' + n * interval '1 second',
			'bulk-' || s,
			CASE WHEN n = 0 THEN 'user' WHEN s % 10 = 0 THEN 'usage' ELSE 'assistant' END,
			'bulk@example.com', 'uid-bulk',
			CASE WHEN s % 10 = 0 THEN 'codex' ELSE 'claude' END,
			'bulk-model', 'bulk-' || s || '-' || n,
			CASE WHEN n > 0 AND s % 10 <> 0 THEN 'msg_' || s || '_' || (n + 1) / 2 END,
			CASE WHEN n = 0 THEN '{"message":{"role":"user","content":"go"}}'::jsonb ELSE '{}'::jsonb END,
			CASE WHEN n > 0 THEN 10 END, CASE WHEN n > 0 THEN 5 END, CASE WHEN n > 0 THEN 0 END
		FROM generate_series(1, $2::int) s, generate_series(0, 39) n`, rsBase, sessions); err != nil {
		t.Fatalf("write bulk session records: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `ANALYZE session_records`); err != nil {
		t.Fatalf("analyze session_records: %v", err)
	}
}

// sessionRecordsSeqScans is the sequential scans, in the plan PostgreSQL makes for
// query with args, of the chunks of session_records that hold rows; and the plan.
// An empty chunk that an earlier test left behind is scanned sequentially at no
// cost, and says nothing.
func sessionRecordsSeqScans(t *testing.T, s *PgStore, query string, args ...any) ([]string, string) {
	t.Helper()
	ctx := context.Background()
	relations := map[string]bool{}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT c.relname::text FROM session_records sr
		JOIN pg_class c ON c.oid = sr.tableoid`)
	if err != nil {
		t.Fatalf("read the chunks: %v", err)
	}
	chunks, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("read the chunks: %v", err)
	}
	for _, c := range chunks {
		relations[c] = true
	}
	rows, err = s.pool.Query(ctx, "EXPLAIN "+query, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	plan, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	var seqScans []string
	for _, line := range plan {
		_, scanned, ok := strings.Cut(line, "Seq Scan on ")
		if ok && relations[strings.Fields(scanned)[0]] {
			seqScans = append(seqScans, strings.TrimSpace(line))
		}
	}
	return seqScans, strings.Join(plan, "\n")
}

// H1 of the P1t-5 review: a sync's refresh of one session reads that session's
// rows through an index on session_id, never the whole table. Every arm of
// syncAttributedRowsSQL filters by (filter IS NULL OR session_id = ANY(filter)): a
// parameter folds to an index condition, while a subquery is an InitPlan the
// planner can neither fold nor use as one, and the arm scans every row. Checked
// on the texts a sync runs -- the plugin facts and task segment refreshes -- and
// on step 8's UPDATE, over enough rows that a whole-table scan is no planner's
// first choice for one session.
func TestFactRefreshesReadOneSessionByIndex(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	bulkSessionRecords(t, s, 500)
	one := []string{"bulk-7"}
	for _, c := range []struct {
		name, query string
		args        []any
	}{
		{"the plugin facts refresh", pluginInvocationFactsRefreshSQL, []any{one}},
		{"the task segment facts refresh", taskSegmentFactsRefreshSQL, []any{one, activitylabel.Version}},
		{"step 8's update", taskSegmentTokensUpdateSQL, []any{one}},
	} {
		if seqScans, plan := sessionRecordsSeqScans(t, s, c.query, c.args...); len(seqScans) > 0 {
			t.Errorf("%s scans session_records whole for one session: %q\n%s", c.name, seqScans, plan)
		}
	}
}

// H1 and R1 of the P1t-5 review, the plan cache: a fact refresh runs unprepared,
// so PostgreSQL plans every execution for the list it is given. Prepared -- pgx's
// default -- a whole-table execution among a connection's first five, a NULL or a
// long list, can tip the statement into a generic plan for good, which cannot
// fold $1 IS NULL and scans all of session_records on every later refresh of the
// connection. Whether it tips depends on the table's size and the plans' costs,
// so the test pins the cause on one connection: after the two rebuilds, a refresh
// of a long list and of one session each, neither refresh text is a prepared
// statement there, and the rebuilds ran texts of their own -- the task segment
// one prepared under its own text (pgx runs the plugin one, which has no
// arguments, unprepared as well).
func TestFactRefreshesRunUnprepared(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire a connection: %v", err)
	}
	defer conn.Release()
	inTx := func(what string, fn func(pgx.Tx) error) {
		t.Helper()
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatalf("%s: begin: %v", what, err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
		if err := fn(tx); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("%s: commit: %v", what, err)
		}
	}
	long := make([]string, 500)
	for i := range long {
		long[i] = fmt.Sprintf("many-%03d", i)
	}

	inTx("rebuild the plugin facts", func(tx pgx.Tx) error { return rebuildAllPluginInvocationFacts(ctx, tx) })
	inTx("rebuild the task segment facts", func(tx pgx.Tx) error { return rebuildAllTaskSegmentFacts(ctx, tx) })
	for _, sessions := range [][]string{long, {"one"}} {
		inTx("refresh", func(tx pgx.Tx) error {
			if err := refreshPluginInvocationFacts(ctx, tx, sessions); err != nil {
				return err
			}
			return refreshTaskSegmentFacts(ctx, tx, sessions)
		})
	}
	rows, err := conn.Query(ctx, `SELECT statement FROM pg_prepared_statements`)
	if err != nil {
		t.Fatalf("read pg_prepared_statements: %v", err)
	}
	statements, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("read pg_prepared_statements: %v", err)
	}
	for name, text := range map[string]string{
		"plugin facts": pluginInvocationFactsRefreshSQL, "task segment facts": taskSegmentFactsRefreshSQL,
	} {
		if slices.Contains(statements, text) {
			t.Errorf("the %s refresh is a prepared statement on the connection: a whole-table execution can tip "+
				"its plan for every later refresh", name)
		}
	}
	if pluginInvocationFactsRebuildSQL == pluginInvocationFactsRefreshSQL || taskSegmentFactsRebuildSQL == taskSegmentFactsRefreshSQL {
		t.Error("a rebuild runs the refresh's text")
	}
	if !slices.Contains(statements, taskSegmentFactsRebuildSQL) {
		t.Error("the task segment rebuild did not run as a prepared statement of its own text")
	}
}
