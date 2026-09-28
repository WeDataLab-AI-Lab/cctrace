package airuntime

import (
	"context"
	"fmt"
	"time"
)

// The turn policy: the budget, the tool-call limit, the wall clock and the
// cancel decision. Every runtime needs all four and none of them is provider
// business, so they live here rather than in four copies that drift.
//
// What stays with each runtime is the wire mapping: which response field is
// input, whether cache writes count as input, whether usage arrives as a delta
// or a running total. Those genuinely differ, so Budget takes numbers that are
// already mapped.

// MaxCallsPastLimit is how many responses in a row with calls refused past the
// tool-call limit end the turn: a model told to answer that keeps calling is
// only burning time.
const MaxCallsPastLimit = 5

// Deadline bounds a run by its wall clock. A zero wall clock leaves ctx alone
// and returns a cancel that does nothing, so callers can always defer it.
func Deadline(ctx context.Context, wallClock time.Duration) (context.Context, context.CancelFunc) {
	if wallClock <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, wallClock)
}

// CtxError tells the caller's cancel from the turn's own clock running out and
// returns nil while both are live. The two are different failures -- someone
// closed the page, or the run took too long -- and a runtime that reports the
// second for the first sends the reader after the wrong thing.
func CtxError(ctx, runCtx context.Context, wallClock time.Duration) *RunError {
	switch {
	case ctx.Err() != nil:
		return NewRunError("canceled", "run canceled", ErrCanceled)
	case runCtx.Err() != nil:
		return NewRunError("time_limit", fmt.Sprintf("turn exceeded %s", wallClock), ErrTimeLimit)
	}
	return nil
}

// Budget accumulates a turn's usage and stops it once MaxTotalTokens is past.
// Max of zero does not bound the turn.
type Budget struct {
	Max   int64
	usage Usage
}

// Add takes one response's usage and adds it to the turn's. Set replaces the
// total instead, for a provider that reports a running total rather than a
// delta. Both emit a snapshot and then check the budget.
func (b *Budget) Add(delta Usage, sink func(Event)) *RunError {
	if delta.Reported {
		b.usage.Reported = true
	}
	b.usage.InputTokens += delta.InputTokens
	b.usage.CachedInputTokens += delta.CachedInputTokens
	b.usage.OutputTokens += delta.OutputTokens
	return b.settle(sink)
}

func (b *Budget) Set(total Usage, sink func(Event)) *RunError {
	b.usage = total
	return b.settle(sink)
}

// Usage is the turn's usage so far.
func (b *Budget) Usage() Usage { return b.usage }

// settle emits the snapshot and checks the budget. The check runs after the
// response has arrived, so a final answer whose response crosses the budget is
// dropped: the turn stops at the budget either way.
func (b *Budget) settle(sink func(Event)) *RunError {
	u := b.usage
	if sink != nil {
		sink(Event{Kind: EventUsage, Usage: &u})
	}
	if b.Max > 0 && u.InputTokens+u.OutputTokens > b.Max {
		return NewRunError("budget_exceeded", fmt.Sprintf("turn used more than %d tokens", b.Max), ErrBudget)
	}
	return nil
}

// ToolLimit counts how many responses in a row had every call refused past
// MaxToolCalls. Max of zero does not bound the calls.
type ToolLimit struct {
	Max       int
	pastLimit int
}

// Exceeded reports whether the call at this sequence number is past the limit.
func (l *ToolLimit) Exceeded(seq int) bool { return l.Max > 0 && seq > l.Max }

// AfterBatch records one response's outcome. One response is one step past the
// limit however many calls it holds, so a parallel batch that overshoots still
// gets its refusals to the model; a batch that ran a tool clears the count.
func (l *ToolLimit) AfterBatch(refused, ran bool) *RunError {
	switch {
	case refused:
		if l.pastLimit++; l.pastLimit >= MaxCallsPastLimit {
			return NewRunError("tool_call_limit", fmt.Sprintf("model kept calling tools past the limit of %d", l.Max), ErrBudget)
		}
	case ran:
		l.pastLimit = 0
	}
	return nil
}

// ToolLimitRefusal is what a call past the limit sends back to the model.
func ToolLimitRefusal(max int) string {
	return fmt.Sprintf("tool call limit of %d reached; answer with what you have", max)
}
