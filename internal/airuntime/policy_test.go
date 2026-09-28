package airuntime

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDeadlineOnlyWrapsWhenAWallClockIsSet(t *testing.T) {
	ctx := context.Background()

	plain, cancel := Deadline(ctx, 0)
	cancel()
	if _, ok := plain.Deadline(); ok {
		t.Error("a zero wall clock set a deadline")
	}

	bounded, cancel := Deadline(ctx, time.Minute)
	defer cancel()
	if _, ok := bounded.Deadline(); !ok {
		t.Error("a wall clock did not set a deadline")
	}
}

// The caller's cancel and the turn's own clock running out are different
// failures: one is the user closing the page, the other is the run taking too
// long. Only clauderuntime told them apart correctly; it returned nil when both
// were live instead of reporting a time limit that had not happened.
func TestCtxErrorTellsCancelFromTimeLimit(t *testing.T) {
	live := context.Background()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := CtxError(canceled, live, time.Minute); err == nil || !errors.Is(err, ErrCanceled) {
		t.Errorf("canceled parent: err = %v, want ErrCanceled", err)
	}

	expired, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-expired.Done()
	if err := CtxError(live, expired, time.Minute); err == nil || !errors.Is(err, ErrTimeLimit) {
		t.Errorf("expired run context: err = %v, want ErrTimeLimit", err)
	}

	if err := CtxError(live, live, time.Minute); err != nil {
		t.Errorf("both live: err = %v, want nil", err)
	}
}

func TestBudgetAddsUsageAndEmitsASnapshot(t *testing.T) {
	var events []Event
	b := &Budget{Max: 100}

	if err := b.Add(Usage{Reported: true, InputTokens: 10, CachedInputTokens: 4, OutputTokens: 5}, func(e Event) { events = append(events, e) }); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := b.Add(Usage{Reported: true, InputTokens: 20, OutputTokens: 6}, func(e Event) { events = append(events, e) }); err != nil {
		t.Fatalf("second: %v", err)
	}

	got := b.Usage()
	if got.InputTokens != 30 || got.CachedInputTokens != 4 || got.OutputTokens != 11 || !got.Reported {
		t.Errorf("usage = %+v, want 30/4/11 reported", got)
	}
	if len(events) != 2 {
		t.Fatalf("emitted %d usage events, want 2", len(events))
	}
	// The event carries a copy: a later Add must not change what was emitted.
	if first := events[0].Usage; first == nil || first.InputTokens != 10 {
		t.Errorf("first event usage = %+v, want a 10-token snapshot", first)
	}
}

func TestBudgetStopsTheTurnWhenTheTotalIsPast(t *testing.T) {
	b := &Budget{Max: 10}
	err := b.Add(Usage{Reported: true, InputTokens: 8, OutputTokens: 5}, func(Event) {})
	if err == nil || !errors.Is(err, ErrBudget) {
		t.Fatalf("err = %v, want ErrBudget", err)
	}
	if b2 := (&Budget{}); b2.Add(Usage{InputTokens: 1 << 40}, func(Event) {}) != nil {
		t.Error("a zero Max must not bound the turn")
	}
}

// codexappserver's app-server reports cumulative totals, not per-response
// deltas, so it replaces the usage instead of adding to it.
func TestBudgetSetReplacesTheTotal(t *testing.T) {
	b := &Budget{Max: 100}
	_ = b.Add(Usage{Reported: true, InputTokens: 10, OutputTokens: 1}, func(Event) {})
	if err := b.Set(Usage{Reported: true, InputTokens: 30, OutputTokens: 2}, func(Event) {}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := b.Usage(); got.InputTokens != 30 || got.OutputTokens != 2 {
		t.Errorf("usage = %+v, want the replacement 30/2", got)
	}
}

// One response is one step past the limit however many calls it holds, so a
// parallel batch that overshoots still gets its refusals to the model. Five in
// a row ends the turn: a model told to answer that keeps calling burns time.
func TestToolLimitEndsTheTurnAfterFiveRefusedBatches(t *testing.T) {
	l := &ToolLimit{Max: 2}
	for i := 1; i < MaxCallsPastLimit; i++ {
		if err := l.AfterBatch(true, false); err != nil {
			t.Fatalf("batch %d ended the turn early: %v", i, err)
		}
	}
	err := l.AfterBatch(true, false)
	if err == nil || !errors.Is(err, ErrBudget) {
		t.Fatalf("err = %v, want ErrBudget", err)
	}

	l2 := &ToolLimit{Max: 2}
	_ = l2.AfterBatch(true, false)
	_ = l2.AfterBatch(false, true)
	for i := 1; i < MaxCallsPastLimit; i++ {
		if err := l2.AfterBatch(true, false); err != nil {
			t.Fatalf("a batch that ran a tool did not reset the count: %v", err)
		}
	}
}

func TestToolLimitRefusalNamesTheLimit(t *testing.T) {
	if got := ToolLimitRefusal(3); got != "tool call limit of 3 reached; answer with what you have" {
		t.Errorf("refusal = %q", got)
	}
}
