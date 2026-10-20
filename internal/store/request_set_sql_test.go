package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// Tests of the request set (request_set_sql.go, p1t-design §8.1). Expected values
// come from rsFixture.expect, which states the rule of p1t-design §2.2 in Go over
// the fixture's own lines, and from numbers written out beside each fixture --
// never from running the shared SQL a second time.
//
// Model names are placeholders: no test here reads a cost, and a Claude line is a
// request line whatever its model unless the model is <synthetic>.

var rsBase = time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)

// rsAll keeps every line, as lineWhere "TRUE" does.
func rsAll(rsLine) bool { return true }

// rsTokens is input, output, cache_read and cache_create; nil is NULL.
type rsTokens [4]*int64

func rsTok(in, out, cr, cw int64) rsTokens { return rsTokens{&in, &out, &cr, &cw} }

func ptrI64(n int64) *int64 { return &n }

var rsZero = rsTok(0, 0, 0, 0)

func (v rsTokens) String() string {
	parts := make([]string, len(v))
	for i, p := range v {
		parts[i] = "NULL"
		if p != nil {
			parts[i] = fmt.Sprint(*p)
		}
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func rsEqual(a, b rsTokens) bool {
	for i := range a {
		if (a[i] == nil) != (b[i] == nil) || (a[i] != nil && *a[i] != *b[i]) {
			return false
		}
	}
	return true
}

// rsMax is each field's MAX, NULL only where every operand is NULL -- SQL's max.
func rsMax(a, b rsTokens) rsTokens {
	for i := range a {
		if b[i] != nil && (a[i] == nil || *b[i] > *a[i]) {
			a[i] = b[i]
		}
	}
	return a
}

// rsLine is one session_records row of a fixture. tag names it: the insert writes
// the tag into raw, which is how a test finds the row's id, the head order's last
// tie-break.
type rsLine struct {
	tag     string
	session string
	uuid    string
	at      time.Duration // after rsBase
	agent   string        // claude when empty
	recType string        // assistant when empty
	model   string
	msgID   string // raw message.id; the line has none when empty
	raw     string // further raw keys, as a JSON object
	user    string // uid-rs when empty
	profile string // rs@example.com when empty
	login   string
	project string
	command string // command_name: the line is a plugin command

	in, out, cr, cw *int
}

func (l rsLine) userID() string {
	if l.user == "" {
		return "uid-rs"
	}
	return l.user
}

func (l rsLine) profileEmail() string {
	if l.profile == "" {
		return "rs@example.com"
	}
	return l.profile
}

func (l rsLine) agentName() string {
	if l.agent == "" {
		return "claude"
	}
	return l.agent
}

func (l rsLine) recordType() string {
	if l.recType == "" {
		return "assistant"
	}
	return l.recType
}

func (l rsLine) isRequestLine() bool {
	return l.agentName() == "claude" && l.recordType() == "assistant" && l.model != "<synthetic>"
}

func (l rsLine) tokens() rsTokens {
	var v rsTokens
	for i, p := range []*int{l.in, l.out, l.cr, l.cw} {
		if p != nil {
			v[i] = ptrI64(int64(*p))
		}
	}
	return v
}

// rsFixture is a fixture as stored: its lines and their row ids.
type rsFixture struct {
	lines []rsLine
	ids   map[string]int64
	tags  map[int64]string
}

// insertRequestSet stores lines in order, so ids rise in the order given.
func insertRequestSet(t *testing.T, s *PgStore, lines ...rsLine) *rsFixture {
	t.Helper()
	ctx := context.Background()
	records := make([]*SessionRecord, 0, len(lines))
	for _, l := range lines {
		raw := map[string]any{}
		if l.raw != "" {
			if err := json.Unmarshal([]byte(l.raw), &raw); err != nil {
				t.Fatalf("fixture %s: raw: %v", l.tag, err)
			}
		}
		raw["tag"] = l.tag
		if l.msgID != "" {
			raw["message"] = map[string]any{"id": l.msgID}
		}
		b, err := json.Marshal(raw)
		if err != nil {
			t.Fatalf("fixture %s: %v", l.tag, err)
		}
		records = append(records, &SessionRecord{
			Ts: rsBase.Add(l.at), SessionID: l.session, UUID: l.uuid, RecordType: l.recordType(),
			ProfileEmail: l.profileEmail(), UserID: l.userID(), LoginEmail: l.login, ProjectHash: l.project,
			Model: l.model, Agent: l.agentName(), CommandName: l.command,
			InputTokens: l.in, OutputTokens: l.out, CacheReadTokens: l.cr, CacheCreateTokens: l.cw,
			Raw: json.RawMessage(b),
		})
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	f := &rsFixture{lines: lines, ids: map[string]int64{}, tags: map[int64]string{}}
	rows, err := s.pool.Query(ctx, `SELECT raw->>'tag', id FROM session_records`)
	if err != nil {
		t.Fatalf("read fixture ids: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tag string
		var id int64
		if err := rows.Scan(&tag, &id); err != nil {
			t.Fatalf("read fixture ids: %v", err)
		}
		f.ids[tag], f.tags[id] = id, tag
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read fixture ids: %v", err)
	}
	for _, l := range lines {
		if _, ok := f.ids[l.tag]; !ok {
			t.Fatalf("fixture line %s was not stored -- it shares a unique key with another", l.tag)
		}
	}
	return f
}

func (f *rsFixture) id(tag string) int64 { return f.ids[tag] }

// expect states p1t-design §2.2 over the fixture: the heads, and the attributed
// tokens of every row keep selects, each by row id. codex is the
// codex_imputed_cost each Codex usage row should have, by tag; a row it leaves out
// has none.
func (f *rsFixture) expect(keep func(rsLine) bool, codex map[string]rsTokens) (heads, attributed map[int64]rsTokens) {
	type request struct {
		head rsLine
		max  rsTokens
	}
	// The head order: ts, then a line with a uuid before one without, then id.
	before := func(a, b rsLine) bool {
		if a.at != b.at {
			return a.at < b.at
		}
		if (a.uuid == "") != (b.uuid == "") {
			return a.uuid != ""
		}
		return f.ids[a.tag] < f.ids[b.tag]
	}
	requests := map[[2]string]*request{}
	for _, l := range f.lines {
		if !keep(l) || !l.isRequestLine() {
			continue
		}
		key := l.msgID
		if key == "" {
			key = fmt.Sprintf("row:%d", f.ids[l.tag])
		}
		r := requests[[2]string{l.session, key}]
		if r == nil {
			r = &request{head: l}
			requests[[2]string{l.session, key}] = r
		} else if before(l, r.head) {
			r.head = l
		}
		r.max = rsMax(r.max, l.tokens())
	}
	heads = map[int64]rsTokens{}
	for _, r := range requests {
		heads[f.ids[r.head.tag]] = r.max
	}
	attributed = map[int64]rsTokens{}
	for _, l := range f.lines {
		if !keep(l) {
			continue
		}
		id := f.ids[l.tag]
		switch {
		case l.isRequestLine():
			attributed[id] = rsZero
			if v, ok := heads[id]; ok {
				attributed[id] = v
			}
		case l.agentName() == "codex" && l.recordType() == "usage":
			attributed[id] = rsZero
			if v, ok := codex[l.tag]; ok {
				attributed[id] = v
			}
		case l.agentName() == "codex":
			attributed[id] = rsZero
		default:
			attributed[id] = l.tokens()
		}
	}
	return heads, attributed
}

// rows reads a query whose columns are a row id and four token columns, by id.
func (f *rsFixture) rows(t *testing.T, s *PgStore, q string, args ...any) map[int64]rsTokens {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), q, args...)
	if err != nil {
		t.Fatalf("query: %v\n%s", err, q)
	}
	defer rows.Close()
	out := map[int64]rsTokens{}
	for rows.Next() {
		var id int64
		var v rsTokens
		if err := rows.Scan(&id, &v[0], &v[1], &v[2], &v[3]); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if _, dup := out[id]; dup {
			t.Fatalf("row %s returned twice", f.tags[id])
		}
		out[id] = v
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

func (f *rsFixture) heads(t *testing.T, s *PgStore, source, lineWhere string, args ...any) map[int64]rsTokens {
	t.Helper()
	return f.rows(t, s, `SELECT id, input_tokens, output_tokens, cache_read_tokens, cache_create_tokens
		FROM (`+claudeRequestHeadsSQL(source, lineWhere, []string{"id"})+`) h`, args...)
}

func (f *rsFixture) attributed(t *testing.T, s *PgStore, source, lineWhere string, args ...any) map[int64]rsTokens {
	t.Helper()
	return f.rows(t, s, `SELECT id, req_input_tokens, req_output_tokens, req_cache_read_tokens, req_cache_create_tokens
		FROM (`+attributedRowsSQL(source, lineWhere, []string{"id"})+`) a`, args...)
}

func (f *rsFixture) compare(t *testing.T, what string, got, want map[int64]rsTokens) {
	t.Helper()
	for id, w := range want {
		g, ok := got[id]
		switch {
		case !ok:
			t.Errorf("%s: row %s is missing, want %s", what, f.tags[id], w)
		case !rsEqual(g, w):
			t.Errorf("%s: row %s = %s, want %s", what, f.tags[id], g, w)
		}
	}
	for id, g := range got {
		if _, ok := want[id]; !ok {
			t.Errorf("%s: row %s = %s is not expected", what, f.tags[id], g)
		}
	}
}

// check runs both shapes over source and compares each with expect.
func (f *rsFixture) check(t *testing.T, s *PgStore, source, lineWhere string, keep func(rsLine) bool,
	codex map[string]rsTokens, args ...any) (heads, attributed map[int64]rsTokens) {
	t.Helper()
	wantHeads, wantAttributed := f.expect(keep, codex)
	heads = f.heads(t, s, source, lineWhere, args...)
	attributed = f.attributed(t, s, source, lineWhere, args...)
	f.compare(t, "heads", heads, wantHeads)
	f.compare(t, "attributed rows", attributed, wantAttributed)
	return heads, attributed
}

// headIdentity reads each head line's own columns, by head id: its ts as an offset
// from rsBase, then uuid, login, project and model.
func (f *rsFixture) headIdentity(t *testing.T, s *PgStore) map[int64]string {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), `SELECT id, ts, uuid, login_email, project_hash, model FROM (`+
		claudeRequestHeadsSQL("session_records", "TRUE",
			[]string{"id", "ts", "uuid", "login_email", "project_hash", "model"})+`) h`)
	if err != nil {
		t.Fatalf("read head identity: %v", err)
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var ts time.Time
		var uuid, login, project, model string
		if err := rows.Scan(&id, &ts, &uuid, &login, &project, &model); err != nil {
			t.Fatalf("scan head identity: %v", err)
		}
		out[id] = strings.Join([]string{ts.Sub(rsBase).String(), uuid, login, project, model}, "|")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read head identity: %v", err)
	}
	return out
}

// One response written as three lines, each repeating its usage, is one request
// with one line's usage -- not three. And a response whose lines carry output still
// growing (1, 1, 523) is one request with output 523 and input counted once.
func TestRequestSet_ResponseLinesFoldIntoOneRequest(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	f := insertRequestSet(t, s,
		rsLine{tag: "same-1", session: "rs-same", uuid: "same-1", msgID: "msg_same",
			in: ptrInt(10), out: ptrInt(523), cr: ptrInt(1000), cw: ptrInt(50)},
		rsLine{tag: "same-2", session: "rs-same", uuid: "same-2", at: time.Second, msgID: "msg_same",
			in: ptrInt(10), out: ptrInt(523), cr: ptrInt(1000), cw: ptrInt(50)},
		rsLine{tag: "same-3", session: "rs-same", uuid: "same-3", at: 2 * time.Second, msgID: "msg_same",
			in: ptrInt(10), out: ptrInt(523), cr: ptrInt(1000), cw: ptrInt(50)},
		rsLine{tag: "grow-1", session: "rs-grow", uuid: "grow-1", msgID: "msg_grow",
			in: ptrInt(7), out: ptrInt(1), cr: ptrInt(900), cw: ptrInt(30)},
		rsLine{tag: "grow-2", session: "rs-grow", uuid: "grow-2", at: time.Second, msgID: "msg_grow",
			in: ptrInt(7), out: ptrInt(1), cr: ptrInt(900), cw: ptrInt(30)},
		rsLine{tag: "grow-3", session: "rs-grow", uuid: "grow-3", at: 2 * time.Second, msgID: "msg_grow",
			in: ptrInt(7), out: ptrInt(523), cr: ptrInt(900), cw: ptrInt(30)},
	)
	heads, attributed := f.check(t, s, "session_records", "TRUE", rsAll, nil)

	if len(heads) != 2 {
		t.Fatalf("requests = %d, want 2", len(heads))
	}
	if got, want := heads[f.id("same-1")], rsTok(10, 523, 1000, 50); !rsEqual(got, want) {
		t.Errorf("three lines of one response = %s, want one line's usage %s", got, want)
	}
	if got, want := heads[f.id("grow-1")], rsTok(7, 523, 900, 30); !rsEqual(got, want) {
		t.Errorf("growing output = %s, want output 523 with input once %s", got, want)
	}
	for _, tag := range []string{"same-2", "same-3", "grow-2", "grow-3"} {
		if got := attributed[f.id(tag)]; !rsEqual(got, rsZero) {
			t.Errorf("%s is not its request's head but carries %s", tag, got)
		}
	}
}

// A <synthetic> line is not a request line, so it is neither grouped with the
// response whose id it carries nor counted as one: the response's MAX and head come
// from its real lines, and the <synthetic> line passes through with its own counts.
// Its 999s would show in the MAX, and its earlier ts would take the head, if the
// predicate let it into the partition.
func TestRequestSet_SyntheticLinesStayOutOfRequests(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	f := insertRequestSet(t, s,
		rsLine{tag: "synthetic", session: "rs-synth", uuid: "synthetic", msgID: "msg_synth", model: "<synthetic>",
			in: ptrInt(999), out: ptrInt(999), cr: ptrInt(999), cw: ptrInt(999)},
		rsLine{tag: "real-1", session: "rs-synth", uuid: "real-1", at: time.Second, msgID: "msg_synth",
			model: "model-a", in: ptrInt(20), out: ptrInt(5), cr: ptrInt(300), cw: ptrInt(0)},
		rsLine{tag: "real-2", session: "rs-synth", uuid: "real-2", at: 2 * time.Second, msgID: "msg_synth",
			model: "model-a", in: ptrInt(20), out: ptrInt(40), cr: ptrInt(300), cw: ptrInt(0)},
	)
	heads, attributed := f.check(t, s, "session_records", "TRUE", rsAll, nil)

	if got, want := heads[f.id("real-1")], rsTok(20, 40, 300, 0); len(heads) != 1 || !rsEqual(got, want) {
		t.Errorf("heads = %v, want only real-1 with %s", heads, want)
	}
	if got, want := attributed[f.id("synthetic")], rsTok(999, 999, 999, 999); !rsEqual(got, want) {
		t.Errorf("the <synthetic> line = %s, want it passed through as %s", got, want)
	}
}

// A line without an id is its own request: the row key, never one key shared by
// every id-less line of the session. An explicit empty id is the same as none.
func TestRequestSet_LinesWithoutAnIDAreTheirOwnRequests(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	f := insertRequestSet(t, s,
		rsLine{tag: "noid-1", session: "rs-noid", uuid: "noid-1", in: ptrInt(3), out: ptrInt(4)},
		rsLine{tag: "noid-2", session: "rs-noid", uuid: "noid-2", at: time.Second, in: ptrInt(5), out: ptrInt(6)},
		rsLine{tag: "emptyid", session: "rs-noid", uuid: "emptyid", at: 2 * time.Second,
			raw: `{"message":{"id":""}}`, in: ptrInt(7), out: ptrInt(8)},
	)
	heads, _ := f.check(t, s, "session_records", "TRUE", rsAll, nil)

	if len(heads) != 3 {
		t.Fatalf("requests = %d, want 3 -- lines without an id were folded together", len(heads))
	}
}

// Twins -- one JSONL line stored twice in the same slot, once with an empty uuid and
// once with its uuid -- fold into one request, and the head is the twin with the
// uuid even though it was stored later. The empty-uuid rows go in first so that an
// order by id alone would pick them, and the two copies disagree on login and
// project so the head's identity shows which one led. When ts and uuid both tie,
// the lower id leads.
func TestRequestSet_TwinsFoldAndTheLineWithAUUIDLeads(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	f := insertRequestSet(t, s,
		rsLine{tag: "twin-empty-1", session: "rs-twin", msgID: "msg_twin", login: "empty@example.com",
			project: "p-empty", model: "model-a", in: ptrInt(100), out: ptrInt(1), cr: ptrInt(50), cw: ptrInt(5)},
		rsLine{tag: "twin-empty-2", session: "rs-twin", at: time.Second, msgID: "msg_twin", login: "empty@example.com",
			project: "p-empty", model: "model-a", in: ptrInt(100), out: ptrInt(80), cr: ptrInt(50), cw: ptrInt(5)},
		rsLine{tag: "twin-uuid-1", session: "rs-twin", uuid: "U1", msgID: "msg_twin", login: "uuid@example.com",
			project: "p-uuid", model: "model-a", in: ptrInt(100), out: ptrInt(1), cr: ptrInt(50), cw: ptrInt(5)},
		rsLine{tag: "twin-uuid-2", session: "rs-twin", uuid: "U2", at: time.Second, msgID: "msg_twin",
			login: "uuid@example.com", project: "p-uuid", model: "model-a",
			in: ptrInt(100), out: ptrInt(80), cr: ptrInt(50), cw: ptrInt(5)},
		rsLine{tag: "tie-first", session: "rs-tie", uuid: "T1", msgID: "msg_tie", login: "first@example.com",
			project: "p-first", model: "m-first", in: ptrInt(9), out: ptrInt(1)},
		rsLine{tag: "tie-second", session: "rs-tie", uuid: "T2", msgID: "msg_tie", login: "second@example.com",
			project: "p-second", model: "m-second", in: ptrInt(9), out: ptrInt(523)},
	)
	heads, attributed := f.check(t, s, "session_records", "TRUE", rsAll, nil)

	if got, want := heads[f.id("twin-uuid-1")], rsTok(100, 80, 50, 5); len(heads) != 2 || !rsEqual(got, want) {
		t.Errorf("heads = %v, want twin-uuid-1 with %s and tie-first", heads, want)
	}
	if got := attributed[f.id("twin-empty-1")]; !rsEqual(got, rsZero) {
		t.Errorf("the empty-uuid twin carries %s, want 0 -- it is the same line as twin-uuid-1", got)
	}
	identity := f.headIdentity(t, s)
	if got, want := identity[f.id("twin-uuid-1")], "0s|U1|uuid@example.com|p-uuid|model-a"; got != want {
		t.Errorf("twin head identity = %q, want %q", got, want)
	}
	if got, want := identity[f.id("tie-first")], "0s|T1|first@example.com|p-first|m-first"; got != want {
		t.Errorf("tied head identity = %q, want %q (the lower id)", got, want)
	}
}

// message_id (P1t-1) answers the key without reading raw, and NULL -- a row the
// backfill has not reached -- falls back to the same id in raw. So the requests are
// the same with the column filled, half filled (one request's lines split between
// the two states) and empty. The column wins where the two disagree: a filled
// column is the key even against raw, and an empty string is a filled row without an
// id, which keeps the line its own request whatever raw says.
func TestRequestSet_MessageIDColumnAndRawGiveTheSameRequests(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	f := insertRequestSet(t, s,
		rsLine{tag: "a-1", session: "rs-mid", uuid: "a-1", msgID: "msg_a", in: ptrInt(10), out: ptrInt(1)},
		rsLine{tag: "a-2", session: "rs-mid", uuid: "a-2", at: time.Second, msgID: "msg_a", in: ptrInt(10), out: ptrInt(60)},
		rsLine{tag: "a-3", session: "rs-mid", uuid: "a-3", at: 2 * time.Second, msgID: "msg_a", in: ptrInt(10), out: ptrInt(70)},
		rsLine{tag: "b-1", session: "rs-mid", uuid: "b-1", at: 3 * time.Second, msgID: "msg_b", in: ptrInt(20), out: ptrInt(5)},
		rsLine{tag: "b-2", session: "rs-mid", uuid: "b-2", at: 4 * time.Second, msgID: "msg_b", in: ptrInt(20), out: ptrInt(9)},
		rsLine{tag: "noid", session: "rs-mid", uuid: "noid", at: 5 * time.Second, in: ptrInt(30), out: ptrInt(3)},
		rsLine{tag: "twin-empty", session: "rs-mid", at: 6 * time.Second, msgID: "msg_c", in: ptrInt(40), out: ptrInt(4)},
		rsLine{tag: "twin-uuid", session: "rs-mid", uuid: "C", at: 6 * time.Second, msgID: "msg_c", in: ptrInt(40), out: ptrInt(4)},
	)
	unfilled := func() int {
		var n int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM session_records
			WHERE agent = 'claude' AND record_type = 'assistant' AND message_id IS NULL`).Scan(&n); err != nil {
			t.Fatalf("count unfilled rows: %v", err)
		}
		return n
	}
	if n := unfilled(); n != 0 {
		t.Fatalf("%d row(s) inserted without message_id -- the insert did not fill it", n)
	}
	filledHeads, filledAttributed := f.check(t, s, "session_records", "TRUE", rsAll, nil)

	for _, step := range []struct{ name, update string }{
		{"half filled", `UPDATE session_records SET message_id = NULL WHERE id % 2 = 0`},
		{"unfilled", `UPDATE session_records SET message_id = NULL`},
	} {
		if _, err := s.pool.Exec(ctx, step.update); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if unfilled() == 0 {
			t.Fatalf("%s: no row lost its message_id", step.name)
		}
		heads, attributed := f.check(t, s, "session_records", "TRUE", rsAll, nil)
		f.compare(t, step.name+" heads against filled", heads, filledHeads)
		f.compare(t, step.name+" attributed rows against filled", attributed, filledAttributed)
	}

	// The column against raw: b-2 is filled with a's id, a-3 with the empty string.
	for tag, id := range map[string]string{"b-2": "msg_a", "a-3": ""} {
		if _, err := s.pool.Exec(ctx, `UPDATE session_records SET message_id = $1 WHERE id = $2`, id, f.id(tag)); err != nil {
			t.Fatalf("set message_id of %s: %v", tag, err)
		}
	}
	f.compare(t, "heads with the column against raw", f.heads(t, s, "session_records", "TRUE"), map[int64]rsTokens{
		// a-1, a-2 and b-2 (by its column); output 60 from a-2.
		f.id("a-1"): {ptrI64(20), ptrI64(60), nil, nil},
		// a-3 alone: its empty column is a filled row with no id, so raw is not read.
		f.id("a-3"): {ptrI64(10), ptrI64(70), nil, nil},
		// b-1 alone: b-2 left it by its column.
		f.id("b-1"):       {ptrI64(20), ptrI64(5), nil, nil},
		f.id("noid"):      {ptrI64(30), ptrI64(3), nil, nil},
		f.id("twin-uuid"): {ptrI64(40), ptrI64(4), nil, nil},
	})
}

// The same message id in two sessions is a request in each. Whether one is a copy
// of the other is P1b's question (p1t-design D2); until then each session counts
// it, and this pins that.
func TestRequestSet_SameMessageIDInTwoSessionsCountsInEach(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	f := insertRequestSet(t, s,
		rsLine{tag: "a-1", session: "rs-copy-a", uuid: "a-1", msgID: "msg_copy", in: ptrInt(10), out: ptrInt(20)},
		rsLine{tag: "a-2", session: "rs-copy-a", uuid: "a-2", at: time.Second, msgID: "msg_copy", in: ptrInt(10), out: ptrInt(30)},
		rsLine{tag: "b-1", session: "rs-copy-b", uuid: "b-1", msgID: "msg_copy", in: ptrInt(10), out: ptrInt(30)},
	)
	heads, _ := f.check(t, s, "session_records", "TRUE", rsAll, nil)

	if len(heads) != 2 {
		t.Fatalf("requests = %d, want 2 -- one per session", len(heads))
	}
}

// An id-collision group -- one key over lines that disagree on input, cache_read and
// model -- is one request taking each field's MAX, with the head line's model.
// Production has 122 such groups, none with a <synthetic> line. If they were
// separate requests this undercounts; that is the pinned behaviour, and its size is
// reported as a diagnostic (p1t-design §2.2, §9).
func TestRequestSet_IDCollisionTakesEachFieldsMax(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	f := insertRequestSet(t, s,
		rsLine{tag: "first", session: "rs-collide", uuid: "first", msgID: "msg_collide", model: "model-a",
			in: ptrInt(100), out: ptrInt(50), cr: ptrInt(5000), cw: ptrInt(10)},
		rsLine{tag: "second", session: "rs-collide", uuid: "second", at: time.Second, msgID: "msg_collide",
			model: "model-b", in: ptrInt(3000), out: ptrInt(20), cr: ptrInt(10), cw: ptrInt(300)},
	)
	heads, _ := f.check(t, s, "session_records", "TRUE", rsAll, nil)

	if got, want := heads[f.id("first")], rsTok(3000, 50, 5000, 300); len(heads) != 1 || !rsEqual(got, want) {
		t.Errorf("heads = %v, want one request with %s", heads, want)
	}
	if got := f.headIdentity(t, s)[f.id("first")]; !strings.HasSuffix(got, "|model-a") {
		t.Errorf("head identity = %q, want the first line's model", got)
	}
}

// A line with input and no output, or output and no input, keeps the side it has --
// in a request of its own and in a request whose lines each bring one side.
func TestRequestSet_OneSidedLinesKeepTheirSide(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	f := insertRequestSet(t, s,
		rsLine{tag: "input-only", session: "rs-side", uuid: "input-only", msgID: "msg_in", in: ptrInt(100)},
		rsLine{tag: "output-only", session: "rs-side", uuid: "output-only", at: time.Second, msgID: "msg_out", out: ptrInt(200)},
		rsLine{tag: "mixed-in", session: "rs-side", uuid: "mixed-in", at: 2 * time.Second, msgID: "msg_mix", in: ptrInt(100)},
		rsLine{tag: "mixed-out", session: "rs-side", uuid: "mixed-out", at: 3 * time.Second, msgID: "msg_mix", out: ptrInt(300)},
	)
	heads, attributed := f.check(t, s, "session_records", "TRUE", rsAll, nil)

	for tag, want := range map[string]rsTokens{
		"input-only":  {ptrI64(100), nil, nil, nil},
		"output-only": {nil, ptrI64(200), nil, nil},
		"mixed-in":    {ptrI64(100), ptrI64(300), nil, nil},
	} {
		if got := heads[f.id(tag)]; !rsEqual(got, want) {
			t.Errorf("head %s = %s, want %s", tag, got, want)
		}
		if got := attributed[f.id(tag)]; !rsEqual(got, want) {
			t.Errorf("attributed %s = %s, want %s", tag, got, want)
		}
	}
}

// rsMixed is a fixture with every kind of row the rule tells apart: repeated and
// growing lines, a <synthetic> line, id-less lines, twins, a collision, one-sided
// lines, a copy in another session, Claude's other rows, gjc, omo with NULL tokens,
// and Codex rows. The map is the codex_imputed_cost it should produce, by tag.
func rsMixed() ([]rsLine, map[string]rsTokens) {
	usageRecord := `{"type":"token_usage_record","payload":{"usage":{"input_tokens":1}}}`
	tokenCount := `{"type":"event_msg","payload":{"type":"token_count","info":{}}}`
	return []rsLine{
		{tag: "same-1", session: "mx-a", uuid: "same-1", msgID: "msg_same", in: ptrInt(10), out: ptrInt(523), cr: ptrInt(1000), cw: ptrInt(50)},
		{tag: "same-2", session: "mx-a", uuid: "same-2", at: time.Second, msgID: "msg_same", in: ptrInt(10), out: ptrInt(523), cr: ptrInt(1000), cw: ptrInt(50)},
		{tag: "same-3", session: "mx-a", uuid: "same-3", at: 2 * time.Second, msgID: "msg_same", in: ptrInt(10), out: ptrInt(523), cr: ptrInt(1000), cw: ptrInt(50)},
		{tag: "grow-1", session: "mx-a", uuid: "grow-1", at: 3 * time.Second, msgID: "msg_grow", in: ptrInt(7), out: ptrInt(1), cr: ptrInt(900)},
		{tag: "grow-2", session: "mx-a", uuid: "grow-2", at: 4 * time.Second, msgID: "msg_grow", in: ptrInt(7), out: ptrInt(523), cr: ptrInt(900)},
		{tag: "synthetic", session: "mx-a", uuid: "synthetic", at: 3 * time.Second, msgID: "msg_grow", model: "<synthetic>",
			in: ptrInt(999), out: ptrInt(999), cr: ptrInt(999), cw: ptrInt(999)},
		{tag: "noid-1", session: "mx-a", uuid: "noid-1", at: 5 * time.Second, in: ptrInt(3), out: ptrInt(4)},
		{tag: "noid-2", session: "mx-a", uuid: "noid-2", at: 5 * time.Second, in: ptrInt(5), out: ptrInt(6)},
		{tag: "twin-empty", session: "mx-b", msgID: "msg_twin", in: ptrInt(100), out: ptrInt(80), cr: ptrInt(50), cw: ptrInt(5)},
		{tag: "twin-uuid", session: "mx-b", uuid: "U", msgID: "msg_twin", in: ptrInt(100), out: ptrInt(80), cr: ptrInt(50), cw: ptrInt(5)},
		{tag: "collide-1", session: "mx-b", uuid: "collide-1", at: time.Second, msgID: "msg_collide", model: "model-a",
			in: ptrInt(100), out: ptrInt(50), cr: ptrInt(5000), cw: ptrInt(10)},
		{tag: "collide-2", session: "mx-b", uuid: "collide-2", at: 2 * time.Second, msgID: "msg_collide", model: "model-b",
			in: ptrInt(3000), out: ptrInt(20), cr: ptrInt(10), cw: ptrInt(300)},
		{tag: "side-in", session: "mx-b", uuid: "side-in", at: 3 * time.Second, msgID: "msg_side", in: ptrInt(100)},
		{tag: "side-out", session: "mx-b", uuid: "side-out", at: 4 * time.Second, msgID: "msg_side", out: ptrInt(300)},
		{tag: "copy", session: "mx-c", uuid: "copy", at: 2 * time.Second, msgID: "msg_same", in: ptrInt(10), out: ptrInt(523)},
		{tag: "claude-user", session: "mx-a", uuid: "claude-user", at: 6 * time.Second, recType: "user", in: ptrInt(120)},
		{tag: "gjc", session: "mx-gjc", uuid: "gjc", at: 2 * time.Second, agent: "gjc", msgID: "msg_gjc",
			in: ptrInt(11), out: ptrInt(12), cr: ptrInt(13), cw: ptrInt(14)},
		{tag: "omo-null", session: "mx-omo", uuid: "omo-null", at: 2 * time.Second, agent: "omo"},
		{tag: "cx-record", session: "mx-cx", uuid: "cx-record", agent: "codex", recType: "usage", model: "codex-model",
			raw: usageRecord, in: ptrInt(1000), out: ptrInt(100), cr: ptrInt(0)},
		{tag: "cx-mirror", session: "mx-cx", uuid: "cx-mirror", at: 2 * time.Second, agent: "codex", recType: "usage",
			model: "codex-model", raw: tokenCount, in: ptrInt(1000), out: ptrInt(100), cr: ptrInt(0)},
		{tag: "cx-cache", session: "mx-cx", uuid: "cx-cache", at: 3 * time.Second, agent: "codex", recType: "usage",
			model: "codex-model", in: ptrInt(500), out: ptrInt(0), cr: ptrInt(500)},
		{tag: "cx-other", session: "mx-cx", uuid: "cx-other", at: 3 * time.Second, agent: "codex", recType: "assistant",
			in: ptrInt(5), out: ptrInt(5)},
	}, map[string]rsTokens{
		"cx-record": rsTok(1000, 100, 0, 0),
		"cx-cache":  rsTok(0, 0, 500, 0),
	}
}

// The two shapes agree: summed over request lines, the attributed rows give each
// token field exactly what the heads give, because the head carries its request's
// MAX and every other line 0, under the same head order. Checked over the whole
// table and over a period that cuts requests at its start, read through
// visible_session_records as the read paths will.
func TestRequestSet_AttributedRowsSumToHeads(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	lines, codex := rsMixed()
	f := insertRequestSet(t, s, lines...)
	refreshCodexImputed(t, s)

	period := func(l rsLine) bool { return l.at >= 1500*time.Millisecond && l.at < time.Hour }
	for _, run := range []struct {
		name, source, lineWhere string
		keep                    func(rsLine) bool
		args                    []any
	}{
		{"whole table", "session_records", "TRUE", rsAll, nil},
		{"period", "visible_session_records", "sr.ts >= $1 AND sr.ts < $2", period,
			[]any{rsBase.Add(1500 * time.Millisecond), rsBase.Add(time.Hour)}},
	} {
		heads, attributed := f.check(t, s, run.source, run.lineWhere, run.keep, codex, run.args...)
		var headSum, lineSum [4]int64
		for _, v := range heads {
			for i, p := range v {
				if p != nil {
					headSum[i] += *p
				}
			}
		}
		for _, l := range f.lines {
			v, ok := attributed[f.id(l.tag)]
			if !ok || !l.isRequestLine() {
				continue
			}
			for i, p := range v {
				if p != nil {
					lineSum[i] += *p
				}
			}
		}
		if headSum != lineSum || headSum == [4]int64{} {
			t.Errorf("%s: request lines sum to %v, heads to %v", run.name, lineSum, headSum)
		}
	}
}

// Every row that is not a request line keeps its own counts: gjc and omo, NULL
// included -- omo leaves them NULL where the provider double-counts usage -- and
// Claude's other rows. Their raw carries a message id shared with other lines, the
// Claude user row the same one as an assistant line of its session, so a predicate
// that let them in would fold them into requests.
func TestRequestSet_OtherAgentsPassThroughUnchanged(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	f := insertRequestSet(t, s,
		rsLine{tag: "gjc-1", session: "rs-gjc", uuid: "gjc-1", agent: "gjc", msgID: "msg_gjc",
			in: ptrInt(11), out: ptrInt(12), cr: ptrInt(13), cw: ptrInt(14)},
		rsLine{tag: "gjc-2", session: "rs-gjc", uuid: "gjc-2", at: time.Second, agent: "gjc", msgID: "msg_gjc",
			in: ptrInt(21), out: ptrInt(22), cr: ptrInt(23), cw: ptrInt(24)},
		rsLine{tag: "omo-null", session: "rs-omo", uuid: "omo-null", agent: "omo", msgID: "msg_omo"},
		rsLine{tag: "omo-counted", session: "rs-omo", uuid: "omo-counted", at: time.Second, agent: "omo", msgID: "msg_omo",
			in: ptrInt(31), out: ptrInt(32)},
		rsLine{tag: "claude-user", session: "rs-claude", uuid: "claude-user", recType: "user", msgID: "msg_claude",
			in: ptrInt(120)},
		rsLine{tag: "claude-assistant", session: "rs-claude", uuid: "claude-assistant", at: time.Second, msgID: "msg_claude",
			in: ptrInt(5), out: ptrInt(6)},
	)
	heads, attributed := f.check(t, s, "session_records", "TRUE", rsAll, nil)

	if len(heads) != 1 {
		t.Errorf("heads = %v, want only claude-assistant", heads)
	}
	for tag, want := range map[string]rsTokens{
		"gjc-1":       rsTok(11, 12, 13, 14),
		"gjc-2":       rsTok(21, 22, 23, 24),
		"omo-null":    {},
		"omo-counted": {ptrI64(31), ptrI64(32), nil, nil},
		"claude-user": {ptrI64(120), nil, nil, nil},
	} {
		if got := attributed[f.id(tag)]; !rsEqual(got, want) {
			t.Errorf("%s = %s, want it unchanged %s", tag, got, want)
		}
	}
}

// A Codex usage row reads its codex_imputed_cost row -- input net of cache reads,
// behind the whole-session defences -- and 0 when the imputation dropped it: a
// token_count mirroring a token_usage_record, a repeated cumulative snapshot. The
// period run starts after the record and the first snapshot, so the rows that
// defend against the mirror and the repeat are outside it: recomputing the defences
// within the period would count both. Any other Codex row is 0.
func TestRequestSet_CodexUsageReadsTheImputedTable(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	snapshot := `{"payload":{"info":{"total_token_usage":{"input_tokens":10,"cached_input_tokens":1,"output_tokens":2}}}}`
	codexUsage := func(tag, session string, at time.Duration, raw string, in, out, cr int) rsLine {
		return rsLine{tag: tag, session: session, uuid: tag, at: at, agent: "codex", recType: "usage",
			model: "codex-model", raw: raw, in: ptrInt(in), out: ptrInt(out), cr: ptrInt(cr)}
	}
	f := insertRequestSet(t, s,
		codexUsage("record", "rs-cx-mirror", 0, `{"type":"token_usage_record","payload":{"usage":{"input_tokens":1}}}`, 1000, 100, 0),
		codexUsage("mirror", "rs-cx-mirror", time.Second, `{"type":"event_msg","payload":{"type":"token_count","info":{}}}`, 1000, 100, 0),
		codexUsage("snapshot", "rs-cx-repeat", 0, snapshot, 1000, 100, 0),
		codexUsage("repeat", "rs-cx-repeat", time.Second, snapshot, 1000, 100, 0),
		codexUsage("cache-only", "rs-cx-cache", 2*time.Second, "", 500, 0, 500),
		rsLine{tag: "assistant", session: "rs-cx-other", uuid: "assistant", agent: "codex", in: ptrInt(5), out: ptrInt(5)},
	)
	refreshCodexImputed(t, s)
	codex := map[string]rsTokens{
		"record":     rsTok(1000, 100, 0, 0),
		"snapshot":   rsTok(1000, 100, 0, 0),
		"cache-only": rsTok(0, 0, 500, 0),
	}
	want := map[int64]rsTokens{}
	for tag, v := range codex {
		want[f.id(tag)] = v
	}
	imputed := f.rows(t, s, `SELECT srec_id, input_tokens, output_tokens, cache_read_tokens, cache_create_tokens
		FROM codex_imputed_cost`)
	f.compare(t, "codex_imputed_cost", imputed, want)

	for _, run := range []struct {
		name, lineWhere string
		keep            func(rsLine) bool
		args            []any
	}{
		{"whole table", "TRUE", rsAll, nil},
		{"period after the defending rows", "sr.ts >= $1",
			func(l rsLine) bool { return l.at >= 500*time.Millisecond },
			[]any{rsBase.Add(500 * time.Millisecond)}},
	} {
		_, attributed := f.check(t, s, "session_records", run.lineWhere, run.keep, codex, run.args...)
		for _, l := range f.lines {
			got, ok := attributed[f.id(l.tag)]
			if !ok || l.recordType() != "usage" {
				continue
			}
			v, imputedRow := imputed[f.id(l.tag)]
			if !imputedRow {
				v = rsZero
			}
			if !rsEqual(got, v) {
				t.Errorf("%s: %s = %s, codex_imputed_cost gives %s", run.name, l.tag, got, v)
			}
		}
	}
}

// codexUsageRowsSQL reads the sessions its filter names, up to its id ceiling, and
// leaves out what the defences drop; NULL for either bound means none.
func TestCodexUsageRows_SessionFilterAndIDCeiling(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	usage := func(tag, session string, at time.Duration, raw string) rsLine {
		return rsLine{tag: tag, session: session, uuid: tag, at: at, agent: "codex", recType: "usage",
			model: "codex-model", raw: raw, in: ptrInt(10), out: ptrInt(1), cr: ptrInt(0)}
	}
	f := insertRequestSet(t, s,
		usage("a-1", "rs-cu-a", 0, ""), usage("a-2", "rs-cu-a", time.Second, ""), usage("a-3", "rs-cu-a", 2*time.Second, ""),
		usage("b-1", "rs-cu-b", 0, ""),
		usage("b-record", "rs-cu-b", time.Second, `{"type":"token_usage_record","payload":{"usage":{"input_tokens":1}}}`),
		usage("b-mirror", "rs-cu-b", 2*time.Second, `{"type":"event_msg","payload":{"type":"token_count","info":{}}}`),
	)
	rowsOf := func(args ...any) []string {
		t.Helper()
		rows, err := s.pool.Query(context.Background(),
			`SELECT id FROM (`+codexUsageRowsSQL("$1::text[]", "$2::bigint")+`) u`, args...)
		if err != nil {
			t.Fatalf("codexUsageRowsSQL: %v", err)
		}
		defer rows.Close()
		var tags []string
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan: %v", err)
			}
			tags = append(tags, f.tags[id])
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("rows: %v", err)
		}
		slices.Sort(tags)
		return tags
	}
	for _, c := range []struct {
		name string
		args []any
		want []string
	}{
		{"no bound", []any{nil, nil}, []string{"a-1", "a-2", "a-3", "b-1", "b-record"}},
		{"one session", []any{[]string{"rs-cu-a"}, nil}, []string{"a-1", "a-2", "a-3"}},
		{"id ceiling", []any{nil, f.id("a-2")}, []string{"a-1", "a-2"}},
	} {
		if got := rowsOf(c.args...); !slices.Equal(got, c.want) {
			t.Errorf("%s: rows %v, want %v", c.name, got, c.want)
		}
	}
}

// The fragments carry the columns asked for, req_key and the four token columns,
// nothing else -- no raw, which a SELECT * would carry through every read path.
func TestRequestSet_CarriesOnlyTheColumnsAskedFor(t *testing.T) {
	s := acquireTestStore(t)
	cols := []string{"session_id", "model"}
	for _, c := range []struct {
		name, sql string
		want      []string
	}{
		{"heads", claudeRequestHeadsSQL("session_records", "TRUE", cols),
			[]string{"session_id", "model", "req_key",
				"input_tokens", "output_tokens", "cache_read_tokens", "cache_create_tokens"}},
		{"attributed rows", attributedRowsSQL("session_records", "TRUE", cols),
			[]string{"session_id", "model", "req_key",
				"req_input_tokens", "req_output_tokens", "req_cache_read_tokens", "req_cache_create_tokens"}},
	} {
		rows, err := s.pool.Query(context.Background(), `SELECT * FROM (`+c.sql+`) x LIMIT 0`)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var got []string
		for _, fd := range rows.FieldDescriptions() {
			got = append(got, fd.Name)
		}
		rows.Close()
		if !slices.Equal(got, c.want) {
			t.Errorf("%s columns = %v, want %v", c.name, got, c.want)
		}
	}
}

// The head is a request's earliest line, not the first one stored. Lines need not
// arrive in ts order: here the line stored second, with the larger id, carries the
// earlier ts. The head's ts is then the request's earliest.
func TestRequestSet_HeadIsTheEarliestLineNotTheFirstStored(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	f := insertRequestSet(t, s,
		rsLine{tag: "stored-first", session: "rs-order", uuid: "stored-first", at: 2 * time.Second,
			msgID: "msg_order", model: "model-a", in: ptrInt(9), out: ptrInt(1)},
		rsLine{tag: "stored-second", session: "rs-order", uuid: "stored-second", at: time.Second,
			msgID: "msg_order", model: "model-b", in: ptrInt(9), out: ptrInt(523)},
	)
	if f.id("stored-second") <= f.id("stored-first") {
		t.Fatalf("ids do not follow the insert order: %d then %d", f.id("stored-first"), f.id("stored-second"))
	}
	heads, _ := f.check(t, s, "session_records", "TRUE", rsAll, nil)

	if _, ok := heads[f.id("stored-second")]; !ok || len(heads) != 1 {
		t.Errorf("heads = %v, want only stored-second, the earlier line", heads)
	}
	earliest := f.lines[0].at
	for _, l := range f.lines {
		earliest = min(earliest, l.at)
	}
	want := earliest.String() + "|stored-second|||model-b"
	if got := f.headIdentity(t, s)[f.id("stored-second")]; got != want {
		t.Errorf("head identity = %q, want %q -- the request's earliest ts, and that line's model", got, want)
	}
}

// A request whose lines straddle a period boundary counts in both periods
// (p1t-design §2.4): each period folds the lines it holds, so each has that request
// once, and the two periods together exceed the whole range by exactly the part
// both of them count -- computed here from the fixture.
func TestRequestSet_RequestAcrossAPeriodBoundaryCountsInBoth(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	f := insertRequestSet(t, s,
		rsLine{tag: "early", session: "rs-period", uuid: "early", at: 10 * time.Minute, msgID: "msg_early",
			in: ptrInt(3), out: ptrInt(4)},
		rsLine{tag: "split-1", session: "rs-period", uuid: "split-1", at: 59 * time.Minute, msgID: "msg_split",
			in: ptrInt(10), out: ptrInt(1), cr: ptrInt(100), cw: ptrInt(5)},
		rsLine{tag: "split-2", session: "rs-period", uuid: "split-2", at: 61 * time.Minute, msgID: "msg_split",
			in: ptrInt(10), out: ptrInt(523), cr: ptrInt(100), cw: ptrInt(5)},
		rsLine{tag: "late", session: "rs-period", uuid: "late", at: 90 * time.Minute, msgID: "msg_late",
			in: ptrInt(7), out: ptrInt(8)},
	)
	sum := func(heads map[int64]rsTokens) (v [4]int64) {
		for _, tk := range heads {
			for i, p := range tk {
				if p != nil {
					v[i] += *p
				}
			}
		}
		return v
	}
	type period struct{ from, to time.Duration }
	whole, first, second := period{0, 2 * time.Hour}, period{0, time.Hour}, period{time.Hour, 2 * time.Hour}
	got, want := map[period][4]int64{}, map[period][4]int64{}
	for _, p := range []period{whole, first, second} {
		keep := func(l rsLine) bool { return l.at >= p.from && l.at < p.to }
		heads, _ := f.check(t, s, "session_records", "sr.ts >= $1 AND sr.ts < $2", keep, nil,
			rsBase.Add(p.from), rsBase.Add(p.to))
		wantHeads, _ := f.expect(keep, nil)
		got[p], want[p] = sum(heads), sum(wantHeads)
		if split := map[period]string{first: "split-1", second: "split-2"}[p]; split != "" {
			if _, ok := heads[f.id(split)]; !ok {
				t.Errorf("period %v has no head for the split request: %v", p, heads)
			}
		}
	}
	var gotTwice, wantTwice [4]int64
	for i := range wantTwice {
		gotTwice[i] = got[first][i] + got[second][i] - got[whole][i]
		wantTwice[i] = want[first][i] + want[second][i] - want[whole][i]
	}
	if wantTwice == [4]int64{} {
		t.Fatal("the two periods count nothing twice -- the fixture splits no request")
	}
	if gotTwice != wantTwice {
		t.Errorf("first + second - whole = %v, want %v: the split request's part both periods count", gotTwice, wantTwice)
	}
}

// rsScopeFixture is Claude, gjc, omo and Codex sessions of one user, and Claude and
// Codex sessions of another. The map is the codex_imputed_cost it produces.
func rsScopeFixture(t *testing.T, s *PgStore) (*rsFixture, map[string]rsTokens) {
	t.Helper()
	f := insertRequestSet(t, s,
		rsLine{tag: "scope-1", session: "rs-scope-claude", uuid: "scope-1", msgID: "msg_scope",
			in: ptrInt(10), out: ptrInt(1)},
		rsLine{tag: "scope-2", session: "rs-scope-claude", uuid: "scope-2", at: time.Second, msgID: "msg_scope",
			in: ptrInt(10), out: ptrInt(50)},
		rsLine{tag: "synthetic", session: "rs-scope-claude", uuid: "synthetic", at: 2 * time.Second, model: "<synthetic>",
			in: ptrInt(1), out: ptrInt(1)},
		rsLine{tag: "gjc", session: "rs-scope-gjc", uuid: "gjc", agent: "gjc", msgID: "msg_gjc",
			in: ptrInt(11), out: ptrInt(12)},
		rsLine{tag: "omo", session: "rs-scope-omo", uuid: "omo", agent: "omo"},
		rsLine{tag: "codex", session: "rs-scope-codex", uuid: "codex", agent: "codex", recType: "usage",
			model: "codex-model", in: ptrInt(100), out: ptrInt(10), cr: ptrInt(40)},
		rsLine{tag: "other-claude", session: "rs-other-claude", uuid: "other-claude", user: "uid-other",
			msgID: "msg_other", in: ptrInt(20), out: ptrInt(2)},
		rsLine{tag: "other-codex", session: "rs-other-codex", uuid: "other-codex", user: "uid-other",
			agent: "codex", recType: "usage", model: "codex-model", in: ptrInt(200), out: ptrInt(20), cr: ptrInt(0)},
	)
	refreshCodexImputed(t, s)
	return f, map[string]rsTokens{"codex": rsTok(60, 10, 40, 0), "other-codex": rsTok(200, 20, 0, 0)}
}

func rsMine(l rsLine) bool { return l.userID() == "uid-rs" }

// lineWhere sees only the source row, in both arms: an unqualified column is the
// source's own. codex_imputed_cost, which the pass-through arm joins, has a user_id
// too, and an arm that filtered after that join found user_id ambiguous -- the
// Weekly scope writes it unqualified (postgres_weekly_insights.go).
func TestRequestSet_LineWhereUnqualifiedColumnIsTheSources(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	f, codex := rsScopeFixture(t, s)
	f.check(t, s, "session_records", "user_id = $1", rsMine, codex, "uid-rs")
}

// An outer alias in lineWhere is the caller's, should a caller correlate through it
// (the fragment docs advise against that for speed; the plugin raw fallback names
// its command rows c, p1t-design §6.3). An arm that filtered after its Codex join
// bound c to codex_imputed_cost instead: the WHERE
// turned the LEFT JOIN inner, dropped gjc and omo rows, and let in the Codex rows of
// every session.
func TestRequestSet_LineWhereOuterAliasIsTheCallers(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	f, codex := rsScopeFixture(t, s)
	sessions := []string{"rs-scope-claude", "rs-scope-gjc", "rs-scope-omo", "rs-scope-codex"}
	sessionOf := map[int64]string{}
	for _, l := range f.lines {
		sessionOf[f.id(l.tag)] = l.session
	}
	// Each row has to come back once, under its own session.
	correlated := func(what, shape, tokens string) map[int64]rsTokens {
		t.Helper()
		rows, err := s.pool.Query(context.Background(), `SELECT c.session_id, x.id, `+tokens+
			` FROM unnest($1::text[]) AS c(session_id) CROSS JOIN LATERAL (`+shape+`) x`, sessions)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		defer rows.Close()
		out := map[int64]rsTokens{}
		for rows.Next() {
			var outer string
			var id int64
			var v rsTokens
			if err := rows.Scan(&outer, &id, &v[0], &v[1], &v[2], &v[3]); err != nil {
				t.Fatalf("%s: scan: %v", what, err)
			}
			if sessionOf[id] != outer {
				t.Errorf("%s: row %s of %s came back under %s", what, f.tags[id], sessionOf[id], outer)
			}
			if _, dup := out[id]; dup {
				t.Errorf("%s: row %s came back more than once", what, f.tags[id])
			}
			out[id] = v
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		return out
	}
	wantHeads, wantAttributed := f.expect(rsMine, codex)
	f.compare(t, "heads correlated with c", correlated("heads",
		claudeRequestHeadsSQL("session_records", "sr.session_id = c.session_id", []string{"id"}),
		"x.input_tokens, x.output_tokens, x.cache_read_tokens, x.cache_create_tokens"), wantHeads)
	f.compare(t, "attributed rows correlated with c", correlated("attributed rows",
		attributedRowsSQL("session_records", "sr.session_id = c.session_id", []string{"id"}),
		"x.req_input_tokens, x.req_output_tokens, x.req_cache_read_tokens, x.req_cache_create_tokens"), wantAttributed)
}

// A row the request-line predicate answers NULL for passes through. Through this
// source gjc's agent reads NULL; NOT would have dropped the row from both arms.
func TestRequestSet_RowWithANullPredicatePassesThrough(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	f := insertRequestSet(t, s,
		rsLine{tag: "claude", session: "rs-null", uuid: "claude", msgID: "msg_null", in: ptrInt(5), out: ptrInt(6)},
		rsLine{tag: "gjc", session: "rs-null-gjc", uuid: "gjc", agent: "gjc", msgID: "msg_gjc",
			in: ptrInt(11), out: ptrInt(12)},
	)
	source := `(SELECT id, session_id, ts, uuid, message_id, raw, model, record_type, NULLIF(agent, 'gjc') AS agent,
		input_tokens, output_tokens, cache_read_tokens, cache_create_tokens FROM session_records)`
	_, attributed := f.check(t, s, source, "TRUE", rsAll, nil)
	if got, want := attributed[f.id("gjc")], (rsTokens{ptrI64(11), ptrI64(12), nil, nil}); !rsEqual(got, want) {
		t.Errorf("gjc with a NULL agent = %s, want %s", got, want)
	}
}

// Both shapes carry the request key as req_key -- P1t-5's plugin raw fallback joins
// on it (p1t-design §6.3): message_id; the id in raw where message_id is not filled
// yet; the row key where there is none. Attributed rows carry it on every line of a
// request, head or not, and NULL on every other row.
func TestRequestSet_RequestKeyIsCarried(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	f := insertRequestSet(t, s,
		rsLine{tag: "filled-1", session: "rs-key", uuid: "filled-1", msgID: "msg_filled", in: ptrInt(1)},
		rsLine{tag: "filled-2", session: "rs-key", uuid: "filled-2", at: time.Second, msgID: "msg_filled", in: ptrInt(1)},
		rsLine{tag: "unfilled", session: "rs-key", uuid: "unfilled", at: 2 * time.Second, msgID: "msg_unfilled",
			in: ptrInt(2)},
		rsLine{tag: "noid", session: "rs-key", uuid: "noid", at: 3 * time.Second, in: ptrInt(3)},
		rsLine{tag: "synthetic", session: "rs-key", uuid: "synthetic", at: 4 * time.Second, msgID: "msg_filled",
			model: "<synthetic>"},
		rsLine{tag: "user", session: "rs-key", uuid: "user", at: 5 * time.Second, recType: "user"},
		rsLine{tag: "gjc", session: "rs-key-gjc", uuid: "gjc", agent: "gjc", msgID: "msg_gjc", in: ptrInt(4)},
	)
	if _, err := s.pool.Exec(ctx, `UPDATE session_records SET message_id = NULL WHERE id = $1`, f.id("unfilled")); err != nil {
		t.Fatalf("unfill: %v", err)
	}
	// From the fixture: a request line's message id, or its row key when it has none.
	keyOf := func(l rsLine) *string {
		k := l.msgID
		if k == "" {
			k = fmt.Sprintf("row:%d", f.id(l.tag))
		}
		return &k
	}
	wantHeadTokens, _ := f.expect(rsAll, nil)
	wantHeads, wantRows := map[int64]*string{}, map[int64]*string{}
	for _, l := range f.lines {
		wantRows[f.id(l.tag)] = nil
		if l.isRequestLine() {
			wantRows[f.id(l.tag)] = keyOf(l)
			if _, head := wantHeadTokens[f.id(l.tag)]; head {
				wantHeads[f.id(l.tag)] = keyOf(l)
			}
		}
	}
	for _, c := range []struct {
		name, sql string
		want      map[int64]*string
	}{
		{"heads", claudeRequestHeadsSQL("session_records", "TRUE", []string{"id"}), wantHeads},
		{"attributed rows", attributedRowsSQL("session_records", "TRUE", []string{"id"}), wantRows},
	} {
		rows, err := s.pool.Query(ctx, `SELECT id, req_key FROM (`+c.sql+`) x`)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := map[int64]*string{}
		for rows.Next() {
			var id int64
			var key *string
			if err := rows.Scan(&id, &key); err != nil {
				t.Fatalf("%s: scan: %v", c.name, err)
			}
			got[id] = key
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		str := func(p *string) string {
			if p == nil {
				return "NULL"
			}
			return *p
		}
		if len(got) != len(c.want) {
			t.Errorf("%s: %d rows, want %d", c.name, len(got), len(c.want))
		}
		for id, w := range c.want {
			if g, ok := got[id]; !ok || str(g) != str(w) {
				t.Errorf("%s: req_key of %s = %s, want %s", c.name, f.tags[id], str(g), str(w))
			}
		}
	}
}

// syncAttributedRowsSQL is the rule expect states, for facts written at sync
// time: a Codex usage row reads its own counts behind the whole-session defences,
// input net of cache reads, so it holds with codex_imputed_cost empty -- as it is
// for a session the imputed refresh has not reached yet. The mirrored token_count
// and the repeated snapshot are 0 on their own rows.
func TestSyncAttributedRows_FollowTheRuleWithoutTheImputedTable(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	snapshot := `{"payload":{"info":{"total_token_usage":{"input_tokens":10,"cached_input_tokens":1,"output_tokens":2}}}}`
	lines, codex := rsMixed()
	lines = append(lines,
		rsLine{tag: "cx-snapshot", session: "mx-cx-repeat", uuid: "cx-snapshot", agent: "codex", recType: "usage",
			model: "codex-model", raw: snapshot, in: ptrInt(800), out: ptrInt(80), cr: ptrInt(300)},
		rsLine{tag: "cx-repeat", session: "mx-cx-repeat", uuid: "cx-repeat", at: time.Second, agent: "codex",
			recType: "usage", model: "codex-model", raw: snapshot, in: ptrInt(800), out: ptrInt(80), cr: ptrInt(300)},
	)
	codex["cx-snapshot"] = rsTok(500, 80, 300, 0)
	f := insertRequestSet(t, s, lines...)
	var imputed int
	if err := s.pool.QueryRow(context.Background(), `SELECT count(*) FROM codex_imputed_cost`).Scan(&imputed); err != nil {
		t.Fatalf("count codex_imputed_cost: %v", err)
	}
	if imputed != 0 {
		t.Fatalf("codex_imputed_cost holds %d row(s); the test needs it empty", imputed)
	}

	_, want := f.expect(rsAll, codex)
	f.compare(t, "sync attributed rows", f.rows(t, s, `SELECT id, req_input_tokens, req_output_tokens,
		req_cache_read_tokens, req_cache_create_tokens
		FROM (`+syncAttributedRowsSQL("$1::text[]", []string{"id"})+`) a`, nil), want)
}

// The filter names whole sessions, and a deleted session is left out even when
// named. A caller that cuts a period from the result keeps the defences of the
// whole session: the mirror after the cut stays 0 though the record it mirrors
// is before it. The fragment carries cols, req_key and the four token columns,
// nothing else.
func TestSyncAttributedRows_ReadWholeSessionsOfTheFilter(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	f := insertRequestSet(t, s,
		rsLine{tag: "kept-1", session: "rs-sync-kept", uuid: "kept-1", msgID: "msg_kept", in: ptrInt(10), out: ptrInt(1)},
		rsLine{tag: "kept-2", session: "rs-sync-kept", uuid: "kept-2", at: time.Second, msgID: "msg_kept",
			in: ptrInt(10), out: ptrInt(523)},
		rsLine{tag: "other", session: "rs-sync-other", uuid: "other", msgID: "msg_other", in: ptrInt(20), out: ptrInt(2)},
		rsLine{tag: "deleted", session: "rs-sync-deleted", uuid: "deleted", msgID: "msg_deleted", in: ptrInt(30), out: ptrInt(3)},
		rsLine{tag: "cx-record", session: "rs-sync-cx", uuid: "cx-record", agent: "codex", recType: "usage",
			model: "codex-model", raw: `{"type":"token_usage_record","payload":{"usage":{"input_tokens":1}}}`,
			in: ptrInt(1000), out: ptrInt(100), cr: ptrInt(0)},
		rsLine{tag: "cx-mirror", session: "rs-sync-cx", uuid: "cx-mirror", at: time.Hour, agent: "codex", recType: "usage",
			model: "codex-model", raw: `{"type":"event_msg","payload":{"type":"token_count","info":{}}}`,
			in: ptrInt(1000), out: ptrInt(100), cr: ptrInt(0)},
	)
	if _, err := s.DeleteSession(ctx, "rs-sync-deleted", "admin@example.com", "", false, false); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	codex := map[string]rsTokens{"cx-record": rsTok(1000, 100, 0, 0)}
	named := []string{"rs-sync-kept", "rs-sync-deleted", "rs-sync-cx"}
	inNamed := func(l rsLine) bool { return slices.Contains(named, l.session) && l.session != "rs-sync-deleted" }
	read := `SELECT id, req_input_tokens, req_output_tokens, req_cache_read_tokens, req_cache_create_tokens
		FROM (` + syncAttributedRowsSQL("$1::text[]", []string{"id", "ts"}) + `) a`

	_, want := f.expect(inNamed, codex)
	f.compare(t, "the named sessions", f.rows(t, s, read, named), want)
	_, want = f.expect(func(l rsLine) bool { return inNamed(l) && l.at >= time.Minute }, codex)
	f.compare(t, "the named sessions from a minute in", f.rows(t, s, read+` WHERE a.ts >= $2`, named,
		rsBase.Add(time.Minute)), want)

	rows, err := s.pool.Query(ctx, `SELECT * FROM (`+syncAttributedRowsSQL("NULL::text[]", []string{"session_id", "model"})+`) x LIMIT 0`)
	if err != nil {
		t.Fatalf("read the columns: %v", err)
	}
	var columns []string
	for _, fd := range rows.FieldDescriptions() {
		columns = append(columns, fd.Name)
	}
	rows.Close()
	if want := []string{"session_id", "model", "req_key", "req_input_tokens", "req_output_tokens",
		"req_cache_read_tokens", "req_cache_create_tokens"}; !slices.Equal(columns, want) {
		t.Errorf("columns = %v, want %v", columns, want)
	}
}
