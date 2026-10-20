package sessionfacts

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

var remainingCases = []tcase{
	{name: "context attachment and competing models", in: contextFixture()},
	{name: "codex root and child", in: codexRootFixture()},
	{name: "codex root with earlier legacy input", in: codexMixedFixture()},
	{name: "codex equal snapshot times", in: codexEqualSnapshotFixture()},
	{name: "slim todo statuses", in: todoFixture()},
	{name: "edit area and task type", in: editFixture()},
}

func contextFixture() *fixture {
	f := session(
		typed("u1", "", 0, "P1", "one"),
		attachment("m1", "u1", 1, `{"type":"model","identity":{"modelId":"provider/claude-haiku-4-5[1m]"}}`),
		reply("a1", "m1", 2, model("claude-haiku-4-5")),
		typed("u2", "a1", 10, "P2", "two"),
		attachment("m2", "u2", 11, `{"type":"model","identity":{"modelId":"provider/other[1m]"}}`),
		reply("a2", "m2", 12, model("claude-haiku-4-5")),
	).request("a1", 1).request("a2", 2)
	for i := range f.in.Rows {
		f.in.Rows[i].SourceFile = "main.jsonl"
	}
	for i := range f.in.Heads {
		f.in.Heads[i].SourceFile = "main.jsonl"
	}
	return f
}
func codexRootFixture() *fixture {
	f := codexSession()
	f.in.CodexRows[0].TurnID = "root"
	f.in.CodexRows[1].TurnID = "child"
	f.in.CodexRows[1].RootTurnID = "root"
	f.in.CodexRows[2].TurnID = "root"
	f.in.CodexRows[2].RootTurnID = "root"
	f.in.ReadAt = at(20).Add(25 * time.Hour)
	f.in.Snapshots = []Snapshot{{ObservedAt: at(20), CodexTurns: []SnapshotCodexTurn{{TurnID: "root", StartedAt: at(0)}}}}
	return f
}
func codexMixedFixture() *fixture {
	f := codexSession()
	f.in.CodexRows = append(f.in.CodexRows, CodexRow{ID: 4, TS: at(-10), RecordType: "user", Role: "user", TextHead: "older legacy input", Entrypoint: "cli"})
	return f
}
func codexEqualSnapshotFixture() *fixture {
	f := codexSession()
	f.in.Snapshots = []Snapshot{
		{ObservedAt: at(30), CodexTurns: []SnapshotCodexTurn{{TurnID: "turn-1", StartedAt: at(0), End: "complete", ContextWindow: intPointer(100000)}}},
		{ObservedAt: at(30), CodexTurns: []SnapshotCodexTurn{{TurnID: "turn-1", StartedAt: at(0), End: "aborted", ContextWindow: intPointer(200000)}}},
	}
	return f
}
func todoFixture() *fixture {
	return session(typed("u", "", 0, "P", "work"), call("a", "u", 1, "todos", "TodoWrite", func(r *ClaudeRow) {
		r.Blocks = json.RawMessage(`[{"t":"tool_use","id":"todos","n":"TodoWrite","in":{"sts":["completed","pending","in_progress"]}}]`)
	}))
}
func editFixture() *fixture {
	return session(typed("u", "", 0, "P", "work", func(r *ClaudeRow) { r.TaskType = "bugfix" }), call("a", "u", 1, "edit", "Edit", func(r *ClaudeRow) {
		r.CWD, r.RepoSubpath = "/repo/web", "web"
		r.Blocks = json.RawMessage(`[{"t":"tool_use","id":"edit","n":"Edit","in":{"fp":"/repo/web/app/main.go"}}]`)
	})).request("a", 1)
}

