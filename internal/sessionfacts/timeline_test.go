package sessionfacts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

// placedRows renders each timeline row as "uuid turn [roles]" or "uuid -reason",
// keyed by uuid; a uuid stored twice lists both rows.
func placedRows(facts TimelineFacts) map[string][]string {
	out := make(map[string][]string)
	for _, r := range facts.Rows {
		s := "-" + r.Unmapped
		if r.Unmapped == "" {
			s = r.TurnKey + " [" + strings.Join(r.Roles, " ") + "]"
		}
		out[r.UUID] = append(out[r.UUID], s)
	}
	return out
}

func expectRows(t *testing.T, facts TimelineFacts, want map[string]string) {
	t.Helper()
	got := placedRows(facts)
	for uuid, w := range want {
		rows := got[uuid]
		if len(rows) == 0 {
			t.Errorf("%s: no row", uuid)
			continue
		}
		for _, r := range rows {
			if r != w {
				t.Errorf("%s: got %q, want %q", uuid, r, w)
			}
		}
	}
	if len(facts.Rows) != countRows(want, got) {
		t.Errorf("rows %v not all checked: %v", facts.Rows, want)
	}
}

func countRows(want map[string]string, got map[string][]string) int {
	n := 0
	for uuid := range want {
		n += len(got[uuid])
	}
	return n
}

func TestTimelineRequiresExplicitOptIn(t *testing.T) {
	for _, tc := range allCases(t) {
		in := tc.in.in
		legacy := Compute(in)
		extended := ComputeWithTimeline(in)
		stripped := extended.Output
		stripped.Hash = legacy.Hash
		if !reflect.DeepEqual(stripped, legacy) {
			t.Fatalf("%s: timeline path changed the r2 output", tc.name)
		}
		if !bytes.Equal(legacy.Hash, HashOutput(legacy)) || bytes.Equal(legacy.Hash, extended.Hash) {
			t.Fatalf("%s: hashes not separated", tc.name)
		}
		if !bytes.Equal(extended.Hash, HashOutputWithTimeline(extended)) {
			t.Fatalf("%s: extended hash differs from exported hash", tc.name)
		}
		wire, err := json.Marshal(legacy)
		if err != nil || bytes.Contains(wire, []byte(`"Timeline"`)) {
			t.Fatalf("%s: legacy JSON includes the timeline", tc.name)
		}
		checkTimelineShape(t, tc.name, in, extended)
	}
}

// Every read row has exactly one result, in storage key order, and a placed row
// names a turn the output has.
func checkTimelineShape(t *testing.T, name string, in Input, out TimelineOutput) {
	t.Helper()
	f := out.Timeline
	read := len(in.Rows)
	if in.Agent == "codex" {
		read = len(in.CodexRows)
		for _, r := range in.CodexRows {
			if r.UUID == "" {
				if f.State != "not_collected" || f.Reason != "codex_uuid_not_loaded" || len(f.Rows) != 0 {
					t.Fatalf("%s: codex timeline without uuids %+v", name, f)
				}
				return
			}
		}
	}
	if len(f.Rows) != read {
		t.Fatalf("%s: %d timeline rows for %d input rows", name, len(f.Rows), read)
	}
	keys := make(map[string]bool)
	for _, turn := range out.Turns {
		keys[turn.TurnKey] = true
	}
	for i, r := range f.Rows {
		if (r.TurnKey == "") == (r.Unmapped == "") || (r.TurnKey == "" && len(r.Roles) != 0) {
			t.Fatalf("%s: row %+v has no single result", name, r)
		}
		if r.TurnKey != "" && !keys[r.TurnKey] {
			t.Fatalf("%s: row %+v names no turn", name, r)
		}
		if f.State != "observed" && r.Unmapped != "turn_boundaries_"+f.Reason {
			t.Fatalf("%s: undeterminable row %+v", name, r)
		}
		if i > 0 {
			a := f.Rows[i-1]
			less := a.TS.Before(r.TS) || a.TS.Equal(r.TS) && (a.UUID < r.UUID || a.UUID == r.UUID &&
				(a.ID < r.ID || a.ID == r.ID && (a.RecordType < r.RecordType || a.RecordType == r.RecordType && a.ProfileEmail <= r.ProfileEmail)))
			if !less {
				t.Fatalf("%s: rows out of key order: %+v then %+v", name, a, r)
			}
		}
	}
}

