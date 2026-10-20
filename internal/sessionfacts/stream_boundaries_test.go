package sessionfacts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"unsafe"
)

func assertStreamEqual(t *testing.T, in Input) {
	t.Helper()
	want := ComputeWithTimelineAndCommits(in)
	for _, batch := range []int{1, 2, 11} {
		got := streamed(t, in, batch)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("batch=%d %s", batch, firstDifference(reflect.ValueOf(got), reflect.ValueOf(want), "out"))
		}
	}
}
func TestStreamPayloadBoundaries(t *testing.T) {
	t.Parallel()
	in := session(typed("u", "", 0, "p", "implement"), p4Call("create", "u", 1, "create", "TaskCreate", map[string]any{"subject": "work", "description": "description\nbody"}), p4Call("update", "create", 2, "update", "TaskUpdate", map[string]any{"taskId": "7", "status": "completed"}), p4Result("updated", "update", 3, "update", "", false), p4Result("created", "updated", 4, "create", "Task #7 created", false), p4Call("todo", "created", 5, "todo", "TodoWrite", map[string]any{"todos": []map[string]string{{"content": "remember", "status": "pending"}}}), p4Result("todo-result", "todo", 6, "todo", "", false), p4Call("gh", "todo-result", 7, "gh", "Bash", map[string]any{"gc": "gh issue create --title 'issue'"}), p4Result("gh-result", "gh", 8, "gh", "", false), p4Call("q", "gh-result", 9, "question", "AskUserQuestion", map[string]any{"h": []string{"choose"}}), p4Result("answer", "q", 10, "question", "", false), p4Call("plan", "answer", 11, "plan", "ExitPlanMode", map[string]any{"pf": "old.md"}), attachment("plan-file", "plan", 12, `{"type":"plan_file_reference","planFilePath":"new.md"}`), reply("final", "plan-file", 13)).in
	for i := range in.Rows {
		in.Rows[i].SourceFile = "session.jsonl"
	}
	in.CommitResults = []CommitResult{{SrecID: 9, SourceFile: "session.jsonl", Call: "gh", Text: "https://github.com/o/r/issues/12"}}
	assertStreamEqual(t, in)
	rng := rand.New(rand.NewSource(934))
	in.Rows = shuffled(rng, in.Rows)
	assertStreamEqual(t, in)
}
func TestStreamCommitEvidenceAndCodexPayload(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"base", "missing", "duplicate", "truncated", "redacted", "structured", "invalid", "failed", "github"} {
		in := commitFixture()
		switch mode {
		case "missing":
			in.CommitResults = nil
		case "duplicate":
			in.CommitResults = append(in.CommitResults, in.CommitResults[0])
		case "truncated":
			in.CommitResults[0].Truncated = true
		case "redacted":
			in.CommitResults[0].Text = marker
		case "structured":
			in.Rows[2].ToolUseResult = json.RawMessage(`{"gitOperation":{"commit":{"sha":"` + commitHashA + `"}}}`)
			in.CommitResults = nil
		case "invalid":
			in.Rows[2].Blocks = json.RawMessage(`[{"t":"tool_result","tu":"call","err":"bad"}]`)
		case "failed":
			in.Rows[2].Blocks = json.RawMessage(`[{"t":"tool_result","tu":"call","err":true}]`)
		case "github":
			in.Rows[1].Blocks = json.RawMessage(`[{"t":"tool_use","id":"call","n":"Bash","in":{"gc":"gh pr create --title release"}}]`)
			in.CommitResults[0].Text = "https://github.com/o/r/pull/12"
		}
		assertStreamEqual(t, in)
	}
	in := codexSession().in
	in.CodexRows = append(in.CodexRows, CodexRow{ID: 4, TS: at(12), RecordType: "tool_call", ToolName: "exec_command", ToolCallID: "c", ToolArgs: `{"cmd":"git commit -m 'ship #34'; gh pr create --title release","workdir":"/repo"}`, TurnID: "turn-1", RootTurnID: "turn-1", SourceFile: "rollout.jsonl", CWD: "/repo", RepositoryID: "github.com/o/r", UserID: "user-1", ProfileEmail: "dev@example.com", BillingProvider: "openai", AccountID: "acct-1"}, CodexRow{ID: 5, TS: at(13), RecordType: "tool_output", ToolCallID: "c", ToolOutput: "[main abcdef1] ship #34\nhttps://github.com/o/r/pull/34", TurnID: "turn-1", RootTurnID: "turn-1", SourceFile: "rollout.jsonl", UserID: "user-1", ProfileEmail: "dev@example.com", BillingProvider: "openai", AccountID: "acct-1"})
	assertStreamEqual(t, in)
	in.CommitResults = []CommitResult{{SrecID: 5, SourceFile: "rollout.jsonl", Call: "c", Text: "Process exited with code 1"}}
	assertStreamEqual(t, in)
}
func TestStreamLargeSingleTurn(t *testing.T) {
	t.Parallel()
	// A few thousand payload-bearing rows cross the progress and loader boundary
	// without a large text buffer (the race detector charges each byte touched).
	f := session(typed("u", "", 0, "p", "large turn"))
	parent := "u"
	for i := 0; i < 2048; i++ {
		uuid := fmt.Sprintf("a-%04d", i)
		f.in.Rows = append(f.in.Rows, reply(uuid, parent, i+1, blocks(`[{"t":"text"}]`)))
		f.in.Rows[len(f.in.Rows)-1].ID = int64(i + 2)
		parent = uuid
	}
	want := ComputeWithTimelineAndCommits(f.in)
	got := streamed(t, f.in, 1024)
	if !reflect.DeepEqual(got, want) {
		t.Fatal(firstDifference(reflect.ValueOf(got), reflect.ValueOf(want), "out"))
	}
}