func TestContextByRequestTime(t *testing.T) {
	f := contextFixture()
	out := Compute(f.in)
	if len(out.Turns) != 2 || out.Turns[0].ContextSource != "observed" || out.Turns[1].ContextSource != "model_default" || out.Turns[0].ContextWindow == nil || *out.Turns[0].ContextWindow != 1000000 || out.Turns[1].ContextWindow == nil || *out.Turns[1].ContextWindow != 200000 {
		t.Fatalf("context by time: %+v", out.Turns)
	}
	f.in.Rows = append(f.in.Rows, attachment("future", "a2", 30, `{"type":"model","identity":{"modelId":"provider/claude-haiku-4-5[1m]"}}`))
	later := Compute(f.in)
	if later.Turns[0].ContextSource != out.Turns[0].ContextSource || later.Turns[1].ContextSource != out.Turns[1].ContextSource || *later.Turns[1].ContextWindow != *out.Turns[1].ContextWindow {
		t.Fatalf("future changed past context: %+v", later.Turns)
	}
}

func TestContextLastAttachmentInSameFile(t *testing.T) {
	f := session(
		typed("u1", "", 0, "P1", "first"),
		attachment("model1", "u1", 1, `{"type":"model","identity":{"modelId":"bedrock/region/claude-haiku-4-5[1m]"}}`),
		reply("a1", "model1", 2, model("claude-haiku-4-5")),
		typed("u2", "a1", 10, "P2", "second"),
		attachment("model2", "u2", 11, `{"type":"model","identity":{"modelId":"bedrock/region/claude-haiku-4-5"}}`),
		reply("a2", "model2", 12, model("claude-haiku-4-5")),
	).request("a1", 1).request("a2", 2)
	f.in.Heads[0].InputTokens = 300000
	for i := range f.in.Rows {
		f.in.Rows[i].SourceFile = "main.jsonl"
	}
	for i := range f.in.Heads {
		f.in.Heads[i].SourceFile = "main.jsonl"
	}
	got := Compute(f.in)
	if len(got.Turns) != 2 || got.Turns[0].ContextWindow == nil || *got.Turns[0].ContextWindow != 1000000 || got.Turns[0].ContextSource != "observed" || got.Turns[1].ContextWindow == nil || *got.Turns[1].ContextWindow != 200000 || got.Turns[1].ContextSource != "model_default" {
		t.Fatalf("provider prefix and last attachment: %+v", got.Turns)
	}
	f.in.Rows = append(f.in.Rows, attachment("other-file", "a2", 20, `{"type":"model","identity":{"modelId":"bedrock/region/claude-haiku-4-5[1m]"}}`))
	f.in.Rows[len(f.in.Rows)-1].SourceFile = "different.jsonl"
	got = Compute(f.in)
	if got.Turns[1].ContextWindow == nil || *got.Turns[1].ContextWindow != 200000 {
		t.Fatalf("other file changed prior request: %+v", got.Turns)
	}
}

func TestContextUsesHeadLineSourceFile(t *testing.T) {
	f := session(
		typed("u", "", 0, "P", "work"),
		attachment("model", "u", 1, `{"type":"model","identity":{"modelId":"provider/claude-haiku-4-5[1m]"}}`),
		reply("a", "model", 2, model("claude-haiku-4-5")),
	).request("a", 2)
	for i := range f.in.Rows {
		f.in.Rows[i].SourceFile = "main.jsonl"
	}
	f.in.Heads[0].SourceFile = ""
	f.in.Heads[0].InputTokens = 300000
	got := Compute(f.in)
	if len(got.Turns) != 1 || got.Turns[0].ContextWindow == nil || *got.Turns[0].ContextWindow != 1000000 || got.Turns[0].ContextSource != "observed" {
		t.Fatalf("head projection missed source file: %+v", got.Turns)
	}
	f.in.Rows[1].SourceFile = "other.jsonl"
	got = Compute(f.in)
	if got.Turns[0].ContextWindow != nil || got.Turns[0].ContextSource != "unknown" || got.Turns[0].ContextTokens == nil || *got.Turns[0].ContextTokens != 302010 {
		t.Fatalf("unrelated file must not set a context window: %+v", got.Turns)
	}
	f.in.Rows[1].SourceFile = ""
	f.in.Rows[2].SourceFile = ""
	got = Compute(f.in)
	if got.Turns[0].ContextWindow != nil || got.Turns[0].ContextSource != "unknown" {
		t.Fatalf("missing file identity must not assert observed context: %+v", got.Turns)
	}
	f.in.Heads[0].SourceFile = "main.jsonl"
	got = Compute(f.in)
	if got.Turns[0].ContextWindow != nil || got.Turns[0].ContextSource != "unknown" {
		t.Fatalf("head file without matching row file must not assert observed context: %+v", got.Turns)
	}
}

