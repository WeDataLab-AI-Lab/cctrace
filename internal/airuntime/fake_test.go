package airuntime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestFakeRuntimeReplaysScript(t *testing.T) {
	calls := 0
	tool := Tool{
		Name: "query_segments",
		Handler: func(ctx context.Context, args json.RawMessage) (ToolOutput, error) {
			calls++
			return ToolOutput{Text: "rows", Rows: 2, ArgsSummary: map[string]any{"limit": 2}}, nil
		},
	}
	usage := Usage{Reported: true, InputTokens: 10, OutputTokens: 4}
	fr := &FakeRuntime{
		Steps: []FakeStep{
			{CallTool: "query_segments", Args: json.RawMessage(`{"limit":2}`)},
			{Event: &Event{Kind: EventUsage, Usage: &usage}},
			{Event: &Event{Kind: EventTextDelta, Text: "hi"}},
		},
		FinalText: `{"summary":"s","items":[]}`,
		Usage:     usage,
	}
	req := RunRequest{Prompt: "p", Tools: []Tool{tool}, ToolTimeout: time.Second}

	var events []Event
	res, err := fr.Run(context.Background(), req, func(e Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	if res.FinalText != fr.FinalText || res.Usage != usage {
		t.Fatalf("result = %+v", res)
	}
	if calls != 1 {
		t.Fatalf("handler calls = %d", calls)
	}
	kinds := []EventKind{EventToolCall, EventToolResult, EventUsage, EventTextDelta}
	if len(events) != len(kinds) {
		t.Fatalf("events = %+v", events)
	}
	for i, k := range kinds {
		if events[i].Kind != k {
			t.Fatalf("event %d kind = %s, want %s", i, events[i].Kind, k)
		}
	}
	if events[0].Seq != 1 || events[1].Seq != 1 || events[0].Tool != "query_segments" {
		t.Fatalf("tool events = %+v %+v", events[0], events[1])
	}
	if events[0].Args["limit"] != float64(2) {
		t.Fatalf("tool_call args = %+v", events[0].Args)
	}
	if events[1].Args["limit"] != 2 {
		t.Fatalf("tool_result args should be the handler's ArgsSummary: %+v", events[1].Args)
	}
	if events[1].Status != ToolStatusOK || events[1].Rows != 2 {
		t.Fatalf("tool_result = %+v", events[1])
	}
	got := fr.Requests()
	if len(got) != 1 || got[0].Prompt != "p" {
		t.Fatalf("requests = %+v", got)
	}
}

func TestFakeRuntimeUnknownTool(t *testing.T) {
	fr := &FakeRuntime{Steps: []FakeStep{{CallTool: "codex_apps"}}}
	_, err := fr.Run(context.Background(), RunRequest{}, func(Event) {})
	if !errors.Is(err, ErrUnexpectedTool) {
		t.Fatalf("err = %v", err)
	}
}

func TestFakeRuntimeScriptedError(t *testing.T) {
	fr := &FakeRuntime{Err: ErrTimeLimit}
	res, err := fr.Run(context.Background(), RunRequest{}, func(Event) {})
	if res != nil || !errors.Is(err, ErrTimeLimit) {
		t.Fatalf("res=%v err=%v", res, err)
	}
}

func TestFakeRuntimeWaitForCancel(t *testing.T) {
	fr := &FakeRuntime{Steps: []FakeStep{{WaitForCancel: true}}, FinalText: "never"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := fr.Run(ctx, RunRequest{}, func(Event) {})
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrCanceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestFakeRuntimeInfoStatus(t *testing.T) {
	fr := &FakeRuntime{
		InfoValue:   Info{Key: "fake", AuthMode: AuthModeNone},
		StatusValue: Status{Configured: true, Available: true},
	}
	var _ Runtime = fr
	if fr.Info().Key != "fake" || !fr.Status(context.Background()).Available {
		t.Fatal("info/status not returned")
	}
}

func TestRunErrorMatchesSentinel(t *testing.T) {
	err := error(&RunError{Code: "time_limit", Message: "8m", Err: ErrTimeLimit})
	if !errors.Is(err, ErrTimeLimit) || err.Error() == "" {
		t.Fatalf("err = %v", err)
	}
}
