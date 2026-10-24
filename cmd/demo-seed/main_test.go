package main

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cctrace/internal/codexlog"
	"cctrace/internal/commandclass"
	"cctrace/internal/insights"
	"cctrace/internal/sessionlog"
)

var testAnchor = time.Date(2026, 10, 21, 18, 0, 0, 0, kst)

// fixture is the two-day plan the tests read, every session as the seeder
// writes it, and what the client's parsers read back from it. It is built once:
// rendering and parsing it is most of this file's time under -race, and no test
// changes it.
type fixture struct {
	plan   *plan
	files  map[string][]byte // session id -> transcript or rollout
	claude map[string][]*sessionlog.Record
	codex  map[string][]*codexlog.Record
	err    error
}

var sharedFixture = sync.OnceValue(func() fixture {
	pl := buildPlan(1, testAnchor, 2)
	f := fixture{plan: pl, files: map[string][]byte{}, claude: map[string][]*sessionlog.Record{}, codex: map[string][]*codexlog.Record{}}
	dir, err := os.MkdirTemp("", "demo-seed-test")
	if err != nil {
		f.err = err
		return f
	}
	defer os.RemoveAll(dir)
	type parsed struct {
		data   []byte
		claude []*sessionlog.Record
		codex  []*codexlog.Record
		err    error
	}
	out := make([]parsed, len(pl.Sessions))
	var wg sync.WaitGroup
	slots := make(chan struct{}, runtime.GOMAXPROCS(0))
	for i, s := range pl.Sessions {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer func() { <-slots; wg.Done() }()
			p := &out[i]
			p.data = render(s)
			path := filepath.Join(dir, s.ID+".jsonl")
			if s.Agent == "codex" {
				path = filepath.Join(dir, filepath.Base(codexRolloutPath(s)))
			}
			if p.err = os.WriteFile(path, p.data, 0o644); p.err != nil {
				return
			}
			if s.Agent == "claude" {
				p.claude, _, p.err = sessionlog.ScanFile(path, 0)
			} else {
				p.codex, _, p.err = codexlog.ScanFile(path, 0, s.ID)
			}
		}()
	}
	wg.Wait()
	for i, s := range pl.Sessions {
		f.files[s.ID], f.claude[s.ID], f.codex[s.ID] = out[i].data, out[i].claude, out[i].codex
		if f.err == nil {
			f.err = out[i].err
		}
	}
	return f
})

func render(s *session) []byte {
	if s.Agent == "claude" {
		return claudeTranscript("/home/"+s.P.Home, s)
	}
	return codexRollout("/home/"+s.P.Home, s)
}

func testPlan(t *testing.T) fixture {
	t.Helper()
	f := sharedFixture()
	if f.err != nil {
		t.Fatal(f.err)
	}
	if len(f.plan.Sessions) == 0 {
		t.Fatal("plan has no sessions")
	}
	return f
}