func TestTimelineDoesNotDependOnInputOrderOrChangeIt(t *testing.T) {
	rng := rand.New(rand.NewSource(20261008))
	for _, tc := range allCases(t) {
		in := tc.in.in
		want := ComputeWithTimeline(in)
		for round := 0; round < 10; round++ {
			mixed := in
			mixed.Rows, mixed.CodexRows = shuffled(rng, in.Rows), shuffled(rng, in.CodexRows)
			mixed.Heads, mixed.OTEL = shuffled(rng, in.Heads), shuffled(rng, in.OTEL)
			mixed.Snapshots, mixed.CopiedIDs = shuffled(rng, in.Snapshots), shuffled(rng, in.CopiedIDs)
			before := mixed
			before.Rows, before.CopiedIDs = append([]ClaudeRow(nil), mixed.Rows...), append([]int64(nil), mixed.CopiedIDs...)
			if got := ComputeWithTimeline(mixed); !reflect.DeepEqual(got, want) {
				t.Fatalf("%s: shuffled input (round %d) changed the timeline\n got  %+v\n want %+v", tc.name, round, got.Timeline, want.Timeline)
			}
			if !reflect.DeepEqual(mixed, before) {
				t.Fatalf("%s: ComputeWithTimeline changed its input", tc.name)
			}
		}
	}
}

func TestTimelineNilAndEmptyInput(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		none := ComputeWithTimeline(Input{Agent: agent})
		empty := ComputeWithTimeline(Input{Agent: agent, Rows: []ClaudeRow{}, CodexRows: []CodexRow{}, Heads: []Head{}, CopiedIDs: []int64{}})
		if !reflect.DeepEqual(none, empty) || none.Timeline.Rows != nil {
			t.Fatalf("%s: nil and empty differ: %+v %+v", agent, none.Timeline, empty.Timeline)
		}
	}
	if got := ComputeWithTimeline(Input{Agent: "claude"}).Timeline; got.State != "observed" || got.Reason != "" {
		t.Fatalf("empty claude timeline %+v", got)
	}
}

func TestTimelineHashCoversEveryTimelineValue(t *testing.T) {
	out := ComputeWithTimeline(timelineRolesFixture())
	legacy := HashOutput(out.Output)
	base := HashOutputWithTimeline(out)
	var changed int
	eachLeaf(reflect.ValueOf(&out).Elem().FieldByName("Timeline"), "Timeline", false, func(path string, excluded bool) {
		if bytes.Equal(HashOutputWithTimeline(out), base) {
			t.Errorf("%s does not change the timeline hash", path)
		}
		if !bytes.Equal(HashOutput(out.Output), legacy) {
			t.Errorf("%s changes the r2 hash", path)
		}
		changed++
	})
	if changed < 10 || !bytes.Equal(HashOutputWithTimeline(out), base) {
		t.Fatalf("walk changed %d values or did not restore the output", changed)
	}
}

func timelineRolesFixture() Input {
	return session(
		typed("u1", "", 1, "p1", "do it"),
		call("a1", "u1", 2, "q1", "AskUserQuestion"),
		result("r1", "a1", 3, "q1", prompt("p1")),
		call("a2", "r1", 4, "pl1", "ExitPlanMode"),
		attachment("f1", "a2", 5, `{"type":"plan_file_reference","planFilePath":"/p.md"}`),
		result("r2", "f1", 6, "pl1", prompt("p1")),
		attachment("qa", "r2", 7, `{"type":"queued_command","commandMode":"prompt","prompt":"more","origin":{"kind":"human"}}`),
		reply("a3", "qa", 8),
		newRow("user", "s1", "", 3, sidechain("ag"), prompt("p1"), textBlock("task")),
		reply("s2", "s1", 4, sidechain("ag")),
	).in
}

func TestTimelineRoles(t *testing.T) {
	out := ComputeWithTimeline(timelineRolesFixture())
	if out.Timeline.State != "observed" {
		t.Fatalf("state %+v", out.Timeline)
	}
	expectRows(t, out.Timeline, map[string]string{
		"u1": "u1 [opener input]",
		"a1": "u1 [question]",
		"r1": "u1 [answer question_result]",
		"a2": "u1 [plan]",
		"f1": "u1 [plan_attachment]",
		"r2": "u1 [plan_result]",
		"qa": "u1 [queued]",
		"a3": "u1 [assistant_final]",
		"s1": "u1 [subagent]",
		"s2": "u1 [subagent]",
	})
}

// A turn's key is its first line, which need not be the line that opened it.
func TestTimelineTurnKeyIsNotTheOpener(t *testing.T) {
	out := ComputeWithTimeline(session(typed("u1", "", 10, "p1", "hi"), reply("a1", "u1", 5)).in)
	if len(out.Turns) != 1 || out.Turns[0].TurnKey != "a1" || out.Turns[0].OpenerUUID != "u1" {
		t.Fatalf("fixture turn %+v", out.Turns)
	}
	expectRows(t, out.Timeline, map[string]string{"u1": "a1 [opener input]", "a1": "a1 [assistant_final]"})
}

