package sessionfacts

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"cctrace/internal/sessiontranscript"
)

func TestTranscriptIOHash(t *testing.T) {
	t.Parallel()
	in := commitFixture()
	legacy := Compute(in)
	legacyJSON, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var previous *TimelineCommitOutput
	for _, count := range []int64{5, 6} {
		in.TranscriptIO = &TranscriptIODetail{Version: TranscriptIOVersion, Owners: []TranscriptIOOwner{{
			UserID: "owner", ProfileEmail: "owner@example.com", Measurement: sessiontranscript.Measurement{InputBytes: &count},
		}}}
		base := Compute(in)
		baseJSON, err := json.Marshal(base)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(base.Hash, legacy.Hash) || !bytes.Equal(baseJSON, legacyJSON) {
			t.Fatal("transcript cache changed the legacy R2 output")
		}
		worker := ComputeWithTimelineAndCommits(in)
		if worker.Session.Detail.TranscriptIO == nil || *worker.Session.Detail.TranscriptIO.Owners[0].Measurement.InputBytes != count {
			t.Fatal("worker omitted the measurement from its published detail")
		}
		if previous != nil && (bytes.Equal(previous.Hash, worker.Hash) || bytes.Equal(previous.TimelineHash, worker.TimelineHash)) {
			t.Fatal("changed transcript counters did not change the worker hashes")
		}
		previous = &worker
	}
}

func p4Call(uuid, parent string, sec int, id, name string, input any) ClaudeRow {
	body, err := json.Marshal([]map[string]any{{"t": "tool_use", "id": id, "n": name, "in": input}})
	if err != nil {
		panic(err)
	}
	return call(uuid, parent, sec, id, name, blocks(string(body)))
}

func p4Result(uuid, parent string, sec int, id, result string, failed bool) ClaudeRow {
	body, err := json.Marshal([]map[string]any{{"t": "tool_result", "tu": id, "tk": result, "err": failed}})
	if err != nil {
		panic(err)
	}
	return resultRow(uuid, parent, sec, string(body))
}

func resultRow(uuid, parent string, sec int, body string) ClaudeRow {
	return newRow("user", uuid, parent, sec, blocks(body))
}

func TestP4_TaskCreateAndUpdateAttachToTurn(t *testing.T) {
	f := session(
		typed("u", "", 0, "p", "implement work"),
		p4Call("create", "u", 1, "create-call", "TaskCreate", map[string]any{"subject": "Implement facts", "description": "Persist task actions\nMore detail"}),
		p4Result("create-result", "create", 2, "create-call", "Task #7 created", false),
		p4Call("update", "create-result", 3, "update-call", "TaskUpdate", map[string]any{"taskId": "7", "status": "completed"}),
		p4Result("update-result", "update", 4, "update-call", "", false),
	).in
	for i := range f.Rows {
		f.Rows[i].SourceFile = "session.jsonl"
	}

	got := ComputeWithTimelineAndCommits(f)
	if got.Timeline.State != "observed" || got.Tasks.State != "observed" || len(got.Tasks.Items) != 1 {
		t.Fatalf("timeline/tasks = %q/%+v", got.Timeline.State, got.Tasks)
	}
	if got.Session.RulesVersion != RulesVersion {
		t.Fatalf("P4 rules version=%d, want %d", got.Session.RulesVersion, RulesVersion)
	}
	item := got.Tasks.Items[0]
	if item.Kind != "task" || item.ID != "7" || item.Title != "Implement facts" || item.Summary != "Persist task actions" || !item.Actions.Added || !item.Actions.Completed || len(item.Touches) != 2 {
		t.Fatalf("task = %+v", item)
	}
	for _, touch := range item.Touches {
		if touch.TurnKey == "" {
			t.Fatalf("task action has no turn: %+v", item.Touches)
		}
	}
	if got.Hash == nil || !reflect.DeepEqual(got.Hash, HashOutputWithCommitsAndTasks(got.Output, got.Commits, got.Tasks)) ||
		!reflect.DeepEqual(got.TimelineHash, HashOutputWithTimelineAndCommits(got)) {
		t.Fatal("fact or timeline hash omits the P4 facts")
	}
}