// Every line the seeder writes has to survive the client's own parser: a line the
// scanner drops never reaches the server, and the demo quietly loses it.
func TestClaudeTranscriptParsesLineForLine(t *testing.T) {
	t.Parallel()
	checked := 0
	f := testPlan(t)
	for _, s := range f.plan.Sessions {
		if s.Agent != "claude" {
			continue
		}
		data, records := f.files[s.ID], f.claude[s.ID]
		if lines := bytes.Count(data, []byte("\n")); len(records) != lines {
			t.Fatalf("session %s: %d records from %d lines", s.ID, len(records), lines)
		}
		for _, r := range records {
			if r.Type != "assistant" {
				continue
			}
			msg := r.ParseAssistantMessage()
			if msg == nil || msg.Model == "" || msg.Usage.OutputTokens == 0 {
				t.Fatalf("session %s: assistant record without model or usage: %s", s.ID, r.Message)
			}
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no Claude sessions in the plan")
	}
}

func TestCodexRolloutYieldsUsage(t *testing.T) {
	t.Parallel()
	checked := 0
	f := testPlan(t)
	for _, s := range f.plan.Sessions {
		if s.Agent != "codex" {
			continue
		}
		usage, calls := 0, 0
		for _, r := range f.codex[s.ID] {
			switch r.RecordType {
			case "usage":
				if r.Model != s.Model {
					t.Fatalf("usage model = %q, want %q", r.Model, s.Model)
				}
				usage++
			case "tool_call":
				calls++
			}
		}
		requests := 0
		for _, tr := range s.Turns {
			requests += len(tr.Requests)
		}
		if usage != requests {
			t.Fatalf("session %s: %d usage records for %d model responses", s.ID, usage, requests)
		}
		if calls == 0 {
			t.Fatalf("session %s: no tool calls parsed", s.ID)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no Codex sessions in the plan")
	}
}

// The Plugins & Skills page counts what the client classified as a plugin's
// command or skill while it still had the plugin directories. A command or skill
// the seeder writes has to come out of that classification the same way, and an
// applied skill has to be counted once per turn, as sync counts it.
func TestPluginUseIsClassifiedAsTheClientWould(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	if err := writePlugins(dir); err != nil {
		t.Fatal(err)
	}
	idx := commandclass.NewIndex(claudeDir, "")
	commands, skills, codexSkills := 0, 0, 0
	f := testPlan(t)
	for _, s := range f.plan.Sessions {
		wantSkills, wantCommands := 0, 0
		for _, tr := range s.Turns {
			if tr.Skill != "" {
				wantSkills++
			}
			if tr.Command != "" {
				wantCommands++
			}
		}
		if s.Agent == "codex" {
			got := 0
			for _, r := range f.codex[s.ID] {
				if r.RecordType == "user" && strings.HasPrefix(r.Content, "<skill>") && strings.Contains(r.Content, "</name>") {
					got++
				}
			}
			if got != wantSkills || wantCommands != 0 {
				t.Fatalf("codex session %s: %d skill injections for %d skill turns, %d commands", s.ID, got, wantSkills, wantCommands)
			}
			codexSkills += got
			continue
		}
		records := f.claude[s.ID]
		gotCommands, gotSkills, active := 0, 0, ""
		for i, r := range records {
			if name := r.ParseCommandName(); name != "" {
				if c := idx.Classify(name); c != (commandclass.Class{Source: commandclass.SourcePlugin, Kind: commandclass.KindCommand}) {
					t.Fatalf("command %s classified as %+v", name, c)
				}
				// The server gives a command the tokens up to the next line a
				// person wrote, and the hidden expansion line counts as one unless
				// it shares the command's time, as Claude Code writes it.
				if next := records[i+1]; !next.IsMeta || !next.Timestamp.Equal(r.Timestamp) {
					t.Fatalf("command %s: expansion line meta=%v at %s, command at %s", name, next.IsMeta, next.Timestamp, r.Timestamp)
				}
				gotCommands++
			}
			// syncer.attributionSkillInvocationName: a person's line ends the skill.
			if r.Type == "user" && !r.IsToolResultUser() {
				active = ""
			}
			if r.AttributionSkill != "" && r.AttributionSkill != active {
				if c := idx.Classify(r.AttributionSkill); c != (commandclass.Class{Source: commandclass.SourcePlugin, Kind: commandclass.KindSkill}) {
					t.Fatalf("skill %s classified as %+v", r.AttributionSkill, c)
				}
				active = r.AttributionSkill
				gotSkills++
			}
		}
		if gotCommands != wantCommands || gotSkills != wantSkills {
			t.Fatalf("claude session %s: %d commands and %d skills counted for %d and %d turns", s.ID, gotCommands, gotSkills, wantCommands, wantSkills)
		}
		commands += gotCommands
		skills += gotSkills
	}
	if commands == 0 || skills == 0 || codexSkills == 0 {
		t.Fatalf("plan uses %d plugin commands, %d Claude skills, %d Codex skills; want some of each", commands, skills, codexSkills)
	}
}

// A recording taken while the server still analyses sessions captures them
// half built ("state": "building"); the recorder waits for the queue to empty.
func TestAnalysisSettledWaitsForAnEmptyQueue(t *testing.T) {
	status := func(depth int, build string) []byte {
		return []byte(`{"token_rule_pending":false,"session_analysis":{"enabled":true,"waiting_for_request_set":false,` +
			`"waiting_for_commit_claims":false,"build":{"state":"` + build + `","remaining":0},"queue":{"depth":` + strconv.Itoa(depth) + `}}}`)
	}
	replies := [][]byte{status(0, "running"), status(906, "done"), status(0, "done")}
	calls := 0
	get := func() ([]byte, error) {
		calls++
		return replies[min(calls, len(replies))-1], nil
	}
	if err := waitForAnalysis(get, 0, time.Minute); err != nil || calls != 3 {
		t.Fatalf("err = %v after %d polls, want nil after 3", err, calls)
	}
	if err := waitForAnalysis(func() ([]byte, error) { return status(5, "done"), nil }, 0, 0); err == nil {
		t.Fatal("a queue that never empties did not time out")
	}
}

// The prompts are written to land in the task types the weekly report shows; a
// wording change that moves them all to "unknown" would empty that card.
func TestPromptsClassifyAsTheirTask(t *testing.T) {
	proj := projects["nebula-ingest"]
	for _, korean := range []bool{false, true} {
		for _, w := range taskWeights {
			tk := task{Kind: w.v, Korean: korean, Project: proj, Comp: proj.Components[0],
				Feature: "jitter on the backoff", Thing: "the retry budget", Case: "attempt overflow"}
			if got := insights.ClassifyPrompt(tk.prompt()); got != w.v {
				t.Errorf("korean=%v %s prompt %q classified as %q", korean, w.v, tk.prompt(), got)
			}
		}
	}
}

func TestSameSeedSameData(t *testing.T) {
	t.Parallel()
	f := testPlan(t)
	b := buildPlan(1, testAnchor, 2)
	if len(f.plan.Sessions) != len(b.Sessions) {
		t.Fatalf("%d vs %d sessions", len(f.plan.Sessions), len(b.Sessions))
	}
	for i, s := range b.Sessions {
		if s.ID != f.plan.Sessions[i].ID || !bytes.Equal(render(s), f.files[s.ID]) {
			t.Fatalf("session %d differs", i)
		}
	}
}

func TestNoGateShapedTokens(t *testing.T) {
	t.Parallel()
	f := testPlan(t)
	for _, s := range f.plan.Sessions {
		// The mirror gate's entropy rule: a run of 32+ base64-alphabet characters
		// with at most one slash (more slashes make it a path).
		for _, m := range base64Runs(f.files[s.ID], 32) {
			if bytes.Count(m, []byte("/")) <= 1 {
				t.Fatalf("session %s carries a token-shaped run %q", s.ID, m)
			}
		}
	}
}

// base64Runs is what [A-Za-z0-9+/=]{min,} finds, every maximal run, without
// the regexp engine, which took 12s of this file's time under -race.
func base64Runs(data []byte, min int) [][]byte {
	var out [][]byte
	start := -1
	for i := 0; i <= len(data); i++ {
		if i < len(data) {
			c := data[i]
			if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '=' {
				if start < 0 {
					start = i
				}
				continue
			}
		}
		if start >= 0 && i-start >= min {
			out = append(out, data[start:i])
		}
		start = -1
	}
	return out
}

// recordKey is the lookup contract the static demo's replay layer has to match;
// web/lib/demo/replay-key.test.ts reads the same cases.
func TestRecordKeyMatchesSharedCases(t *testing.T) {
	data, err := os.ReadFile("testdata/replay-keys.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Now, Method, Path, Query, Key string
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		now, err := time.Parse(time.RFC3339, c.Now)
		if err != nil {
			t.Fatal(err)
		}
		q, err := url.ParseQuery(c.Query)
		if err != nil {
			t.Fatal(err)
		}
		if got := recordKey(c.Method, c.Path, q, now); got != c.Key {
			t.Errorf("%s: recordKey = %q, want %q", c.Name, got, c.Key)
		}
	}
}

// shardOf names the manifest file the replay loads for a path;
// web/lib/demo/replay-key.test.ts reads the same cases.
func TestShardOfMatchesSharedCases(t *testing.T) {
	data, err := os.ReadFile("testdata/replay-shards.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct{ Path, Shard string }
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if got := shardOf(c.Path); got != c.Shard {
			t.Errorf("shardOf(%q) = %q, want %q", c.Path, got, c.Shard)
		}
	}
}

func TestWorkdayAnchorIsAWeekdayAfternoonBeforeNow(t *testing.T) {
	cases := map[string]string{
		"2026-10-24T03:15:00+09:00": "2026-10-23T16:40:00+09:00", // Saturday night: Friday
		"2026-10-26T10:00:00+09:00": "2026-10-23T16:40:00+09:00", // Monday morning: last Friday
		"2026-10-26T17:00:00+09:00": "2026-10-26T16:40:00+09:00", // Monday evening: today
	}
	for now, want := range cases {
		n, _ := time.Parse(time.RFC3339, now)
		if got := workdayAnchor(n).Format(time.RFC3339); got != want {
			t.Errorf("workdayAnchor(%s) = %s, want %s", now, got, want)
		}
	}
}