// The latest turn opened before a line is a time join. Only the line's own tool
// result can place it, and only in the turn r2 gave it.
func TestTimelineTimeFallbackIsNotExact(t *testing.T) {
	out := ComputeWithTimeline(session(
		reply("z0", "gone0", 0),
		typed("u1", "", 1, "p1", "first"),
		call("a1", "u1", 2, "t1", "Bash"),
		typed("u2", "", 10, "p2", "second"),
		reply("x1", "gone1", 11),
		result("x2", "gone2", 12, "t1"),
		call("a3", "u2", 13, "t3", "Bash"),
		result("x3", "gone3", 14, "t3"),
	).in)
	// r2 still counts the fallback lines in u2.
	if turnsBy(out.Output, "key")[1] != "u2" {
		t.Fatalf("fixture turns %v", turnsBy(out.Output, "key"))
	}
	expectRows(t, out.Timeline, map[string]string{
		"z0": "-no_turn",
		"u1": "u1 [opener input]",
		"a1": "u1 [assistant_final]",
		"u2": "u2 [opener input]",
		"x1": "-ts_fallback",
		"x2": "-conflicting_evidence",
		"a3": "u2 [assistant_final]",
		"x3": "u2 []",
	})
}

// Rows of one uuid are a choice r2 makes; neither row nor what follows only
// through them is exact. A descendant's own sole prompt id is independent.
func TestTimelineDuplicateUUIDAndItsLineage(t *testing.T) {
	out := ComputeWithTimeline(session(
		typed("u1", "", 1, "p1", "q"),
		reply("a1", "u1", 2),
		reply("a1", "u1", 2, func(r *ClaudeRow) { r.ProfileEmail = "other@example.com" }),
		reply("c1", "a1", 3),
		reply("c2", "a1", 4, prompt("p1")),
	).in)
	expectRows(t, out.Timeline, map[string]string{
		"u1": "u1 [opener input]",
		"a1": "-ambiguous_uuid",
		"c1": "-ambiguous_lineage",
		"c2": "u1 [assistant_final]",
	})
}

func TestTimelineThreadsNeedOneAnchor(t *testing.T) {
	agent := func(id string) opt {
		return func(r *ClaudeRow) { r.ToolUseResult = json.RawMessage(`{"agentId":"` + id + `"}`) }
	}
	toolUse := func(id string) opt { return func(r *ClaudeRow) { r.ToolUseID = id } }
	out := ComputeWithTimeline(session(
		typed("u1", "", 1, "p1", "x"),
		call("a1", "u1", 2, "dup", "Agent"),
		result("rx", "a1", 2, "zz", prompt("p1"), agent("ag-x")),
		typed("u2", "a1", 3, "p2", "y"),
		call("a2", "u2", 4, "dup", "Agent"),
		result("r2", "a2", 5, "dup", prompt("p2"), agent("ag-dup")),
		result("r1", "r2", 6, "other", prompt("p2"), agent("ag-dup")),
		result("r3", "r1", 7, "more", prompt("p2"), agent("ag-one")),
		newRow("user", "s1", "", 3, sidechain("ag-t"), toolUse("dup"), textBlock("t")),
		newRow("user", "s2", "", 3, sidechain("ag-dup"), textBlock("t")),
		newRow("user", "s3", "", 3, sidechain("ag-one"), textBlock("t")),
		newRow("user", "s4", "", 3, sidechain("ag-none"), textBlock("t")),
		newRow("user", "s5", "", 3, func(r *ClaudeRow) { r.IsSidechain = true }, textBlock("t")),
		// r2 takes the last node with the repeated call (u2); the sole agent
		// id names u1, so no exact anchor agrees with r2.
		newRow("user", "s6", "", 3, sidechain("ag-x"), toolUse("dup"), textBlock("t")),
	).in)
	expectRows(t, out.Timeline, map[string]string{
		"u1": "u1 [opener input]",
		"a1": "u1 [assistant_final]",
		"rx": "u1 []",
		"u2": "u2 [opener input]",
		"a2": "u2 [assistant_final]",
		"r2": "u2 []",
		"r1": "u2 []",
		"r3": "u2 []",
		"s1": "-ambiguous_tool_use",
		"s2": "-ambiguous_agent_id",
		"s3": "u2 [subagent]",
		"s4": "-thread_unattributed",
		"s5": "-sidechain_unkeyed",
		"s6": "-conflicting_evidence",
	})
}