func TestContextWindowAboveDefaultAndInputMix(t *testing.T) {
	f := session(typed("u", "", 0, "P", "work"), reply("a", "u", 1, model("claude-haiku-4-5"))).request("a", 1)
	f.in.Heads[0].InputTokens = 201000
	got := Compute(f.in)
	if got.Turns[0].ContextWindow != nil || got.Turns[0].ContextSource != "unknown" || !got.Session.LargeWindowSeen || got.Session.Detail.ContextMix.Input != 201000 {
		t.Fatalf("overflow and mix: %+v", got)
	}
}

func TestCodexRootLifecycle(t *testing.T) {
	f := codexRootFixture()
	out := Compute(f.in)
	if len(out.Turns) != 1 || out.Turns[0].TurnKey != "root" || out.Turns[0].APIRequests != 2 || out.Turns[0].Status != "undeterminable" || out.Turns[0].StatusReason != "no_end_line" {
		t.Fatalf("root and no end: %+v", out.Turns)
	}
	f.in.ReadAt = at(20).Add(time.Hour)
	out = Compute(f.in)
	if out.Turns[0].Status != "open" {
		t.Fatalf("fresh unfinished root: %+v", out.Turns[0])
	}
	f.in.Snapshots[0].CodexTurns[0].End = "aborted"
	out = Compute(f.in)
	if out.Turns[0].Status != "interrupted" {
		t.Fatalf("aborted root: %+v", out.Turns[0])
	}
}

func TestCodexLifecycleStartExcludesEarlierUser(t *testing.T) {
	f := codexSession()
	f.in.CodexRows = append(f.in.CodexRows, CodexRow{ID: 4, TS: at(12), RecordType: "user", Role: "user", TextHead: "inside root"})
	f.in.Snapshots = []Snapshot{{ObservedAt: at(30), CodexTurns: []SnapshotCodexTurn{{TurnID: "turn-1", StartedAt: at(10)}}}}
	out := Compute(f.in)
	if len(out.Turns) != 2 || out.Turns[0].TurnKey != "cx:"+at(0).UTC().Format(time.RFC3339Nano) || len(out.Turns[0].Detail.Inputs) != 1 || len(out.Turns[1].Detail.Inputs) != 1 || out.Turns[1].Detail.Inputs[0].TS != at(12) || out.Turns[1].Preview == nil || *out.Turns[1].Preview != "inside root" || counts(out.Session) != "2/2/2/0" {
		t.Fatalf("lifecycle and earlier legacy turn: %+v", out)
	}
	if out.Turns[1].Status != "undeterminable" || out.Turns[1].StatusReason != "no_read_time" {
		t.Fatalf("read time absent: %+v", out.Turns[1])
	}
}

func TestCodexLegacyInputAfterCompletedRoot(t *testing.T) {
	f := codexSession()
	f.in.Snapshots = []Snapshot{{ObservedAt: at(30), CodexTurns: []SnapshotCodexTurn{{TurnID: "turn-1", StartedAt: at(0), EndedAt: at(21), End: "complete"}}}}
	f.in.CodexRows = append(f.in.CodexRows, CodexRow{ID: 4, TS: at(25), RecordType: "user", Role: "user", TextHead: "late legacy input", Entrypoint: "cli"})
	got := Compute(f.in)
	if len(got.Turns) != 2 || got.Turns[0].TurnKey != "turn-1" || got.Turns[1].TurnKey != "cx:"+at(25).UTC().Format(time.RFC3339Nano) || len(got.Turns[0].Detail.Inputs) != 1 || len(got.Turns[1].Detail.Inputs) != 1 || counts(got.Session) != "2/2/2/0" {
		t.Fatalf("legacy input after completed root: %+v", got)
	}
}