type countingContext struct {
	context.Context
	calls int
}

func (c *countingContext) Err() error {
	c.calls++
	if c.calls >= 10 {
		return context.Canceled
	}
	return nil
}
func TestStreamCancellationInsideSeal(t *testing.T) {
	t.Parallel()
	b := NewIndexBuilder(Input{Agent: "claude"}, nil)
	for i := 0; i < 2048; i++ {
		row := reply(fmt.Sprint(i), "", i)
		row.ID = int64(i)
		if err := b.AddClaude(context.Background(), int64(i), row); err != nil {
			t.Fatal(err)
		}
	}
	ctx := &countingContext{Context: context.Background()}
	_, err := b.Seal(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Seal did not check context in long loops: %v (%d checks)", err, ctx.calls)
	}
}
func cloneClaude(r ClaudeRow) ClaudeRow {
	v := reflect.ValueOf(&r).Elem()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if f.Kind() == reflect.Slice {
			f.SetBytes(append([]byte(nil), f.Bytes()...))
		}
	}
	return r
}
func poisonClaude(r *ClaudeRow) {
	v := reflect.ValueOf(r).Elem()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if f.Kind() == reflect.Slice {
			for j := 0; j < f.Len(); j++ {
				f.Index(j).SetUint('x')
			}
		}
	}
	r.TextHead = "borrower replaced text"
	r.UUID = "borrower replaced id"
}
func TestStreamBorrowedPayloadIsReleased(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	in := commitFixture()
	want := ComputeWithTimelineAndCommits(in)
	b := NewIndexBuilder(in, nil)
	for i, row := range in.Rows {
		r := cloneClaude(row)
		if err := b.AddClaude(ctx, int64(i), r); err != nil {
			t.Fatal(err)
		}
		poisonClaude(&r)
	}
	a, err := b.Seal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, turn := range a.c.turns {
		if len(turn.members) > 0 || len(turn.read) > 0 {
			t.Fatal("turn member/read arrays survived Seal")
		}
	}
	for _, r := range in.CommitResults {
		if err := a.AddCommitResult(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	for _, ref := range a.ClaudeOrder() {
		r := cloneClaude(in.Rows[ref.Ordinal])
		if err := a.ApplyClaude(ctx, ref.Ordinal, r); err != nil {
			t.Fatal(err)
		}
		poisonClaude(&r)
		for _, l := range a.c.lines {
			if len(l.Blocks) > 0 || len(l.Attachment) > 0 || len(l.ToolUseResult) > 0 || l.TextHead != "" {
				t.Fatal("raw payload retained")
			}
			for _, block := range l.blocks {
				if len(block.Input) > 0 || len(block.TodoContents) > 0 {
					t.Fatal("decoded payload retained")
				}
			}
		}
	}
	got, err := a.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(firstDifference(reflect.ValueOf(got), reflect.ValueOf(want), "out"))
	}
}

func TestStreamCodexSourceOrderAndDuplicateIDs(t *testing.T) {
	t.Parallel()
	in := codexSession().in
	for i, sec := range []int{20, 12} {
		call := CodexRow{ID: 4, TS: at(sec), RecordType: "tool_call", ToolName: "exec_command", ToolCallID: fmt.Sprint(i), ToolArgs: fmt.Sprintf(`{"cmd":"gh pr edit 34 --title title%d -R o/r"}`, i), SourceFile: "rollout.jsonl", CWD: "/repo", UserID: "user-1", ProfileEmail: "dev@example.com", BillingProvider: "openai", AccountID: "acct-1"}
		result := call
		result.ID = 5
		result.RecordType = "tool_output"
		result.TS = at(sec + 1)
		result.ToolOutput = "https://github.com/o/r/pull/34"
		result.ToolArgs = ""
		in.CodexRows = append(in.CodexRows, call, result)
	}
	assertStreamEqual(t, in)
}
func TestStreamReminderFieldsFollowCallOrder(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"pending-title", "redacted-title", "pending-summary", "earlier-result", "same-row"} {
		f := session(typed("u", "", 0, "p", "tasks"), p4Call("c", "u", 1, "c", "TaskCreate", map[string]any{}), p4Call("edit", "c", 2, "edit", "TaskUpdate", map[string]any{"taskId": "7", "status": "in_progress", "subject": "later title", "description": "later summary"}), p4Result("edited", "edit", 3, "edit", "", false), p4Result("r", "edited", 4, "c", "Task #7 created", false)).in
		title, summary := "earlier title", "earlier summary"
		switch mode {
		case "redacted-title":
			title = marker
		case "pending-summary":
			title = ""
		case "earlier-result":
			f.Rows[4].TS = at(0)
		case "same-row":
			f.Rows = f.Rows[:2]
			f.Rows[1].RecordType = "user"
			f.Rows[1].Blocks = json.RawMessage(`[{"t":"tool_use","id":"c","n":"TaskCreate","in":{}},{"t":"tool_result","tu":"c","tk":"7","err":false}]`)
		}
		r := &f.Rows[len(f.Rows)-1]
		r.Attachment, _ = json.Marshal(map[string]any{"type": "task_reminder", "tasks": []map[string]string{{"id": "7", "subject": title, "description": summary}}})
		for i := range f.Rows {
			f.Rows[i].SourceFile = "session.jsonl"
		}
		assertStreamEqual(t, f)
	}
}
func TestStreamNormalizedReceiptsOwnSmallStrings(t *testing.T) {
	t.Parallel()
	raw := strings.Repeat("padding", 1024) + "\n[main aaaaaaa] subject\nhttps://github.com/o/r/pull/7"
	e := NormalizeCommitResult(CommitResult{Text: raw})
	lo := uintptr(unsafe.Pointer(unsafe.StringData(raw)))
	hi := lo + uintptr(len(raw))
	for _, s := range append(e.Output.Hashes, e.URL) {
		p := uintptr(unsafe.Pointer(unsafe.StringData(s)))
		if p >= lo && p < hi {
			t.Fatal("derived receipt keeps selected text backing storage")
		}
	}
}