// A question answer whose tool_use id two nodes carry is no exact answer.
func TestTimelineDuplicateQuestionIDKeepsNoResultRole(t *testing.T) {
	out := ComputeWithTimeline(session(
		typed("u1", "", 1, "p1", "x"),
		call("a1", "u1", 2, "q", "AskUserQuestion"),
		result("r1", "a1", 3, "q", prompt("p1")),
		call("a2", "r1", 4, "q", "AskUserQuestion"),
		result("r2", "a2", 5, "q", prompt("p1")),
	).in)
	expectRows(t, out.Timeline, map[string]string{
		"u1": "u1 [opener input]",
		"a1": "u1 [question]",
		"r1": "u1 []",
		"a2": "u1 [assistant_final question]",
		"r2": "u1 []",
	})
}

// A copy mark names an id, which two rows can share.
func TestTimelineCopyIDsAndCopiedTurns(t *testing.T) {
	f := session(
		typed("u1", "", 1, "p1", "x"), reply("a1", "u1", 2),
		typed("u2", "", 10, "p2", "y"), reply("a2", "u2", 11),
		typed("u3", "", 20, "p3", "z"), reply("a3", "u3", 21),
	)
	f.in.Rows[3].ID = f.in.Rows[1].ID // a2 shares a1's id
	f.copies("u2", "a1", "u3", "a3")
	out := ComputeWithTimeline(f.in)
	expectRows(t, out.Timeline, map[string]string{
		"u1": "u1 [opener input]",
		"a1": "-ambiguous_copy_id",
		"u2": "-ambiguous_copy_id",
		"a2": "-ambiguous_copy_id",
		"u3": "-copied_turn",
		"a3": "-copied_turn",
	})
}

func TestTimelineRowsNoTurnCanName(t *testing.T) {
	out := ComputeWithTimeline(session(
		typed("u1", "", 1, "p1", "x"),
		reply("a1", "u1", 2),
		reply("a1", "u1", 2),
		system("d1", "u1", 3, "x", func(r *ClaudeRow) { r.TS = time.Unix(0, 0) }),
		system("", "u1", 4, "x"),
		newRow("user", "content-abc", "", 5),
		newRow("summary", "m1", "", 6),
	).in)
	expectRows(t, out.Timeline, map[string]string{
		"u1":          "u1 [opener input]",
		"a1":          "-duplicate_source_tuple",
		"d1":          "-undated",
		"":            "-unkeyed",
		"content-abc": "-unkeyed",
		"m1":          "-non_conversation_type",
	})
}

func TestTimelineUndeterminableAndCodex(t *testing.T) {
	out := ComputeWithTimeline(session(typed("u1", "", 1, "p1", "x"), reply("", "u1", 2)).in)
	if out.Timeline.State != "undeterminable" || out.Timeline.Reason != "pre_lineage" {
		t.Fatalf("pre-lineage timeline %+v", out.Timeline)
	}
	expectRows(t, out.Timeline, map[string]string{"u1": "-turn_boundaries_pre_lineage", "": "-turn_boundaries_pre_lineage"})

	codex := ComputeWithTimeline(Input{Agent: "codex", CodexRows: []CodexRow{{ID: 1, TS: at(1), RecordType: "user", TextHead: "x"}}})
	if codex.Timeline.State != "not_collected" || codex.Timeline.Reason != "codex_uuid_not_loaded" || codex.Timeline.Rows != nil {
		t.Fatalf("codex timeline %+v", codex.Timeline)
	}
}

func sourceFile(f string) opt { return func(r *ClaudeRow) { r.SourceFile = f } }

