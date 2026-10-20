package sessionfacts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

const commitHashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const commitHashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func commitFixture() Input {
	f := session(
		typed("u", "", 0, "p", "make a change"),
		call("c", "u", 1, "call", "Bash", blocks(`[{"t":"tool_use","id":"call","n":"Bash","in":{"gc":"git commit -m change"}}]`)),
		result("r", "c", 2, "call"),
	)
	for i := range f.in.Rows {
		f.in.Rows[i].SourceFile = "s.jsonl"
		f.in.Rows[i].CctraceCollect = json.RawMessage(`2`)
	}
	subject := "change"
	f.in.Snapshots = []Snapshot{{UserID: "user-1", ProfileEmail: "dev@example.com", LoginEmail: f.in.Rows[1].LoginEmail, BillingProvider: "anthropic", Commits: &SnapshotCommits{
		Trees: []CommitTree{{Repository: "github.com/o/r", Branch: "main"}},
		Items: []SnapshotCommit{{Hash: commitHashA, At: at(2).Unix(), Kind: "commit", Call: "call", Transcript: "s.jsonl", Proof: "output_hash", Subject: &subject}},
	}}}
	f.in.CommitClaims = []CommitClaim{{Repository: "github.com/o/r", Hash: commitHashA}}
	f.in.CommitResults = []CommitResult{{SrecID: 3, SourceFile: "s.jsonl", Call: "call", Text: "[main aaaaaaa] change"}}
	return f.in
}

func TestCommitComputationRequiresExplicitOptIn(t *testing.T) {
	in := commitFixture()
	legacy := Compute(in)
	without := in
	without.Snapshots = append([]Snapshot(nil), in.Snapshots...)
	without.Snapshots[0].Commits = nil
	without.CommitClaims, without.CommitResults = nil, nil
	if !reflect.DeepEqual(legacy, Compute(without)) {
		t.Fatal("legacy computation activated commits")
	}
	wire, err := json.Marshal(legacy)
	if err != nil || bytes.Contains(wire, []byte(`"Commits"`)) {
		t.Fatal("legacy output JSON includes commits")
	}
	extended := ComputeWithCommits(in)
	if len(extended.Commits.Items) != 1 || bytes.Equal(legacy.Hash, extended.Hash) {
		t.Fatal("extended path not activated")
	}
	if !bytes.Equal(legacy.Hash, HashOutput(legacy)) {
		t.Fatal("legacy hash included opt-in facts")
	}
	changed := extended
	changed.Commits.Items = append([]Commit(nil), extended.Commits.Items...)
	changed.Commits.Items[0].Subject = "different"
	if bytes.Equal(HashOutputWithCommits(changed), extended.Hash) {
		t.Fatal("extended hash omitted commit field")
	}
}

func TestSnapshotCommitsSurviveMissingCallEvidence(t *testing.T) {
	for _, mode := range []string{"hidden", "unsynced", "snapshot only", "missing source", "different file"} {
		t.Run(mode, func(t *testing.T) {
			in := commitFixture()
			switch mode {
			case "hidden":
				in.Rows[1].Blocks = json.RawMessage(`["[REDACTED]"]`)
			case "unsynced":
				in.Rows = append(in.Rows[:1], in.Rows[2:]...)
			case "snapshot only":
				in.Rows = nil
			case "missing source":
				in.Rows[1].SourceFile = ""
			case "different file":
				in.Snapshots[0].Commits.Items[0].Transcript = "other.jsonl"
			}
			got := ComputeWithCommits(in).Commits
			found := false
			for _, item := range got.Items {
				if item.Source == "git" {
					found = true
					if item.Hash != commitHashA || item.TurnKey != "" || item.CallSrecID != 0 {
						t.Fatalf("invented attribution: %+v", item)
					}
				}
			}
			if !found || got.State != "undeterminable" || got.Total != nil {
				t.Fatalf("lost snapshot: %+v", got)
			}
			in.CommitClaims[0].Conflicting = true
			for _, item := range ComputeWithCommits(in).Commits.Items {
				if item.Source == "git" {
					t.Fatal("conflicting claim retained")
				}
			}
		})
	}
	for _, mode := range []string{"hidden", "unsynced"} {
		t.Run("unresolved/"+mode, func(t *testing.T) {
			in := commitFixture()
			in.Snapshots[0].Commits.Items = nil
			in.Snapshots[0].Commits.Unresolved = []UnresolvedCommit{{Call: "call", Transcript: "s.jsonl", At: at(1).Unix()}}
			if mode == "hidden" {
				in.Rows[1].Blocks = nil
			} else {
				in.Rows = in.Rows[:1]
			}
			got := ComputeWithCommits(in).Commits
			if got.State != "undeterminable" || got.Total != nil || len(got.Items) != 1 || got.Items[0].Source != "call" || got.Items[0].TurnKey != "" || got.Items[0].Hash != "" {
				t.Fatalf("lost unresolved: %+v", got)
			}
			in.Snapshots[0].UserID = "wrong"
			if len(ComputeWithCommits(in).Commits.Items) != 0 {
				t.Fatal("wrong snapshot identity accepted")
			}
		})
	}
}

