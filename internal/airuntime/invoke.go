package airuntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Invocation is the outcome of one tool call. When Success is false,
// Output.Text is the message to send back to the model and Err the cause.
// Bytes is len(Output.Text).
type Invocation struct {
	Output   ToolOutput
	Success  bool
	Status   string
	Bytes    int
	Duration time.Duration
	Err      error
}

// InvokeTool runs tool.Handler with a per-call timeout (0 = none), recovers a
// panic, and turns every failure into success:false instead of an error so the
// turn can continue. It returns at the timeout even if the handler ignores ctx.
func InvokeTool(ctx context.Context, tool Tool, args json.RawMessage, timeout time.Duration) Invocation {
	start := time.Now()
	finish := func(inv Invocation) Invocation {
		inv.Bytes = len(inv.Output.Text)
		inv.Duration = time.Since(start)
		return inv
	}
	fail := func(status, msg string, err error) Invocation {
		return finish(Invocation{Output: ToolOutput{Text: msg}, Status: status, Err: err})
	}
	if tool.Handler == nil {
		return fail(ToolStatusFailed, "tool has no handler", fmt.Errorf("tool %q has no handler", tool.Name))
	}

	callCtx, cancel := ctx, context.CancelFunc(func() {})
	if timeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	type outcome struct {
		out ToolOutput
		err error
	}
	done := make(chan outcome, 1) // buffered: a late handler must not block forever
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- outcome{err: &panicError{tool: tool.Name, value: r}}
			}
		}()
		out, err := tool.Handler(callCtx, args)
		done <- outcome{out: out, err: err}
	}()

	select {
	case o := <-done:
		if ctx.Err() != nil {
			return fail(ToolStatusFailed, "tool call canceled", ErrCanceled)
		}
		if errors.Is(o.err, context.DeadlineExceeded) && callCtx.Err() != nil {
			return fail(ToolStatusTimeout, timeoutMessage(timeout), o.err)
		}
		if o.err != nil {
			return fail(ToolStatusFailed, "tool failed: "+safeMessage(o.err), o.err)
		}
		return finish(Invocation{Output: o.out, Success: true, Status: ToolStatusOK})
	case <-callCtx.Done():
		if ctx.Err() != nil {
			return fail(ToolStatusFailed, "tool call canceled", ErrCanceled)
		}
		return fail(ToolStatusTimeout, timeoutMessage(timeout), callCtx.Err())
	}
}

func timeoutMessage(d time.Duration) string {
	return fmt.Sprintf("tool timed out after %s", d)
}

type panicError struct {
	tool  string
	value any
}

func (e *panicError) Error() string { return fmt.Sprintf("tool %q panicked: %v", e.tool, e.value) }

// safeMessage keeps panic details out of the model's context; handler errors
// are written for the model and pass through.
func safeMessage(err error) string {
	var p *panicError
	if errors.As(err, &p) {
		return "internal error"
	}
	return err.Error()
}