// A result names its call by tool_use id. Another tool's call with that id, a
// second main file with it, or a call no exact link placed leaves the result
// without a role; a subagent file's own call with the id does not.
func TestTimelineResultRolesNeedOneExactCall(t *testing.T) {
	cases := []struct {
		name string
		rows []ClaudeRow
		want map[string]string
	}{
		{"question id shared with Bash", []ClaudeRow{
			typed("u1", "", 1, "p1", "x"),
			call("a1", "u1", 2, "x", "AskUserQuestion"),
			call("b1", "a1", 3, "x", "Bash"),
			result("r1", "b1", 4, "x", prompt("p1")),
		}, map[string]string{"u1": "u1 [opener input]", "a1": "u1 [question]", "b1": "u1 [assistant_final]", "r1": "u1 []"}},
		{"plan id shared with Bash", []ClaudeRow{
			typed("u1", "", 1, "p1", "x"),
			call("a1", "u1", 2, "y", "ExitPlanMode"),
			call("b1", "a1", 3, "y", "Bash"),
			result("r1", "b1", 4, "y", prompt("p1")),
		}, map[string]string{"u1": "u1 [opener input]", "a1": "u1 [plan]", "b1": "u1 [assistant_final]", "r1": "u1 []"}},
		{"subagent file has its own call", []ClaudeRow{
			typed("u1", "", 1, "p1", "x"),
			call("a1", "u1", 2, "z", "AskUserQuestion"),
			result("r1", "a1", 3, "z", prompt("p1")),
			call("s1", "", 2, "z", "Bash", sidechain("ag"), sourceFile("agent.jsonl")),
		}, map[string]string{"u1": "u1 [opener input]", "a1": "u1 [assistant_final question]",
			"r1": "u1 [answer question_result]", "s1": "-thread_unattributed"}},
		{"second main file has the call", []ClaudeRow{
			typed("u1", "", 1, "p1", "x"),
			call("a1", "u1", 2, "w", "AskUserQuestion"),
			result("r1", "a1", 3, "w", prompt("p1")),
			call("a2", "r1", 4, "w", "AskUserQuestion", sourceFile("resumed.jsonl")),
		}, map[string]string{"u1": "u1 [opener input]", "a1": "u1 [question]", "r1": "u1 []", "a2": "u1 [assistant_final question]"}},
		{"a row outside the tree repeats the call", []ClaudeRow{
			typed("u1", "", 1, "p1", "x"),
			call("a1", "u1", 2, "k", "AskUserQuestion"),
			newRow("progress", "g1", "a1", 3, blocks(`[{"t":"tool_use","id":"k","n":"AskUserQuestion"}]`)),
			result("r1", "a1", 4, "k", prompt("p1")),
		}, map[string]string{"u1": "u1 [opener input]", "a1": "u1 [assistant_final question]", "g1": "-non_conversation_type", "r1": "u1 []"}},
		{"call placed only by time", []ClaudeRow{
			typed("u1", "", 1, "p1", "x"),
			call("a1", "gone", 2, "v", "AskUserQuestion"),
			result("r1", "a1", 3, "v", prompt("p1")),
		}, map[string]string{"u1": "u1 [opener input]", "a1": "-ts_fallback", "r1": "u1 []"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expectRows(t, ComputeWithTimeline(session(tc.rows...).in).Timeline, tc.want)
		})
	}
}

// A plan attachment names its plan by parent uuid, as readQuestionsAndPlans
// does: one parent row holding one plan call, and one attachment.
func TestTimelinePlanAttachmentNeedsOneParent(t *testing.T) {
	file := `{"type":"plan_file_reference","planFilePath":"/p.md"}`
	cases := []struct {
		name string
		rows []ClaudeRow
		want map[string]string
	}{
		{"exact", []ClaudeRow{
			typed("u1", "", 1, "p1", "x"),
			call("a1", "u1", 2, "p", "ExitPlanMode"),
			attachment("f1", "a1", 3, file),
		}, map[string]string{"u1": "u1 [opener input]", "a1": "u1 [assistant_final plan]", "f1": "u1 [plan_attachment]"}},
		{"parent stored twice", []ClaudeRow{
			typed("u1", "", 1, "p1", "x"),
			call("a1", "u1", 2, "p", "ExitPlanMode"),
			call("a1", "u1", 2, "p", "ExitPlanMode", func(r *ClaudeRow) { r.ProfileEmail = "other@example.com" }),
			attachment("f1", "a1", 3, file, prompt("p1")),
		}, map[string]string{"u1": "u1 [opener input]", "a1": "-ambiguous_uuid", "f1": "u1 []"}},
		{"parent is another tool", []ClaudeRow{
			typed("u1", "", 1, "p1", "x"),
			call("a1", "u1", 2, "p", "Bash"),
			attachment("f1", "a1", 3, file),
		}, map[string]string{"u1": "u1 [opener input]", "a1": "u1 [assistant_final]", "f1": "u1 []"}},
		{"two attachments", []ClaudeRow{
			typed("u1", "", 1, "p1", "x"),
			call("a1", "u1", 2, "p", "ExitPlanMode"),
			attachment("f1", "a1", 3, file),
			attachment("f2", "a1", 4, file),
		}, map[string]string{"u1": "u1 [opener input]", "a1": "u1 [assistant_final plan]", "f1": "u1 []", "f2": "u1 []"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expectRows(t, ComputeWithTimeline(session(tc.rows...).in).Timeline, tc.want)
		})
	}
}