func TestStreamGlobalIndexBoundariesAndFloatOrder(t *testing.T) {
	t.Parallel()
	f := session(command("cmd", "", 0, "clear", prompt("shared")), typed("u", "cmd", 1, "shared", "request"), call("q", "u", 2, "question", "AskUserQuestion"), result("r", "q", 3, "question"), system("compact", "", 4, "compact_boundary", func(r *ClaudeRow) { r.LogicalParentUUID = "r" }), reply("old", "compact", 5), reply("branch", "compact", 6), reply("future-leaf", "old", 10)).in
	f.Heads = []Head{{ID: 6, UUID: "old", TS: at(5), CostUSD: 1e16, Imputed: true}, {ID: 7, UUID: "branch", TS: at(6), CostUSD: 1, Imputed: true}, {ID: 8, UUID: "future-leaf", TS: at(10), CostUSD: -1e16, Imputed: true}}
	f.HasOTEL = true
	f.OTEL = []OTELRow{{TS: at(3), PromptID: "shared", CostUSD: 1e16}, {TS: at(4), PromptID: "shared", CostUSD: 1}, {TS: at(5), PromptID: "shared", CostUSD: -1e16}}
	assertStreamEqual(t, f)
	duplicate := f.Rows[2]
	duplicate.ID = 99
	duplicate.ProfileEmail = "another@example.com"
	f.Rows = append(f.Rows, duplicate)
	rng := rand.New(rand.NewSource(934))
	f.Rows = shuffled(rng, f.Rows)
	f.Heads = shuffled(rng, f.Heads)
	f.OTEL = shuffled(rng, f.OTEL)
	assertStreamEqual(t, f)
	for _, agent := range []string{"", "claude", "codex"} {
		assertStreamEqual(t, Input{Agent: agent})
	}
}
func TestStreamCloseAndIncompleteFinish(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	in := commitFixture()
	b := NewIndexBuilder(in, nil)
	for i, r := range in.Rows {
		if err := b.AddClaude(ctx, int64(i), r); err != nil {
			t.Fatal(err)
		}
	}
	a, err := b.Seal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Finish(ctx); err == nil {
		t.Fatal("published incomplete input")
	}
	if err := a.ApplyClaude(ctx, 0, in.Rows[0]); err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyClaude(ctx, 2, in.Rows[2]); err == nil {
		t.Fatal("accepted a source permutation")
	}
	a.Close()
	a.Close()
	if len(a.folds) > 0 {
		t.Fatal("Close retained suspended folds")
	}
	if _, err := a.Finish(ctx); err == nil {
		t.Fatal("published closed computation")
	}
}