func TestDynamicCommitMessagesPreserveOutputReceipts(t *testing.T) {
	var want CommitFacts
	for _, option := range []string{`-m "$MESSAGE"`, `--message "$MESSAGE"`, `--message="$MESSAGE"`, `-m"$MESSAGE"`, `-am "$MESSAGE"`, `-am"$MESSAGE"`} {
		in := commitFixture()
		in.Snapshots = nil
		data, err := json.Marshal([]map[string]any{{"t": "tool_use", "id": "call", "n": "Bash", "in": map[string]string{"gc": "git commit " + option}}})
		if err != nil {
			t.Fatal(err)
		}
		in.Rows[1].Blocks = data
		got := ComputeWithCommits(in).Commits
		if got.State != "observed" || got.Total == nil || *got.Total != 1 || len(got.Items) != 1 || got.Items[0].Source != "output" || got.Items[0].Short != "aaaaaaa" || got.Items[0].SubjectState != "none" || got.Items[0].Subject != "" {
			t.Fatalf("receipt lost for %q: %+v", option, got)
		}
		if want.State == "" {
			want = got
		} else if !reflect.DeepEqual(got, want) {
			t.Fatalf("message forms differ: %q %+v", option, got)
		}
	}
}

func TestMultipleCommitAttemptsDoNotProveEachSucceeded(t *testing.T) {
	in := commitFixture()
	in.Snapshots = nil
	in.Rows[1].Blocks = json.RawMessage(`[{"t":"tool_use","id":"call","n":"Bash","in":{"gc":"git commit -m first; git commit -m second; echo done"}}]`)
	in.CommitResults[0].Text = "[main aaaaaaa] first\nnothing to commit, working tree clean\ndone"
	got := ComputeWithCommits(in).Commits
	if got.State != "undeterminable" || got.Total != nil || len(got.Items) != 1 || got.Items[0].Source != "output" {
		t.Fatalf("overcounted attempts: %+v", got)
	}
}

func TestCommitFallbackPreservesSubjectPrivacy(t *testing.T) {
	for _, state := range []string{"withheld", "redacted"} {
		for _, reason := range []string{"claim", "receipt", "metadata"} {
			t.Run(state+"/"+reason, func(t *testing.T) {
				in := commitFixture()
				item := &in.Snapshots[0].Commits.Items[0]
				item.Subject = nil
				if state == "redacted" {
					value := marker
					item.Subject = &value
				}
				switch reason {
				case "claim":
					in.CommitClaims[0].Conflicting = true
				case "receipt":
					in.CommitResults[0].Text = "[main bbbbbbb] changed"
				case "metadata":
					other := *item
					other.At++
					in.Snapshots[0].Commits.Items = append(in.Snapshots[0].Commits.Items, other)
				}
				got := ComputeWithCommits(in).Commits
				if len(got.Items) != 1 || got.Items[0].Source == "git" || got.Items[0].Subject != "" || got.Items[0].SubjectState != state {
					t.Fatalf("privacy restored: %+v", got)
				}
			})
		}
	}
}

func TestCommitSourceRowsMustBeUnambiguousAndRoleCorrect(t *testing.T) {
	for _, kind := range []string{"user call", "assistant result", "duplicate call same id", "duplicate result same id"} {
		t.Run(kind, func(t *testing.T) {
			in := commitFixture()
			switch kind {
			case "user call":
				in.Rows[1].RecordType = "user"
			case "assistant result":
				in.Rows[2].RecordType = "assistant"
			case "duplicate call same id":
				other := in.Rows[1]
				other.UUID = "different-source-tuple"
				other.TS = at(5)
				in.Rows = append(in.Rows, other)
			case "duplicate result same id":
				other := in.Rows[2]
				other.UUID = "different-result-tuple"
				other.ProfileEmail = "other"
				in.Rows = append(in.Rows, other)
			}
			got := ComputeWithCommits(in).Commits
			if got.State != "undeterminable" {
				t.Fatalf("ambiguous evidence counted: %+v", got)
			}
			if len(got.Items) != 0 {
				t.Fatalf("invalid source emitted: %+v", got)
			}
		})
	}
}