// Rule 1-b splits prompt P into a command turn and the typed turn after it, and
// r2 gives later P lines the newest. A P line whose parent sits in the command
// turn has two exact links that disagree, so it is not placed; neither is what
// follows only through it. A P line whose parent agrees stays placed.
func TestTimelineSharedPromptParentConflict(t *testing.T) {
	in := session(
		command("c1", "", 1, "plan", prompt("P")),
		reply("ca", "c1", 2),
		typed("u1", "ca", 3, "P", "follow up"),
		reply("a1", "u1", 4),
		typed("u2", "c1", 5, "P", "another"),
		reply("a2", "u2", 6),
		typed("u3", "a1", 7, "P", "agrees"),
	).in
	out := ComputeWithTimeline(in)
	if got := turnsBy(out.Output, "key"); len(got) != 2 || got[0] != "c1" || got[1] != "u1" {
		t.Fatalf("fixture turns %v", got)
	}
	_, c := compute(in, false)
	for _, l := range c.lines {
		if l.UUID == "u2" && (l.turn == nil || l.turn.out.TurnKey != "u1" || l.parent.turn.out.TurnKey != "c1") {
			t.Fatal("r2 no longer gives u2 the newest P turn under a parent in the command turn")
		}
	}
	legacy := Compute(in)
	stripped := out.Output
	stripped.Hash = legacy.Hash
	if !reflect.DeepEqual(stripped, legacy) || !bytes.Equal(legacy.Hash, HashOutput(legacy)) {
		t.Fatal("timeline path changed the r2 output")
	}
	rng := rand.New(rand.NewSource(5))
	for round := 0; round < 25; round++ {
		mixed := in
		mixed.Rows = shuffled(rng, in.Rows)
		if got := ComputeWithTimeline(mixed); !reflect.DeepEqual(got, out) {
			t.Fatalf("shuffled input (round %d) changed the result", round)
		}
	}
	expectRows(t, out.Timeline, map[string]string{
		"c1": "c1 [opener input]",
		"ca": "c1 [assistant_final]",
		"u1": "u1 [opener input]",
		"a1": "u1 [assistant_final]",
		"u2": "-conflicting_evidence",
		"a2": "-ambiguous_lineage",
		"u3": "u1 []",
	})
}

// withUUIDs gives every Codex row the content digest uuid sync stores, as a
// loader that reads the column would.
func withUUIDs(in Input) Input {
	in.CodexRows = append([]CodexRow(nil), in.CodexRows...)
	for i := range in.CodexRows {
		in.CodexRows[i].UUID = fmt.Sprintf("content-%d", in.CodexRows[i].ID)
	}
	return in
}

// The uuid names Codex rows for the timeline and changes nothing r2 computes.
func TestCodexTimelineKeeps_R2_AndInputOrder(t *testing.T) {
	rng := rand.New(rand.NewSource(72))
	codex := 0
	for _, tc := range allCases(t) {
		if tc.in.in.Agent != "codex" {
			continue
		}
		codex++
		in := withUUIDs(tc.in.in)
		legacy := Compute(tc.in.in)
		out := ComputeWithTimeline(in)
		stripped := out.Output
		stripped.Hash = legacy.Hash
		if !reflect.DeepEqual(stripped, legacy) || !bytes.Equal(Compute(in).Hash, legacy.Hash) {
			t.Fatalf("%s: the uuid changed the r2 output", tc.name)
		}
		if out.Timeline.State != "observed" {
			t.Fatalf("%s: timeline %+v", tc.name, out.Timeline)
		}
		checkTimelineShape(t, tc.name, in, out)
		for round := 0; round < 10; round++ {
			mixed := in
			mixed.CodexRows, mixed.Heads = shuffled(rng, in.CodexRows), shuffled(rng, in.Heads)
			mixed.Snapshots, mixed.CopiedIDs = shuffled(rng, in.Snapshots), shuffled(rng, in.CopiedIDs)
			if got := ComputeWithTimeline(mixed); !reflect.DeepEqual(got, out) {
				t.Fatalf("%s: shuffled input (round %d) changed the result", tc.name, round)
			}
		}
	}
	if codex == 0 {
		t.Fatal("no Codex fixture")
	}
}

func lifecycle(observed int, turns ...SnapshotCodexTurn) Snapshot {
	return Snapshot{UserID: "user-1", ProfileEmail: "dev@example.com", ObservedAt: at(observed), CodexTurns: turns}
}