func TestP4_TodoWriteUsesStableContentIdentityAndStateChanges(t *testing.T) {
	f := session(
		typed("u", "", 0, "p", "do todos"),
		p4Call("todo1", "u", 1, "todo-1", "TodoWrite", map[string]any{"todos": []map[string]string{{"content": "first", "status": "pending"}}}),
		p4Result("todo-r1", "todo1", 2, "todo-1", "", false),
		p4Call("todo2", "todo-r1", 3, "todo-2", "TodoWrite", map[string]any{"todos": []map[string]string{{"content": "first", "status": "completed"}, {"content": "second", "status": "pending"}}}),
		p4Result("todo-r2", "todo2", 4, "todo-2", "", false),
	).in
	for i := range f.Rows {
		f.Rows[i].SourceFile = "session.jsonl"
	}

	got := ComputeWithTimelineAndCommits(f).Tasks
	if got.State != "observed" || len(got.Items) != 2 {
		t.Fatalf("tasks = %+v", got)
	}
	if got.Items[0].ID != "todo-"+contentTaskID("first") || !got.Items[0].Actions.Added || !got.Items[0].Actions.Completed {
		t.Fatalf("first todo = %+v", got.Items[0])
	}
	if got.Items[1].ID != "todo-"+contentTaskID("second") || !got.Items[1].Actions.Added {
		t.Fatalf("second todo = %+v", got.Items[1])
	}
}

func TestP4_GitHubActionsAndCommitReferencesAreRepositoryScoped(t *testing.T) {
	f := session(
		typed("u", "", 0, "p", "fix issue"),
		p4Call("create", "u", 1, "create-issue", "Bash", map[string]any{"gc": "gh issue create --title 'Fix #12'"}),
		p4Result("created", "create", 2, "create-issue", "https://github.com/acme/app/issues/12", false),
		p4Call("close", "created", 3, "close-issue", "Bash", map[string]any{"gc": "gh issue close 12 -R acme/app"}),
		p4Result("closed", "close", 4, "close-issue", "Closed issue #12", false),
		p4Call("commit", "closed", 5, "commit-call", "Bash", map[string]any{"gc": "git commit -m 'fix #12'"}),
		p4Result("commit-result", "commit", 6, "commit-call", "[main aaaaaaa] fix #12", false),
	).in
	for i := range f.Rows {
		f.Rows[i].SourceFile = "session.jsonl"
	}
	f.Snapshots = []Snapshot{{UserID: "user-1", ProfileEmail: "dev@example.com", LoginEmail: "dev@example.com", BillingProvider: "anthropic",
		Commits: &SnapshotCommits{Trees: []CommitTree{{Repository: "github.com/acme/app", Branch: "main"}}, Items: []SnapshotCommit{{Hash: commitHashA, At: at(6).Unix(), Kind: "commit", Call: "commit-call", Transcript: "session.jsonl", Proof: "output_hash", Subject: strptr("fix #12")}}}}}
	f.CommitClaims = []CommitClaim{{Repository: "github.com/acme/app", Hash: commitHashA}}
	f.CommitResults = []CommitResult{{SrecID: 3, SourceFile: "session.jsonl", Call: "create-issue", Text: "https://github.com/acme/app/issues/12"},
		{SrecID: 5, SourceFile: "session.jsonl", Call: "close-issue", Text: "Closed issue #12"},
		{SrecID: 7, SourceFile: "session.jsonl", Call: "commit-call", Text: "[main aaaaaaa] fix #12"}}

	got := ComputeWithTimelineAndCommits(f)
	if len(got.Tasks.Items) != 1 {
		t.Fatalf("tasks = %+v", got.Tasks)
	}
	issue := got.Tasks.Items[0]
	if issue.Kind != "issue" || issue.ID != "acme/app#12" || issue.URL != "https://github.com/acme/app/issues/12" || !issue.Actions.Added || !issue.Actions.Completed || !issue.Actions.CommitLinked {
		t.Fatalf("issue = %+v", issue)
	}
	if len(got.Commits.Items) != 1 || !reflect.DeepEqual(got.Commits.Items[0].Refs, []string{issue.Key}) {
		t.Fatalf("commit refs = %+v", got.Commits.Items)
	}
}