func TestCodexWithoutLifecycleKeepsEarlierLegacyUser(t *testing.T) {
	f := codexMixedFixture()
	got := Compute(f.in)
	if len(got.Turns) != 2 || got.Turns[0].TurnKey != "cx:"+at(-10).UTC().Format(time.RFC3339Nano) || len(got.Turns[0].Detail.Inputs) != 1 || got.Turns[0].Preview == nil || *got.Turns[0].Preview != "older legacy input" || got.Turns[1].TurnKey != "turn-1" || len(got.Turns[1].Detail.Inputs) != 1 || got.Turns[1].Preview == nil || *got.Turns[1].Preview != "add a test" || got.Turns[1].APIRequests != 2 || counts(got.Session) != "2/2/2/0" {
		t.Fatalf("earlier legacy input and inferred root opener: %+v", got)
	}
}

func TestCodexSnapshotChildUsageStaysWithRoot(t *testing.T) {
	f := codexSession()
	f.in.CodexRows[1].TurnID, f.in.CodexRows[1].RootTurnID = "child", ""
	f.in.Snapshots = []Snapshot{{ObservedAt: at(30), CodexTurns: []SnapshotCodexTurn{
		{TurnID: "root", StartedAt: at(0), End: "complete"},
		{TurnID: "child", RootTurnID: "root", StartedAt: at(5), End: "complete"},
	}}}
	f.in.CodexRows[2].TurnID, f.in.CodexRows[2].RootTurnID = "root", "root"
	out := Compute(f.in)
	if len(out.Turns) != 1 || out.Turns[0].TurnKey != "root" || out.Turns[0].APIRequests != 2 || out.Turns[0].Status != "ended" {
		t.Fatalf("child lifecycle must not create root: %+v", out.Turns)
	}
}

func TestCodexCommandNeedsAssistantResponse(t *testing.T) {
	f := codexSession()
	f.in.CodexRows[0].CommandName = "review"
	f.in.CodexRows[0].TextHead = "<skill name=review>"
	if got := Compute(f.in); len(got.Turns) != 1 || got.Turns[0].IsRequest {
		t.Fatalf("usage is not assistant response: %+v", got.Turns)
	}
	f.in.CodexRows = append(f.in.CodexRows, CodexRow{ID: 4, TS: at(15), RecordType: "assistant", Role: "assistant"})
	if got := Compute(f.in); len(got.Turns) != 1 || !got.Turns[0].IsRequest {
		t.Fatalf("assistant response opens request: %+v", got.Turns)
	}
	f.in.CodexRows[3].Role, f.in.CodexRows[3].RawType = "", "message"
	if got := Compute(f.in); len(got.Turns) != 1 || !got.Turns[0].IsRequest {
		t.Fatalf("legacy assistant response opens request: %+v", got.Turns)
	}
	f.in.CodexRows[3].RawType = "response_item"
	if got := Compute(f.in); len(got.Turns) != 1 || !got.Turns[0].IsRequest {
		t.Fatalf("response_item assistant with slim role opens request: %+v", got.Turns)
	}
}

func TestCodexLegacyInputsStayInTheirOwnTurns(t *testing.T) {
	f := &fixture{in: Input{Agent: "codex", CodexRows: []CodexRow{
		{ID: 1, TS: at(0), RecordType: "user", TextHead: "first", Entrypoint: "cli"},
		{ID: 2, TS: at(10), RecordType: "user", TextHead: "second", Entrypoint: "cli"},
	}}}
	out := Compute(f.in)
	if len(out.Turns) != 2 || len(out.Turns[0].Detail.Inputs) != 1 || len(out.Turns[1].Detail.Inputs) != 1 || counts(out.Session) != "2/2/2/0" {
		t.Fatalf("legacy turns: %+v", out)
	}
}