// A usage row is placed by its own root turn id, or its turn id raised to the
// root the snapshots agree on. User rows, rows without a turn id and any root
// two sources disagree on are not placed, whatever r2 chose.
func TestCodexTimelinePlacesUsageByExplicitRoot(t *testing.T) {
	usage := func(id int64, sec int, turnID, rootID string) CodexRow {
		return CodexRow{ID: id, TS: at(sec), RecordType: "usage", ProfileEmail: "dev@example.com", TurnID: turnID, RootTurnID: rootID}
	}
	cases := []struct {
		name  string
		in    Input
		want  map[string]string
		turns []string
	}{
		{"root and user rows", withUUIDs(codexSession().in),
			map[string]string{"content-1": "-codex_time_interval", "content-2": "turn-1 []", "content-3": "turn-1 []"},
			[]string{"turn-1"}},
		{"a sub-turn raised to its root by the lifecycle", withUUIDs(Input{Agent: "codex",
			CodexRows: []CodexRow{usage(1, 1, "root", "root"), usage(2, 2, "sub", "")},
			Snapshots: []Snapshot{lifecycle(5, SnapshotCodexTurn{TurnID: "sub", RootTurnID: "root"})}}),
			map[string]string{"content-1": "root []", "content-2": "root []"}, []string{"root"}},
		{"snapshots disagree on a sub-turn's root", withUUIDs(Input{Agent: "codex",
			CodexRows: []CodexRow{usage(1, 1, "a", "a"), usage(2, 2, "b", "b"), usage(3, 3, "sub", "")},
			Snapshots: []Snapshot{
				lifecycle(5, SnapshotCodexTurn{TurnID: "sub", RootTurnID: "a"}),
				lifecycle(6, SnapshotCodexTurn{TurnID: "sub", RootTurnID: "b"}),
			}}),
			map[string]string{"content-1": "a []", "content-2": "b []", "content-3": "-ambiguous_root"}, []string{"a", "b"}},
		{"the row's root disagrees with the lifecycle", withUUIDs(Input{Agent: "codex",
			CodexRows: []CodexRow{usage(1, 1, "a", "a"), usage(2, 2, "sub", "a")},
			Snapshots: []Snapshot{lifecycle(5, SnapshotCodexTurn{TurnID: "sub", RootTurnID: "b"})}}),
			map[string]string{"content-1": "a []", "content-2": "-conflicting_evidence"}, []string{"a"}},
		{"the root belongs to another root", withUUIDs(Input{Agent: "codex",
			CodexRows: []CodexRow{usage(1, 1, "", "r1"), usage(2, 2, "sub", "")},
			Snapshots: []Snapshot{lifecycle(5,
				SnapshotCodexTurn{TurnID: "r1", RootTurnID: "r0"}, SnapshotCodexTurn{TurnID: "sub", RootTurnID: "r1"})}}),
			map[string]string{"content-1": "-conflicting_evidence", "content-2": "-conflicting_evidence"}, []string{"r1"}},
		{"rows no turn id names", withUUIDs(Input{Agent: "codex", CopyGate: true, CopiedIDs: []int64{4, 5},
			CodexRows: []CodexRow{
				usage(1, 1, "", ""),
				{ID: 2, TS: at(2), RecordType: "session_meta", TurnID: "t"},
				{ID: 3, TS: at(3), RecordType: "user", TextHead: "hello"},
				usage(4, 4, "t", "t"),
				usage(5, 5, "t", "t"),
				usage(6, 6, "t", "t"),
				usage(7, 6, "t", "t"),
			}}),
			map[string]string{"content-1": "-codex_no_turn_id", "content-2": "-codex_not_usage", "content-3": "-codex_time_interval",
				"content-4": "-copied_row", "content-5": "-copied_row", "content-6": "t []", "content-7": "t []"},
			[]string{"t"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := ComputeWithTimeline(tc.in)
			if got := turnsBy(out.Output, "key"); !reflect.DeepEqual(got, tc.turns) {
				t.Fatalf("r2 turns %v, want %v", got, tc.turns)
			}
			expectRows(t, out.Timeline, tc.want)
		})
	}
}

func TestCodexTimelineAmbiguousKeysAndOldLoader(t *testing.T) {
	in := withUUIDs(Input{Agent: "codex", CopyGate: true, CopiedIDs: []int64{2}, CodexRows: []CodexRow{
		{ID: 1, TS: at(1), RecordType: "usage", RootTurnID: "t"},
		{ID: 1, TS: at(1), RecordType: "usage", RootTurnID: "t"},
		{ID: 2, TS: at(2), RecordType: "usage", RootTurnID: "t"},
		{ID: 2, TS: at(3), RecordType: "usage", RootTurnID: "t"},
		{ID: 3, TS: at(4), RecordType: "usage", RootTurnID: "t"},
	}})
	in.CodexRows[3].UUID = "content-2b"
	expectRows(t, ComputeWithTimeline(in).Timeline, map[string]string{
		"content-1":  "-duplicate_source_tuple",
		"content-2":  "-ambiguous_copy_id",
		"content-2b": "-ambiguous_copy_id",
		"content-3":  "t []",
	})
	old := in
	old.CodexRows = append([]CodexRow(nil), in.CodexRows...)
	old.CodexRows[4].UUID = ""
	if got := ComputeWithTimeline(old).Timeline; got.State != "not_collected" || got.Rows != nil {
		t.Fatalf("a row without its uuid: %+v", got)
	}
}