func TestCommitFactsEvidence(t *testing.T) {
	cases := []struct {
		name                        string
		change                      func(*Input)
		state, source, subjectState string
		count                       int
	}{
		{"git", func(*Input) {}, "observed", "git", "ok", 1},
		{"withheld", func(in *Input) { in.Snapshots[0].Commits.Items[0].Subject = nil }, "observed", "git", "withheld", 1},
		{"redacted", func(in *Input) { s := marker; in.Snapshots[0].Commits.Items[0].Subject = &s }, "observed", "git", "redacted", 1},
		{"output", func(in *Input) { in.Snapshots = nil }, "observed", "output", "ok", 1},
		{"call", func(in *Input) { in.Snapshots = nil; in.CommitResults[0].Text = "done" }, "observed", "call", "ok", 1},
		{"old output", func(in *Input) {
			in.Snapshots = nil
			for i := range in.Rows {
				in.Rows[i].CctraceCollect = nil
			}
		}, "not_collected", "output", "ok", 1},
		{"missing result", func(in *Input) { in.Snapshots = nil; in.CommitResults = nil }, "undeterminable", "", "", 0},
		{"nothing", func(in *Input) {
			in.Snapshots = nil
			in.CommitResults[0].Text = "nothing to commit, working tree clean"
		}, "observed", "", "", 0},
		{"failed", func(in *Input) {
			in.Snapshots = nil
			in.CommitResults[0].Text = "failed"
			in.Rows[2].Blocks = json.RawMessage(`[{"t":"tool_result","tu":"call","err":true}]`)
		}, "observed", "", "", 0},
		{"partial failure receipt", func(in *Input) {
			in.Snapshots = nil
			in.Rows[2].Blocks = json.RawMessage(`[{"t":"tool_result","tu":"call","err":true}]`)
		}, "observed", "output", "ok", 1},
		{"cross session conflict", func(in *Input) { in.CommitClaims[0].Conflicting = true }, "undeterminable", "call", "ok", 1},
		{"unchecked claim", func(in *Input) { in.CommitClaims = nil }, "undeterminable", "call", "ok", 1},
		{"malformed hash", func(in *Input) { in.Snapshots[0].Commits.Items[0].Hash = "short" }, "undeterminable", "output", "ok", 1},
		{"bad tree", func(in *Input) { in.Snapshots[0].Commits.Items[0].Tree = -1 }, "undeterminable", "output", "ok", 1},
		{"wrong identity", func(in *Input) { in.Snapshots[0].UserID = "other" }, "undeterminable", "output", "ok", 1},
		{"truncated but transcript complete", func(in *Input) { in.Snapshots[0].Truncated = []string{"commits"} }, "observed", "git", "ok", 1},
		{"malformed document", func(in *Input) { in.Snapshots[0].Commits.Malformed = true }, "undeterminable", "git", "ok", 1},
		{"copied", func(in *Input) { in.CopyGate = true; in.CopiedIDs = []int64{2, 3} }, "observed", "", "", 0},
		{"copy gate off", func(in *Input) { in.CopiedIDs = []int64{2, 3} }, "observed", "git", "ok", 1},
		{"duplicate call", func(in *Input) { r := in.Rows[1]; r.ID = 4; r.UUID = "duplicate"; in.Rows = append(in.Rows, r) }, "undeterminable", "", "", 0},
		{"redacted command", func(in *Input) {
			in.Rows[1].Blocks = json.RawMessage(`[{"t":"tool_use","id":"call","n":"Bash","in":{"hid":true}}]`)
		}, "undeterminable", "git", "ok", 1},
		{"structured output", func(in *Input) {
			in.Snapshots = nil
			in.CommitResults = nil
			in.Rows[2].ToolUseResult = json.RawMessage(`{"gitOperation":{"commit":{"sha":"` + commitHashA + `"}}}`)
		}, "observed", "output", "ok", 1},
		{"conflicting structured", func(in *Input) {
			in.Snapshots = nil
			in.Rows[2].ToolUseResult = json.RawMessage(`{"gitOperation":{"commit":{"sha":"` + commitHashB + `"}}}`)
		}, "undeterminable", "", "", 0},
		{"oversize command", func(in *Input) {
			data, _ := json.Marshal([]map[string]any{{"t": "tool_use", "id": "call", "n": "Bash", "in": map[string]string{"gc": strings.Repeat("x", 8193)}}})
			in.Rows[1].Blocks = data
			in.Snapshots = nil
		}, "undeterminable", "output", "none", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := commitFixture()
			tc.change(&in)
			got := ComputeWithCommits(in).Commits
			if got.State != tc.state || len(got.Items) != tc.count {
				t.Fatalf("got %+v", got)
			}
			if tc.state == "observed" {
				if got.Total == nil || *got.Total != tc.count {
					t.Fatalf("total: %+v", got)
				}
			} else if got.Total != nil {
				t.Fatalf("unknown total: %+v", got)
			}
			if tc.count > 0 {
				r := got.Items[0]
				if r.Source != tc.source || r.SubjectState != tc.subjectState || r.TurnKey != "u" || r.CallSrecID != 2 {
					t.Fatalf("row: %+v", r)
				}
				if r.Source != "git" && r.Hash != "" {
					t.Fatal("promoted output receipt to full hash")
				}
				if r.SubjectState != "ok" && r.Subject != "" {
					t.Fatal("restored private content")
				}
			}
		})
	}
}