func TestBackgroundNotificationAndMissingWorkflow(t *testing.T) {
	f := session(
		typed("u", "", 0, "P", "work"),
		result("bg", "u", 1, "bash", func(r *ClaudeRow) { r.ToolUseResult = json.RawMessage(`{"backgroundTaskId":"job-1"}`) }),
		system("duration", "bg", 2, "turn_duration", func(r *ClaudeRow) {
			r.PendingBackgroundAgentCount = json.RawMessage(`1`)
			r.PendingWorkflowCount = json.RawMessage(`0`)
		}),
	)
	got := Compute(f.in).Session.Detail
	if got.Signals.Background.Value == nil || *got.Signals.Background.Value != 2 || got.SubagentsRunning == nil || *got.SubagentsRunning != 1 {
		t.Fatalf("pending: %+v", got)
	}
	f.in.Rows = append(f.in.Rows, user("notification", "duration", 3, "<task-notification><task-id>job-1</task-id></task-notification>"))
	got = Compute(f.in).Session.Detail
	if got.Signals.Background.Value == nil || *got.Signals.Background.Value != 1 {
		t.Fatalf("completed: %+v", got)
	}
	f.in.Rows[2].PendingWorkflowCount = nil
	f.in.Rows[2].CctraceCollect = nil
	got = Compute(f.in).Session.Detail
	if got.Signals.Background.State != "undeterminable" || got.Signals.Background.Reason != "pre_collect" || got.SubagentsRunning == nil || *got.SubagentsRunning != 1 {
		t.Fatalf("missing workflow: %+v", got)
	}
}

func TestPlanAttachmentRecordsExactSourceID(t *testing.T) {
	f := session(
		typed("u", "", 0, "P", "work"),
		call("plan", "u", 1, "p", "ExitPlanMode"),
		attachment("file", "plan", 2, `{"type":"plan_file_reference","planFilePath":"/p.md","planContent":"# Plan"}`),
	)
	got := Compute(f.in).Session.Detail.Plans
	if len(got) != 1 || got[0].SrecID != 2 || got[0].AttachmentSrecID != 3 || got[0].FilePath != "/p.md" {
		t.Fatalf("plan attachment source=%+v", got)
	}
}

func TestPlanAttachmentRejectsAmbiguousParents(t *testing.T) {
	tests := []struct {
		name   string
		rows   []ClaudeRow
		linked []int64
		paths  []string
	}{
		{"branch attachment after another plan", []ClaudeRow{
			typed("u", "", 0, "P", "work"),
			call("a", "u", 1, "p-a", "ExitPlanMode"),
			call("b", "a", 2, "p-b", "ExitPlanMode"),
			attachment("file-a", "a", 3, `{"type":"plan_file_reference","planFilePath":"/a.md","planContent":"A"}`),
		}, []int64{4, 0}, []string{"/a.md", ""}},
		{"unrelated parent", []ClaudeRow{
			typed("u", "", 0, "P", "work"),
			call("a", "u", 1, "p-a", "ExitPlanMode"),
			attachment("other", "u", 2, `{"type":"plan_file_reference","planFilePath":"/other.md","planContent":"Other"}`),
		}, []int64{0}, []string{""}},
		{"two plan tools on parent", []ClaudeRow{
			typed("u", "", 0, "P", "work"),
			call("a", "u", 1, "p-a", "ExitPlanMode", func(r *ClaudeRow) {
				r.Blocks = json.RawMessage(`[{"t":"tool_use","id":"p-a","n":"ExitPlanMode"},{"t":"tool_use","id":"p-b","n":"ExitPlanMode"}]`)
			}),
			attachment("file", "a", 2, `{"type":"plan_file_reference","planFilePath":"/x.md","planContent":"Ambiguous"}`),
		}, []int64{0, 0}, []string{"", ""}},
		{"two attachments on parent", []ClaudeRow{
			typed("u", "", 0, "P", "work"),
			call("a", "u", 1, "p-a", "ExitPlanMode"),
			attachment("first", "a", 2, `{"type":"plan_file_reference","planFilePath":"/first.md","planContent":"First"}`),
			attachment("second", "a", 3, `{"type":"plan_file_reference","planFilePath":"/second.md","planContent":"Second"}`),
		}, []int64{0}, []string{""}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Compute(session(tc.rows...).in).Session.Detail.Plans
			if len(got) != len(tc.linked) {
				t.Fatalf("plans=%+v", got)
			}
			for i := range got {
				if got[i].AttachmentSrecID != tc.linked[i] || got[i].FilePath != tc.paths[i] {
					t.Fatalf("plan %d=%+v", i, got[i])
				}
			}
		})
	}
}

