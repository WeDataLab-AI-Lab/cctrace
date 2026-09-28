package airuntime

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// ToolCall is one call the model asked for, in the shape every provider's wire
// format reduces to: a name, arguments, and the id the result is echoed back
// under. Mapping a provider's own call shape into this is the adapter's job,
// and so is anything it wants to fix on the way -- the Responses runtime turns
// empty arguments into "{}" there, because the chat/completions one treats the
// same emptiness as a malformed call.
type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

// ToolResult is one finished call. Seq is the call's number within the turn,
// which is what the tool-call limit counts and what the screen shows.
type ToolResult struct {
	Seq  int
	Call ToolCall
	Inv  Invocation
}

// ToolBatch runs the calls from one response. It is shared because every
// runtime that dispatches a batch does the same things in the same order:
// number each call, announce it, refuse it past the limit, refuse it when the
// tool is unknown or the arguments are not JSON, run the rest in parallel, and
// report every result in call order.
//
// Seq runs across batches: the limit counts calls in the turn, not in the
// response. A ToolBatch belongs to one turn and is not safe for concurrent use
// by several turns.
type ToolBatch struct {
	Tools   map[string]Tool
	Limit   *ToolLimit
	Timeout time.Duration
	Sink    func(Event)

	// Seq is the number given to the last call.
	Seq int
}

// Run dispatches every call and waits for them. It announces all of the calls
// before any result: while handlers are running the screen already shows what
// the model asked for. ran reports whether any handler actually ran, and stop
// is set when too many responses in a row had every call refused.
func (b *ToolBatch) Run(ctx context.Context, calls []ToolCall) (results []ToolResult, ran bool, stop *RunError) {
	invs := make([]Invocation, len(calls))
	seqs := make([]int, len(calls))
	failed := func(msg string) Invocation {
		return Invocation{Output: ToolOutput{Text: msg}, Status: ToolStatusFailed}
	}

	var wg sync.WaitGroup
	refused := false
	for i, c := range calls {
		b.Seq++
		seqs[i] = b.Seq

		var args map[string]any
		_ = json.Unmarshal(c.Args, &args)
		b.emit(Event{Kind: EventToolCall, Seq: b.Seq, Tool: c.Name, Args: args})

		tool, known := b.Tools[c.Name]
		switch {
		case b.Limit != nil && b.Limit.Exceeded(b.Seq):
			invs[i] = failed(ToolLimitRefusal(b.Limit.Max))
			refused = true
		case !known:
			invs[i] = failed("unknown tool " + c.Name)
		case !json.Valid(c.Args):
			invs[i] = failed("arguments are not valid JSON")
		default:
			ran = true
			wg.Add(1)
			go func() {
				defer wg.Done()
				invs[i] = InvokeTool(ctx, tool, c.Args, b.Timeout)
			}()
		}
	}

	// One response is one step past the limit however many calls it holds, so
	// a parallel batch that overshoots still gets its refusals to the model.
	if b.Limit != nil {
		stop = b.Limit.AfterBatch(refused, ran)
	}

	// InvokeTool returns at the tool timeout or the run's end even when a
	// handler does not, so this wait is bounded and no emit outlives Run.
	wg.Wait()

	results = make([]ToolResult, len(calls))
	for i, c := range calls {
		inv := invs[i]
		b.emit(Event{
			Kind: EventToolResult, Seq: seqs[i], Tool: c.Name, Args: inv.Output.ArgsSummary,
			Status: inv.Status, Rows: inv.Output.Rows, DurationMs: int(inv.Duration.Milliseconds()),
		})
		results[i] = ToolResult{Seq: seqs[i], Call: c, Inv: inv}
	}
	return results, ran, stop
}

func (b *ToolBatch) emit(e Event) {
	if b.Sink != nil {
		b.Sink(e)
	}
}
