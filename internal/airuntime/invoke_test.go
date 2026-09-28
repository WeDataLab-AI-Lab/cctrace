package airuntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func toolWith(h func(ctx context.Context, args json.RawMessage) (ToolOutput, error)) Tool {
	return Tool{Name: "t", Handler: h}
}

func TestInvokeToolOK(t *testing.T) {
	tool := toolWith(func(ctx context.Context, args json.RawMessage) (ToolOutput, error) {
		if string(args) != `{"limit":3}` {
			t.Errorf("args = %s", args)
		}
		return ToolOutput{Text: "héllo", Rows: 3}, nil
	})
	got := InvokeTool(context.Background(), tool, json.RawMessage(`{"limit":3}`), time.Second)
	if !got.Success || got.Status != ToolStatusOK || got.Err != nil {
		t.Fatalf("got %+v", got)
	}
	if got.Output.Rows != 3 || got.Output.Text != "héllo" {
		t.Fatalf("output = %+v", got.Output)
	}
	if got.Bytes != len("héllo") {
		t.Fatalf("bytes = %d, want %d", got.Bytes, len("héllo"))
	}
}

func TestInvokeToolHandlerError(t *testing.T) {
	tool := toolWith(func(ctx context.Context, args json.RawMessage) (ToolOutput, error) {
		return ToolOutput{}, errors.New("bad args")
	})
	got := InvokeTool(context.Background(), tool, nil, time.Second)
	if got.Success || got.Status != ToolStatusFailed || got.Err == nil {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(got.Output.Text, "bad args") || got.Bytes != len(got.Output.Text) {
		t.Fatalf("output = %+v bytes=%d", got.Output, got.Bytes)
	}
}

func TestInvokeToolTimeout(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	tool := toolWith(func(ctx context.Context, args json.RawMessage) (ToolOutput, error) {
		<-release // ignores ctx on purpose: InvokeTool must not wait for it
		return ToolOutput{Text: "late"}, nil
	})
	start := time.Now()
	got := InvokeTool(context.Background(), tool, nil, 20*time.Millisecond)
	if time.Since(start) > time.Second {
		t.Fatalf("InvokeTool waited for a handler that ignores ctx")
	}
	if got.Success || got.Status != ToolStatusTimeout {
		t.Fatalf("got %+v", got)
	}
	if got.Output.Text == "" || got.Bytes != len(got.Output.Text) {
		t.Fatalf("timeout needs a message for the model: %+v", got)
	}
}

func TestInvokeToolPanic(t *testing.T) {
	tool := toolWith(func(ctx context.Context, args json.RawMessage) (ToolOutput, error) {
		panic("boom")
	})
	got := InvokeTool(context.Background(), tool, nil, time.Second)
	if got.Success || got.Status != ToolStatusFailed || got.Err == nil {
		t.Fatalf("got %+v", got)
	}
	if strings.Contains(got.Output.Text, "boom") {
		t.Fatalf("panic value leaked to model: %q", got.Output.Text)
	}
}

func TestInvokeToolParentCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	seen := make(chan error, 1)
	tool := toolWith(func(ctx context.Context, args json.RawMessage) (ToolOutput, error) {
		cancel()
		<-ctx.Done()
		seen <- ctx.Err()
		return ToolOutput{}, ctx.Err()
	})
	got := InvokeTool(ctx, tool, nil, time.Minute)
	if err := <-seen; !errors.Is(err, context.Canceled) {
		t.Fatalf("handler ctx err = %v", err)
	}
	if got.Success || got.Status != ToolStatusFailed || !errors.Is(got.Err, ErrCanceled) {
		t.Fatalf("got %+v", got)
	}
}

func TestInvokeToolNoTimeout(t *testing.T) {
	tool := toolWith(func(ctx context.Context, args json.RawMessage) (ToolOutput, error) {
		if _, ok := ctx.Deadline(); ok {
			t.Errorf("zero timeout must not set a deadline")
		}
		return ToolOutput{Text: "ok"}, nil
	})
	if got := InvokeTool(context.Background(), tool, nil, 0); !got.Success {
		t.Fatalf("got %+v", got)
	}
}

func TestInvokeToolNilHandler(t *testing.T) {
	got := InvokeTool(context.Background(), Tool{Name: "x"}, nil, time.Second)
	if got.Success || got.Status != ToolStatusFailed {
		t.Fatalf("got %+v", got)
	}
}
