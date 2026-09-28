package airuntime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

func echoTool(name string, ran *int32, mu *sync.Mutex) Tool {
	return Tool{
		Name:        name,
		Description: name,
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, args json.RawMessage) (ToolOutput, error) {
			mu.Lock()
			*ran++
			mu.Unlock()
			return ToolOutput{Text: "result of " + name, Rows: 1}, nil
		},
	}
}

// A parallel batch announces every call before any result comes back: the
// screen shows what the model asked for while the handlers are still running.
func TestToolBatchAnnouncesEveryCallBeforeAnyResult(t *testing.T) {
	var count int32
	var mu sync.Mutex
	var events []Event
	b := &ToolBatch{
		Tools: map[string]Tool{"a": echoTool("a", &count, &mu), "b": echoTool("b", &count, &mu)},
		Limit: &ToolLimit{},
		Sink:  func(e Event) { mu.Lock(); events = append(events, e); mu.Unlock() },
	}

	results, ran, stop := b.Run(context.Background(), []ToolCall{
		{ID: "c1", Name: "a", Args: json.RawMessage(`{"x":1}`)},
		{ID: "c2", Name: "b", Args: json.RawMessage(`{}`)},
	})
	if stop != nil {
		t.Fatalf("stop = %v", stop)
	}
	if !ran || count != 2 {
		t.Fatalf("ran = %v, handler calls = %d, want true and 2", ran, count)
	}
	if len(results) != 2 || results[0].Seq != 1 || results[1].Seq != 2 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].Call.ID != "c1" || results[0].Inv.Output.Text != "result of a" {
		t.Errorf("first result = %+v", results[0])
	}

	var kinds []EventKind
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	want := []EventKind{EventToolCall, EventToolCall, EventToolResult, EventToolResult}
	if len(kinds) != len(want) {
		t.Fatalf("events = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("events = %v, want %v", kinds, want)
		}
	}
}

// The sequence number runs across batches, not within one: it is what the
// tool-call limit counts and what the screen numbers the calls by.
func TestToolBatchSequenceRunsAcrossBatches(t *testing.T) {
	var count int32
	var mu sync.Mutex
	b := &ToolBatch{Tools: map[string]Tool{"a": echoTool("a", &count, &mu)}, Limit: &ToolLimit{}, Sink: func(Event) {}}

	_, _, _ = b.Run(context.Background(), []ToolCall{{ID: "1", Name: "a", Args: json.RawMessage(`{}`)}})
	results, _, _ := b.Run(context.Background(), []ToolCall{{ID: "2", Name: "a", Args: json.RawMessage(`{}`)}})
	if len(results) != 1 || results[0].Seq != 2 {
		t.Fatalf("second batch seq = %+v, want 2", results)
	}
}

func TestToolBatchRefusesUnknownAndMalformedCalls(t *testing.T) {
	var count int32
	var mu sync.Mutex
	b := &ToolBatch{Tools: map[string]Tool{"a": echoTool("a", &count, &mu)}, Limit: &ToolLimit{}, Sink: func(Event) {}}

	results, ran, stop := b.Run(context.Background(), []ToolCall{
		{ID: "1", Name: "nope", Args: json.RawMessage(`{}`)},
		{ID: "2", Name: "a", Args: json.RawMessage(`{broken`)},
	})
	if stop != nil {
		t.Fatalf("stop = %v", stop)
	}
	if ran || count != 0 {
		t.Fatalf("a refused batch ran a handler: ran=%v count=%d", ran, count)
	}
	if results[0].Inv.Output.Text != "unknown tool nope" || results[0].Inv.Status != ToolStatusFailed {
		t.Errorf("unknown tool result = %+v", results[0].Inv)
	}
	if results[1].Inv.Output.Text != "arguments are not valid JSON" {
		t.Errorf("malformed args result = %+v", results[1].Inv)
	}
}

// Past the limit every call is refused with the same answer, and enough
// refused batches in a row end the turn.
func TestToolBatchRefusesPastTheLimitAndEventuallyStops(t *testing.T) {
	var count int32
	var mu sync.Mutex
	b := &ToolBatch{
		Tools: map[string]Tool{"a": echoTool("a", &count, &mu)},
		Limit: &ToolLimit{Max: 1},
		Sink:  func(Event) {},
	}

	if _, ran, _ := b.Run(context.Background(), []ToolCall{{ID: "1", Name: "a", Args: json.RawMessage(`{}`)}}); !ran {
		t.Fatal("the call within the limit did not run")
	}
	var stop *RunError
	for i := 0; i < MaxCallsPastLimit; i++ {
		var results []ToolResult
		results, _, stop = b.Run(context.Background(), []ToolCall{{ID: "x", Name: "a", Args: json.RawMessage(`{}`)}})
		if results[0].Inv.Output.Text != ToolLimitRefusal(1) {
			t.Fatalf("refusal = %q", results[0].Inv.Output.Text)
		}
	}
	if stop == nil || !errors.Is(stop, ErrBudget) {
		t.Fatalf("stop = %v, want ErrBudget", stop)
	}
	if count != 1 {
		t.Errorf("handler ran %d times past the limit", count)
	}
}

func TestToolBatchHonoursTheToolTimeout(t *testing.T) {
	slow := Tool{
		Name:        "slow",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, args json.RawMessage) (ToolOutput, error) {
			<-ctx.Done()
			return ToolOutput{}, ctx.Err()
		},
	}
	b := &ToolBatch{Tools: map[string]Tool{"slow": slow}, Limit: &ToolLimit{}, Timeout: 20 * time.Millisecond, Sink: func(Event) {}}

	start := time.Now()
	results, _, _ := b.Run(context.Background(), []ToolCall{{ID: "1", Name: "slow", Args: json.RawMessage(`{}`)}})
	if time.Since(start) > 2*time.Second {
		t.Fatal("the batch outlived the tool timeout")
	}
	if results[0].Inv.Success {
		t.Errorf("a timed-out call reported success: %+v", results[0].Inv)
	}
}