func TestCommitFactsExactThreadsAndAbandoned(t *testing.T) {
	in := commitFixture()
	// The same call id in a child thread has a distinct exact source file.
	child := call("sc", "", 3, "call", "Bash", sidechain("child"), blocks(`[{"t":"tool_use","id":"call","n":"Bash","in":{"gc":"git commit"}}]`))
	child.ID, child.SourceFile = 4, "agent-child.jsonl"
	child.ToolUseID = "call"
	res := result("sr", "sc", 4, "call", sidechain("child"))
	res.ID, res.SourceFile = 5, child.SourceFile
	in.Rows = append(in.Rows, child, res)
	in.CommitResults = append(in.CommitResults, CommitResult{SrecID: 5, SourceFile: child.SourceFile, Call: "call", Text: "[main bbbbbbb] child"})
	item := in.Snapshots[0].Commits.Items[0]
	item.Hash, item.Transcript, item.At = commitHashB, child.SourceFile, at(4).Unix()
	in.Snapshots[0].Commits.Items = append(in.Snapshots[0].Commits.Items, item)
	in.CommitClaims = append(in.CommitClaims, CommitClaim{Repository: "github.com/o/r", Hash: commitHashB})
	got := ComputeWithCommits(in).Commits
	if len(got.Items) != 2 || got.Items[0].TurnKey != "u" || got.Items[1].TurnKey != "u" || got.Items[0].CallSrecID == got.Items[1].CallSrecID {
		t.Fatalf("threads: %+v", got)
	}
	in.Rows[3].ToolUseID = "missing"
	got = ComputeWithCommits(in).Commits
	if len(got.Items) != 2 || got.Items[1].TurnKey != "" {
		t.Fatalf("time fallback invented turn: %+v", got)
	}

	in = commitFixture()
	root := typed("root", "", -2, "root-p", "root")
	root.ID = 4
	in.Rows[0].ParentUUID = "root"
	newBranch := typed("new", "root", 5, "new-p", "rewind")
	newBranch.ID = 5
	in.Rows = append(in.Rows, root, newBranch)
	out := ComputeWithCommits(in)
	abandoned := false
	for _, turn := range out.Turns {
		if turn.TurnKey == "u" {
			abandoned = turn.Branch == "abandoned"
		}
	}
	if !abandoned || len(out.Commits.Items) != 1 || out.Commits.Items[0].TurnKey != "u" {
		t.Fatalf("abandoned commit lost: %+v", out.Commits)
	}
}