// Usage rows are sources too: two rows of one turn id naming different roots
// disagree, and neither is placed, in any row or snapshot order. Rows that
// repeat one root agree.
func TestCodexTimelineUsageRowsMustAgreeOnRoot(t *testing.T) {
	usage := func(id int64, sec int, turnID, rootID string) CodexRow {
		return CodexRow{ID: id, TS: at(sec), RecordType: "usage", ProfileEmail: "dev@example.com", TurnID: turnID, RootTurnID: rootID}
	}
	cases := []struct {
		name string
		in   Input
		want map[string]string
	}{
		{"two rows of sub name a and b", withUUIDs(Input{Agent: "codex", CodexRows: []CodexRow{
			usage(1, 1, "a", "a"), usage(2, 2, "b", "b"), usage(3, 3, "sub", "a"), usage(4, 4, "sub", "b"),
		}}), map[string]string{"content-1": "a []", "content-2": "b []", "content-3": "-conflicting_evidence", "content-4": "-conflicting_evidence"}},
		{"rows of sub repeat a", withUUIDs(Input{Agent: "codex", CodexRows: []CodexRow{
			usage(1, 1, "a", "a"), usage(2, 2, "sub", "a"), usage(3, 3, "sub", "a"),
		}}), map[string]string{"content-1": "a []", "content-2": "a []", "content-3": "a []"}},
		// r2 counts the bare row in a turn of its own, sub; the other row says
		// sub belongs to a.
		{"a row without a root against a row of its turn id", withUUIDs(Input{Agent: "codex", CodexRows: []CodexRow{
			usage(1, 1, "a", "a"), usage(2, 2, "sub", "a"), usage(3, 3, "sub", ""),
		}}), map[string]string{"content-1": "a []", "content-2": "a []", "content-3": "-conflicting_evidence"}},
		{"a row without a root that the lifecycle and the rows agree on", withUUIDs(Input{Agent: "codex",
			CodexRows: []CodexRow{usage(1, 1, "a", "a"), usage(2, 2, "sub", "a"), usage(3, 3, "sub", "")},
			Snapshots: []Snapshot{lifecycle(5, SnapshotCodexTurn{TurnID: "sub", RootTurnID: "a"})}}),
			map[string]string{"content-1": "a []", "content-2": "a []", "content-3": "a []"}},
		{"a row and the lifecycle name different roots", withUUIDs(Input{Agent: "codex",
			CodexRows: []CodexRow{usage(1, 1, "a", "a"), usage(2, 2, "b", "b"), usage(3, 3, "sub", "a"), usage(4, 4, "sub", "")},
			Snapshots: []Snapshot{lifecycle(5, SnapshotCodexTurn{TurnID: "sub", RootTurnID: "b"})}}),
			map[string]string{"content-1": "a []", "content-2": "b []", "content-3": "-conflicting_evidence", "content-4": "-ambiguous_root"}},
		{"a root that a row names as a sub-turn", withUUIDs(Input{Agent: "codex", CodexRows: []CodexRow{
			usage(1, 1, "", "a"), usage(2, 2, "a", "x"), usage(3, 3, "x", "x"),
		}}), map[string]string{"content-1": "-conflicting_evidence", "content-2": "-conflicting_evidence", "content-3": "x []"}},
	}
	rng := rand.New(rand.NewSource(73))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := ComputeWithTimeline(tc.in)
			legacy := Compute(tc.in)
			stripped := out.Output
			stripped.Hash = legacy.Hash
			if !reflect.DeepEqual(stripped, legacy) {
				t.Fatal("timeline path changed the r2 output")
			}
			expectRows(t, out.Timeline, tc.want)
			for round := 0; round < 25; round++ {
				mixed := tc.in
				mixed.CodexRows, mixed.Snapshots = shuffled(rng, tc.in.CodexRows), shuffled(rng, tc.in.Snapshots)
				if got := ComputeWithTimeline(mixed); !reflect.DeepEqual(got, out) {
					t.Fatalf("shuffled input (round %d) changed the result", round)
				}
			}
		})
	}
}
