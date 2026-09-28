package airuntime

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// FakeStep is one scripted action. Exactly one of CallTool, Event or
// WaitForCancel should be set.
type FakeStep struct {
	// CallTool invokes the named tool from RunRequest.Tools with Args, emitting
	// tool_call and tool_result. An unknown name ends Run with ErrUnexpectedTool.
	CallTool string
	Args     json.RawMessage
	// Event is emitted as-is.
	Event *Event
	// WaitForCancel blocks until ctx is done, then Run returns ErrCanceled.
	WaitForCancel bool
}

// FakeRuntime replays a script. It lives outside _test.go so service and
// handler tests in other packages can use it.
type FakeRuntime struct {
	InfoValue   Info
	StatusValue Status
	Steps       []FakeStep
	FinalText   string
	Usage       Usage
	// ResultModel is Result.Model on success.
	ResultModel string
	// Err, when set, is returned after the steps instead of a Result.
	Err error
	// ModelsValue and ModelsErr are what Models returns.
	ModelsValue []Model
	ModelsErr   error

	mu       sync.Mutex
	requests []RunRequest
}

var (
	_ Runtime      = (*FakeRuntime)(nil)
	_ ModelCatalog = (*FakeRuntime)(nil)
)

func (f *FakeRuntime) Models(context.Context) ([]Model, error) { return f.ModelsValue, f.ModelsErr }

func (f *FakeRuntime) Info() Info { return f.InfoValue }

func (f *FakeRuntime) Status(context.Context) Status { return f.StatusValue }

// Requests returns every RunRequest received so far.
func (f *FakeRuntime) Requests() []RunRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]RunRequest(nil), f.requests...)
}

func (f *FakeRuntime) Run(ctx context.Context, req RunRequest, sink func(Event)) (*Result, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()

	seq := 0
	for _, step := range f.Steps {
		if ctx.Err() != nil {
			return nil, ErrCanceled
		}
		switch {
		case step.WaitForCancel:
			<-ctx.Done()
			return nil, ErrCanceled
		case step.Event != nil:
			sink(*step.Event)
		case step.CallTool != "":
			tool, ok := findTool(req.Tools, step.CallTool)
			if !ok {
				return nil, fmt.Errorf("%w: %s", ErrUnexpectedTool, step.CallTool)
			}
			seq++
			var args map[string]any
			_ = json.Unmarshal(step.Args, &args)
			sink(Event{Kind: EventToolCall, Seq: seq, Tool: tool.Name, Args: args})
			inv := InvokeTool(ctx, tool, step.Args, req.ToolTimeout)
			sink(Event{
				Kind: EventToolResult, Seq: seq, Tool: tool.Name, Args: inv.Output.ArgsSummary,
				Status: inv.Status, Rows: inv.Output.Rows, DurationMs: int(inv.Duration.Milliseconds()),
			})
		}
	}
	if f.Err != nil {
		return nil, f.Err
	}
	return &Result{FinalText: f.FinalText, Usage: f.Usage, Model: f.ResultModel}, nil
}

func findTool(tools []Tool, name string) (Tool, bool) {
	for _, t := range tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}