func TestRelativeEditFromNestedCWD(t *testing.T) {
	f := editFixture()
	f.in.Rows[1].Blocks = json.RawMessage(`[{"t":"tool_use","id":"edit","n":"Edit","in":{"fp":"app/main.go"}}]`)
	got := Compute(f.in)
	if got.Session.Detail.EditedFiles.Total != 1 || len(got.Session.Detail.EditedFiles.Items) != 1 || got.Session.Detail.EditedFiles.Items[0].Path != "web/app/main.go" || got.Session.Detail.ChangeArea.Value != "web/app" {
		t.Fatalf("relative edit must be repository-relative: %+v", got.Session.Detail)
	}
	f.in.Rows[1].Blocks = json.RawMessage(`[{"t":"tool_use","id":"edit","n":"Edit","in":{"fp":"../../outside.go"}}]`)
	got = Compute(f.in)
	if got.Session.Detail.EditedFiles.Total != 0 {
		t.Fatalf("outside repository path must be excluded: %+v", got.Session.Detail.EditedFiles)
	}
}

func TestAbandonedRedactionDoesNotHideActiveChangeArea(t *testing.T) {
	f := session(
		typed("u", "", 0, "P", "work"),
		reply("a", "u", 1),
		reply("abandoned", "a", 2, func(r *ClaudeRow) { r.ContentKind, r.Blocks, r.RawRedacted = "", nil, redactedMarker }),
		call("edit", "a", 3, "edit", "Edit", func(r *ClaudeRow) {
			r.CWD, r.RepoSubpath = "/repo/web", "web"
			r.Blocks = json.RawMessage(`[{"t":"tool_use","id":"edit","n":"Edit","in":{"fp":"/repo/web/app.go"}}]`)
		}),
	).request("edit", 1)
	got := Compute(f.in).Session.Detail
	if got.EditedFiles.State != "redacted" || got.ChangeArea.State != "observed" || got.ChangeArea.Value != "web/app.go" {
		t.Fatalf("abandoned redaction affects file completeness, not active change area: %+v", got)
	}
	f.in.Rows[3].ContentKind, f.in.Rows[3].Blocks, f.in.Rows[3].RawRedacted = "", nil, redactedMarker
	got = Compute(f.in).Session.Detail
	if got.ChangeArea.State != "undeterminable" || got.ChangeArea.Reason != "redacted" {
		t.Fatalf("active redaction hides change area: %+v", got.ChangeArea)
	}
}

func TestEditedFileAreaAndTaskType(t *testing.T) {
	f := editFixture()
	got := Compute(f.in)
	if got.Session.Detail.ChangeArea.Value != "web/app" || got.Session.Detail.EditedFiles.Total != 1 || got.Session.Detail.EditedFiles.Items[0].Path != "web/app/main.go" || got.Turns[0].Detail.Edited.Count != 1 || got.Session.Detail.TaskType.Value != "bugfix" || got.Session.Detail.ToolCalls != 1 {
		t.Fatalf("edit facts: %+v, %+v", got.Session.Detail, got.Turns[0].Detail)
	}
}

func TestTodoWriteProjectedStatuses(t *testing.T) {
	f := todoFixture()
	got := Compute(f.in).Session.Detail.Signals.Todo
	if got.State != "observed" || got.Value == nil || *got.Value != 2 {
		t.Fatalf("todo statuses: %+v", got)
	}
}