func TestStreamCancellationDuringPayloadAndFinish(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"payload", "finish"} {
		ctx, cancel := context.WithCancel(context.Background())
		in := session(typed("u", "", 0, "p", "request"), reply("a", "u", 1, blocks("["+strings.Repeat(`{"t":"text"},`, 2047)+`{"t":"text"}]`))).in
		b := NewIndexBuilder(in, func(p Progress) {
			if p.Phase == phase {
				cancel()
			}
		})
		for i, r := range in.Rows {
			if err := b.AddClaude(context.Background(), int64(i), r); err != nil {
				t.Fatal(err)
			}
		}
		a, err := b.Seal(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var out TimelineCommitOutput
		for _, ref := range a.ClaudeOrder() {
			applyCtx := context.Background()
			if phase == "payload" {
				applyCtx = ctx
			}
			err = a.ApplyClaude(applyCtx, ref.Ordinal, in.Rows[ref.Ordinal])
			if err != nil {
				break
			}
		}
		if phase == "finish" {
			out, err = a.Finish(ctx)
		}
		cancel()
		a.Close()
		if !errors.Is(err, context.Canceled) || out.Hash != nil || out.TimelineHash != nil {
			t.Fatalf("%s cancellation returned partial output: %v", phase, err)
		}
		for _, l := range a.c.lines {
			if l.Blocks != nil || l.ToolUseResult != nil || l.Attachment != nil || l.projection == nil {
				t.Fatalf("%s cancellation kept a borrowed row", phase)
			}
		}
		if len(a.folds) > 0 {
			t.Fatal("cancelled folds were not released")
		}
	}
}