func TestCommitFactsRejectsAmbiguousAndIncompleteEvidence(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Input)
	}{
		{"duplicate selected result", func(in *Input) { in.Snapshots = nil; in.CommitResults = append(in.CommitResults, in.CommitResults[0]) }},
		{"wrong selected row", func(in *Input) { in.Snapshots = nil; in.CommitResults[0].SrecID = 99 }},
		{"result identity", func(in *Input) { in.Snapshots = nil; in.Rows[2].LoginEmail = "other" }},
		{"truncated result", func(in *Input) { in.Snapshots = nil; in.CommitResults[0].Truncated = true }},
		{"oversize result", func(in *Input) { in.Snapshots = nil; in.CommitResults[0].Text = strings.Repeat("x", 65537) }},
		{"snapshot overflow", func(in *Input) {
			item := in.Snapshots[0].Commits.Items[0]
			for i := 0; i < 100; i++ {
				in.Snapshots[0].Commits.Items = append(in.Snapshots[0].Commits.Items, item)
			}
		}},
		{"unknown unresolved", func(in *Input) {
			in.Snapshots[0].Commits.Items = nil
			in.Snapshots[0].Commits.Unresolved = []UnresolvedCommit{{Call: "missing", Transcript: "s.jsonl"}}
		}},
		{"ambiguous short hash", func(in *Input) {
			item := in.Snapshots[0].Commits.Items[0]
			item.Hash = commitHashA[:39] + "b"
			in.Snapshots[0].Commits.Items = append(in.Snapshots[0].Commits.Items, item)
			in.CommitClaims = append(in.CommitClaims, CommitClaim{Repository: "github.com/o/r", Hash: item.Hash})
		}},
		{"metadata conflict", func(in *Input) {
			item := in.Snapshots[0].Commits.Items[0]
			item.At++
			in.Snapshots[0].Commits.Items = append(in.Snapshots[0].Commits.Items, item)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := commitFixture()
			tc.change(&in)
			got := ComputeWithCommits(in).Commits
			if got.State != "undeterminable" || got.Total != nil {
				t.Fatalf("invented known count: %+v", got)
			}
			for _, item := range got.Items {
				if item.Source == "git" {
					t.Fatalf("unverified git: %+v", item)
				}
			}
		})
	}
}

func TestCommitFactsNonCommitKindsAndSameHashClaims(t *testing.T) {
	for _, kind := range []string{"merge", "cherry_pick", "revert"} {
		t.Run(kind, func(t *testing.T) {
			in := commitFixture()
			data, _ := json.Marshal([]map[string]any{{"t": "tool_use", "id": "call", "n": "Bash", "in": map[string]string{"gc": "git " + strings.ReplaceAll(kind, "_", "-") + " branch"}}})
			in.Rows[1].Blocks = data
			in.Snapshots[0].Commits.Items[0].Kind = kind
			got := ComputeWithCommits(in).Commits
			if got.State != "observed" || len(got.Items) != 1 || got.Items[0].Kind != kind || got.Items[0].Source != "git" {
				t.Fatalf("kind: %+v", got)
			}
		})
	}
	in := commitFixture()
	c := call("c2", "r", 3, "second", "Bash", blocks(`[{"t":"tool_use","id":"second","n":"Bash","in":{"gc":"git commit"}}]`))
	c.ID, c.SourceFile = 4, "s.jsonl"
	r := result("r2", "c2", 4, "second")
	r.ID, r.SourceFile = 5, "s.jsonl"
	in.Rows = append(in.Rows, c, r)
	in.CommitResults = append(in.CommitResults, CommitResult{SrecID: 5, SourceFile: "s.jsonl", Call: "second", Text: "[main aaaaaaa] duplicate"})
	item := in.Snapshots[0].Commits.Items[0]
	item.Call = "second"
	in.Snapshots[0].Commits.Items = append(in.Snapshots[0].Commits.Items, item)
	got := ComputeWithCommits(in).Commits
	if got.State != "undeterminable" || len(got.Items) != 2 {
		t.Fatalf("local conflict: %+v", got)
	}
	for _, r := range got.Items {
		if r.Source != "call" || r.Hash != "" || r.Short != "" {
			t.Fatalf("claim winner invented: %+v", r)
		}
	}
}

func TestCommitFactsAmendAndBounds(t *testing.T) {
	in := commitFixture()
	c := call("am", "r", 3, "amend", "Bash", blocks(`[{"t":"tool_use","id":"amend","n":"Bash","in":{"gc":"git commit --amend"}}]`))
	c.ID, c.SourceFile = 4, "s.jsonl"
	r := result("ar", "am", 4, "amend")
	r.ID, r.SourceFile = 5, "s.jsonl"
	in.Rows = append(in.Rows, c, r)
	in.CommitResults = append(in.CommitResults, CommitResult{SrecID: 5, SourceFile: "s.jsonl", Call: "amend", Text: "[main bbbbbbb] amend"})
	item := in.Snapshots[0].Commits.Items[0]
	item.Hash, item.Replaces, item.Kind, item.Call, item.At = commitHashB, commitHashA, "amend", "amend", at(4).Unix()
	subject := strings.Repeat("한", 100)
	item.Subject = &subject
	in.Snapshots[0].Commits.Items = append(in.Snapshots[0].Commits.Items, item)
	in.CommitClaims = append(in.CommitClaims, CommitClaim{Repository: "github.com/o/r", Hash: commitHashB})
	got := ComputeWithCommits(in).Commits
	if got.State != "observed" || len(got.Items) != 1 || got.Items[0].Hash != commitHashB || *got.Total != 1 || len(got.Items[0].Subject) > 200 || !utf8.ValidString(got.Items[0].Subject) {
		t.Fatalf("amend: %+v", got)
	}
}