func TestP4_StructuredPRActionSurvivesHiddenBashInput(t *testing.T) {
	f := session(
		typed("u", "", 0, "p", "create a pull request"),
		call("create", "u", 1, "pr-call", "Bash", blocks(`[{"t":"tool_use","id":"pr-call","n":"Bash","in":{"hid":true}}]`)),
		result("created", "create", 2, "pr-call", func() opt {
			return func(r *ClaudeRow) {
				r.ToolUseResult = json.RawMessage(`{"gitOperation":{"pr":{"action":"created","number":34,"url":"https://github.com/acme/app/pull/34"}}}`)
			}
		}(), blocks(`[{"t":"tool_result","tu":"pr-call","err":false}]`)),
	).in
	for i := range f.Rows {
		f.Rows[i].SourceFile = "session.jsonl"
	}
	got := ComputeWithTimelineAndCommits(f).Tasks
	if got.State != "undeterminable" || len(got.Items) != 1 || got.Items[0].Kind != "pr" || got.Items[0].ID != "acme/app#34" || !got.Items[0].Actions.Added || got.Items[0].URL != "https://github.com/acme/app/pull/34" {
		t.Fatalf("structured PR fact=%+v", got)
	}
}

func TestP4_CodexShellCallProducesCommitAndPRFactsInRootTurn(t *testing.T) {
	in := codexSession().in
	in.Snapshots = []Snapshot{{UserID: "user-1", ProfileEmail: "dev@example.com", BillingProvider: "openai", AccountID: "acct-1",
		CodexTurns: []SnapshotCodexTurn{{TurnID: "turn-1", RootTurnID: "turn-1", StartedAt: at(5), EndedAt: at(25), End: "complete"}}}}
	in.CodexRows = append(in.CodexRows,
		CodexRow{ID: 4, TS: at(12), RecordType: "tool_call", ToolName: "exec_command", ToolCallID: "commit-call", ToolArgs: `{"cmd":"git commit -m 'ship #34'","workdir":"/repo"}`, TurnID: "turn-1", RootTurnID: "turn-1", SourceFile: "rollout.jsonl", CWD: "/repo", RepositoryID: "github.com/acme/app", UserID: "user-1", ProfileEmail: "dev@example.com", BillingProvider: "openai", AccountID: "acct-1"},
		CodexRow{ID: 5, TS: at(13), RecordType: "tool_output", ToolCallID: "commit-call", ToolOutput: "[main abcdef1] ship #34", TurnID: "turn-1", RootTurnID: "turn-1", SourceFile: "rollout.jsonl", UserID: "user-1", ProfileEmail: "dev@example.com", BillingProvider: "openai", AccountID: "acct-1"},
		CodexRow{ID: 6, TS: at(14), RecordType: "tool_call", ToolName: "exec_command", ToolCallID: "pr-call", ToolArgs: `{"cmd":"gh pr create --title 'release'"}`, TurnID: "turn-1", RootTurnID: "turn-1", SourceFile: "rollout.jsonl", CWD: "/repo", RepositoryID: "github.com/acme/app", UserID: "user-1", ProfileEmail: "dev@example.com", BillingProvider: "openai", AccountID: "acct-1"},
		CodexRow{ID: 7, TS: at(15), RecordType: "tool_output", ToolCallID: "pr-call", ToolOutput: "https://github.com/acme/app/pull/34", TurnID: "turn-1", RootTurnID: "turn-1", SourceFile: "rollout.jsonl", UserID: "user-1", ProfileEmail: "dev@example.com", BillingProvider: "openai", AccountID: "acct-1"},
	)
	got := ComputeWithTimelineAndCommits(in)
	if got.Commits.State != "observed" || len(got.Commits.Items) != 1 || got.Commits.Items[0].Source != "output" || got.Commits.Items[0].TurnKey != "turn-1" || got.Commits.Items[0].Short != "abcdef1" {
		t.Fatalf("Codex commits = %+v", got.Commits)
	}
	if got.Tasks.State != "observed" || len(got.Tasks.Items) != 1 || got.Tasks.Items[0].Kind != "pr" || got.Tasks.Items[0].ID != "#34" || !got.Tasks.Items[0].Actions.Added {
		t.Fatalf("Codex tasks = %+v", got.Tasks)
	}
}

func strptr(s string) *string { return &s }