func TestCodexCopiedUsageExcludesPrefixUser(t *testing.T) {
	f := codexSession()
	f.in.CopyGate, f.in.CopiedIDs = true, []int64{2}
	got := Compute(f.in)
	if len(got.Turns) != 1 || len(got.Turns[0].Detail.Inputs) != 0 || got.Turns[0].IsRequest || counts(got.Session) != "1/0/0/0" || got.Turns[0].APIRequests != 1 || got.Turns[0].InputTokens != 2000 || got.Session.CostUSD == nil || *got.Session.CostUSD != 0.25 {
		t.Fatalf("copied usage prefix must exclude human input without changing remaining usage: %+v", got)
	}
}

func TestCodexCopiedUserAndSnapshotOrder(t *testing.T) {
	f := codexSession()
	f.in.CopyGate, f.in.CopiedIDs = true, []int64{1}
	f.in.Snapshots = []Snapshot{
		{ObservedAt: at(50), CodexTurns: []SnapshotCodexTurn{{TurnID: "turn-1", StartedAt: at(0), ContextWindow: intPointer(100000)}}},
		{ObservedAt: at(40), CodexTurns: []SnapshotCodexTurn{{TurnID: "turn-1", StartedAt: at(0), ContextWindow: intPointer(200000)}}},
	}
	out := Compute(f.in)
	if len(out.Turns) != 1 || len(out.Turns[0].Detail.Inputs) != 0 || out.Session.Inputs == nil || *out.Session.Inputs != 0 || out.Turns[0].ContextWindow == nil || *out.Turns[0].ContextWindow != 100000 {
		t.Fatalf("copy and latest window: %+v", out)
	}
}
func intPointer(n int) *int { return &n }

func TestCodexContextMissingAndZeroRemainDistinct(t *testing.T) {
	f := codexSession()
	f.in.CodexRows[1].InputTokens = 0
	f.in.CodexRows[2].InputTokens = 0
	got := Compute(f.in)
	if len(got.Turns) != 1 || got.Turns[0].ContextTokens == nil || *got.Turns[0].ContextTokens != 0 || got.Turns[0].ContextWindow != nil || got.Turns[0].ContextSource != "unknown" {
		t.Fatalf("observed zero input with missing window: %+v", got.Turns)
	}
	f.in.Snapshots = []Snapshot{{ObservedAt: at(30), CodexTurns: []SnapshotCodexTurn{{TurnID: "turn-1", StartedAt: at(0), ContextWindow: intPointer(0)}}}}
	got = Compute(f.in)
	if got.Turns[0].ContextTokens == nil || *got.Turns[0].ContextTokens != 0 || got.Turns[0].ContextWindow != nil || got.Turns[0].ContextSource != "unknown" {
		t.Fatalf("invalid zero window is not a valid observed window: %+v", got.Turns)
	}
}

func TestCodexEqualSnapshotTimesDoNotDependOnInputOrder(t *testing.T) {
	f := codexEqualSnapshotFixture()
	first := Compute(f.in)
	f.in.Snapshots[0], f.in.Snapshots[1] = f.in.Snapshots[1], f.in.Snapshots[0]
	second := Compute(f.in)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("equal snapshot times changed output with read order: %+v, %+v", first.Turns, second.Turns)
	}
}

func TestAbandonedRedactionDoesNotHideActiveQuestionCount(t *testing.T) {
	f := session(
		typed("u1", "", 0, "P1", "first"),
		reply("a1", "u1", 1),
		typed("u2", "a1", 2, "P2", "abandoned"),
		reply("a2", "u2", 3, func(r *ClaudeRow) { r.ContentKind, r.Blocks, r.RawRedacted = "", nil, redactedMarker }),
		typed("u3", "a1", 4, "P3", "active"),
		reply("a3", "u3", 5),
	)
	got := Compute(f.in).Session.Detail.Signals.Question
	if got.State != "observed" || got.Value == nil || *got.Value != 0 {
		t.Fatalf("abandoned redaction cannot hide active question count: %+v", got)
	}
	f.in.Rows[5].ContentKind, f.in.Rows[5].Blocks, f.in.Rows[5].RawRedacted = "", nil, redactedMarker
	got = Compute(f.in).Session.Detail.Signals.Question
	if got.State != "undeterminable" || got.Reason != "redacted" || got.Value != nil {
		t.Fatalf("active redaction hides question count: %+v", got)
	}
}