func TestCommitFactsStoredCapAndHiddenCalls(t *testing.T) {
	in := commitFixture()
	in.Snapshots = nil
	in.CommitResults = nil
	in.Rows = in.Rows[:1]
	for i := 0; i < 501; i++ {
		id := fmt.Sprintf("c%d", i)
		data, _ := json.Marshal([]map[string]any{{"t": "tool_use", "id": id, "n": "Bash", "in": map[string]string{"gc": "git commit -q"}}})
		c := call(id, "u", i*2+1, id, "Bash", blocks(string(data)))
		c.ID = int64(i*2 + 2)
		c.SourceFile = "s.jsonl"
		r := result("r"+id, id, i*2+2, id)
		r.ID = c.ID + 1
		r.SourceFile = "s.jsonl"
		in.Rows = append(in.Rows, c, r)
		in.CommitResults = append(in.CommitResults, CommitResult{SrecID: r.ID, SourceFile: "s.jsonl", Call: id, Text: "done"})
	}
	got := ComputeWithCommits(in).Commits
	if got.State != "observed" || got.Total == nil || *got.Total != 501 || !got.Truncated || len(got.Items) != 500 || got.Items[0].Call != "c1" {
		t.Fatalf("stored cap: %+v", got)
	}
	in = commitFixture()
	in.Snapshots[0].Commits.Items = nil
	in.Snapshots[0].Commits.Unresolved = []UnresolvedCommit{{Call: "call", Transcript: "s.jsonl"}}
	in.Rows[1].Blocks = json.RawMessage(`[{"t":"tool_use","id":"call","n":"Bash","in":{"hid":true}}]`)
	in.CommitResults[0].Text = "done"
	got = ComputeWithCommits(in).Commits
	if got.State != "undeterminable" || len(got.Items) != 1 || got.Items[0].Source != "call" {
		t.Fatalf("hidden unresolved: %+v", got)
	}
	in = commitFixture()
	in.CommitClaims[0].Conflicting = true
	in.Rows[1].Blocks = json.RawMessage(`[{"t":"tool_use","id":"call","n":"Bash","in":{"hid":true}}]`)
	got = ComputeWithCommits(in).Commits
	if got.State != "undeterminable" || len(got.Items) != 1 || got.Items[0].Source != "call" || got.Items[0].Short != "" {
		t.Fatalf("hidden disputed receipt: %+v", got)
	}
}

func TestCommitFactsShuffleAndInputImmutability(t *testing.T) {
	in := commitFixture()
	copySnapshot := in.Snapshots[0]
	copyCommits := *copySnapshot.Commits
	copyCommits.Items = append([]SnapshotCommit(nil), copyCommits.Items...)
	copyCommits.Items[0].Subject = nil
	copySnapshot.Commits = &copyCommits
	in.Snapshots = append(in.Snapshots, copySnapshot)
	in.CommitClaims = append(in.CommitClaims, in.CommitClaims[0])
	before, _ := json.Marshal(in)
	want := ComputeWithCommits(in)
	if want.Commits.Items[0].SubjectState != "withheld" {
		t.Fatal("dedup restored subject")
	}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 30; i++ {
		mixed := in
		mixed.Rows = shuffled(rng, in.Rows)
		mixed.Snapshots = shuffled(rng, in.Snapshots)
		for j := range mixed.Snapshots {
			cs := *mixed.Snapshots[j].Commits
			cs.Items = shuffled(rng, cs.Items)
			cs.Unresolved = shuffled(rng, cs.Unresolved)
			mixed.Snapshots[j].Commits = &cs
		}
		mixed.CommitResults = shuffled(rng, in.CommitResults)
		mixed.CommitClaims = shuffled(rng, in.CommitClaims)
		if got := ComputeWithCommits(mixed); !reflect.DeepEqual(got, want) {
			t.Fatalf("shuffle changed output: %+v != %+v", got.Commits, want.Commits)
		}
	}
	after, _ := json.Marshal(in)
	if !bytes.Equal(before, after) {
		t.Fatal("Compute changed nested input")
	}
}