func TestCopiedRedactionDoesNotHideQuestionCount(t *testing.T) {
	f := session(
		typed("u", "", 0, "P", "work"),
		reply("a", "u", 1),
		reply("copy", "a", 2, func(r *ClaudeRow) { r.ContentKind, r.Blocks, r.RawRedacted = "", nil, redactedMarker }),
	).copies("copy")
	got := Compute(f.in).Session.Detail.Signals.Question
	if got.State != "observed" || got.Value == nil || *got.Value != 0 {
		t.Fatalf("copied redaction cannot hide original active question count: %+v", got)
	}
}

func TestPreLineageQuestionNotMislabelledRedacted(t *testing.T) {
	f := session(
		typed("u", "", 0, "P", "work"),
		reply("a", "u", 1),
		user("", "", 2, "clear older input"),
	)
	got := Compute(f.in)
	if got.Session.TurnBoundariesReason != "pre_lineage" || got.Session.Detail.Signals.Question.State != "undeterminable" || got.Session.Detail.Signals.Question.Reason != "pre_lineage" || got.Session.Detail.Signals.Question.Value != nil || got.Session.OpenQuestions != nil {
		t.Fatalf("missing lineage cannot be reported as redaction: %+v", got.Session)
	}
}

func TestQuestionActiveBranchOnly(t *testing.T) {
	f := session(typed("u", "", 0, "P", "work"), call("q", "u", 1, "question", "AskUserQuestion"), reply("a", "u", 2))
	got := Compute(f.in)
	if got.Session.Detail.Signals.Question.State != "observed" || got.Session.Detail.Signals.Question.Value == nil || *got.Session.Detail.Signals.Question.Value != 0 || len(got.Turns[0].Detail.Questions) != 0 {
		t.Fatalf("abandoned question: %+v", got)
	}
}

func TestSignalsQuestionsAndTodo(t *testing.T) {
	f := session(
		typed("u", "", 0, "P", "work"),
		attachment("tasks", "u", 1, `{"type":"task_reminder","content":[{"id":"1","status":"pending"},{"id":"2","status":"completed"}]}`),
		call("a", "tasks", 2, "q1", "AskUserQuestion", func(r *ClaudeRow) {
			r.Blocks = json.RawMessage(`[{"t":"tool_use","id":"q1","n":"AskUserQuestion","in":{"h":["Scope"]}}]`)
		}),
		result("r", "a", 3, "q1", func(r *ClaudeRow) { r.Blocks = json.RawMessage(`[{"t":"tool_result","tu":"q1","err":true}]`) }),
		system("dur", "r", 4, "turn_duration", func(r *ClaudeRow) {
			r.PendingBackgroundAgentCount = json.RawMessage(`0`)
			r.PendingWorkflowCount = json.RawMessage(`0`)
		}),
	)
	out := Compute(f.in)
	if out.Session.Detail.Signals.Todo.Value == nil || *out.Session.Detail.Signals.Todo.Value != 1 || out.Session.Detail.Signals.Question.Value == nil || *out.Session.Detail.Signals.Question.Value != 0 || out.Session.Detail.Signals.Background.Value == nil || *out.Session.Detail.Signals.Background.Value != 0 || len(out.Turns[0].Detail.Questions) != 1 || out.Turns[0].Detail.Questions[0].Status != "rejected" {
		t.Fatalf("signals: %+v, %+v", out.Session.Detail.Signals, out.Turns)
	}
}
