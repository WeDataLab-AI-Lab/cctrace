package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"cctrace/internal/store"
)

// stepOutcome is what one call of a fake step returns.
type stepOutcome struct {
	progressed bool
	err        error
}

// fakeSteps builds steps that record each call in calls and answer from their
// script in order. Past the end of its script a step succeeds having committed
// its work, (true, nil).
func fakeSteps(calls *[]string, scripts map[string][]stepOutcome, markers ...string) []store.RequestSetMigrationStep {
	steps := make([]store.RequestSetMigrationStep, 0, len(markers))
	for _, marker := range markers {
		steps = append(steps, store.RequestSetMigrationStep{Marker: marker, Run: func(context.Context) (bool, error) {
			*calls = append(*calls, marker)
			script := scripts[marker]
			if len(script) == 0 {
				return true, nil
			}
			scripts[marker] = script[1:]
			return script[0].progressed, script[0].err
		}})
	}
	return steps
}

// testClock moves only when the runner waits: every wait returns at once,
// after moving the time on by the wait, and is recorded.
type testClock struct {
	t     time.Time
	waits []time.Duration
}

func (c *testClock) now() time.Time { return c.t }

func (c *testClock) after(d time.Duration) <-chan time.Time {
	c.waits = append(c.waits, d)
	c.t = c.t.Add(d)
	ch := make(chan time.Time, 1)
	ch <- c.t
	return ch
}

func collectLogs(logs *[]string) func(string, ...any) {
	return func(format string, args ...any) { *logs = append(*logs, fmt.Sprintf(format, args...)) }
}

func countLogs(logs []string, substr string) int {
	n := 0
	for _, l := range logs {
		if strings.Contains(l, substr) {
			n++
		}
	}
	return n
}

// The steps run in order, each once when it succeeds, with no wait between
// them. A step that did its work gets a completion line; one that only found
// its marker gets none.
func TestRequestSetMigrationRunsTheStepsInOrder(t *testing.T) {
	var calls, logs []string
	clock := &testClock{}
	runRequestSetMigration(context.Background(),
		fakeSteps(&calls, map[string][]stepOutcome{"two": {{progressed: false}}}, "one", "two", "three"),
		clock.now, clock.after, collectLogs(&logs))

	if want := []string{"one", "two", "three"}; !slices.Equal(calls, want) {
		t.Errorf("calls = %v, want %v", calls, want)
	}
	if len(clock.waits) != 0 {
		t.Errorf("waits = %v, want none", clock.waits)
	}
	want := []string{
		"[request-set] one done in 0s since this process first ran it",
		"[request-set] three done in 0s since this process first ran it",
	}
	if !slices.Equal(logs, want) {
		t.Errorf("logs = %q, want %q", logs, want)
	}
}

// Every boot after the transition runs every step, and each finds its marker.
// That says nothing worth a line.
func TestRequestSetMigrationBootAfterTheTransitionLogsNothing(t *testing.T) {
	var calls, logs []string
	marked := []stepOutcome{{progressed: false}}
	clock := &testClock{}
	runRequestSetMigration(context.Background(),
		fakeSteps(&calls, map[string][]stepOutcome{"one": marked, "two": marked, "three": marked}, "one", "two", "three"),
		clock.now, clock.after, collectLogs(&logs))

	if want := []string{"one", "two", "three"}; !slices.Equal(calls, want) {
		t.Errorf("calls = %v, want %v", calls, want)
	}
	if len(logs) != 0 {
		t.Errorf("logs = %q, want none", logs)
	}
}

// A failing step is retried alone, the wait doubling from 5s to the 5-minute
// cap, and no later step runs before it succeeds. The same failure is logged
// once however often it repeats, and the recovery once. The completion line
// times the step from this process's first try, waits included.
func TestRequestSetMigrationRetriesTheFailingStepAlone(t *testing.T) {
	const failures = 9
	stuck := errors.New("lock message_id window: 55P03")
	script := make([]stepOutcome, failures)
	for i := range script {
		script[i] = stepOutcome{err: stuck}
	}
	var calls, logs []string
	clock := &testClock{}
	runRequestSetMigration(context.Background(),
		fakeSteps(&calls, map[string][]stepOutcome{"two": script}, "one", "two", "three"),
		clock.now, clock.after, collectLogs(&logs))

	want := []string{"one"}
	for range failures + 1 {
		want = append(want, "two")
	}
	want = append(want, "three")
	if !slices.Equal(calls, want) {
		t.Errorf("calls = %v, want %v -- a later step ran before the failing one succeeded", calls, want)
	}
	wantWaits := []time.Duration{
		5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second,
		160 * time.Second, 5 * time.Minute, 5 * time.Minute, 5 * time.Minute,
	}
	if !slices.Equal(clock.waits, wantWaits) {
		t.Errorf("waits = %v, want %v", clock.waits, wantWaits)
	}
	if n := countLogs(logs, "failed"); n != 1 {
		t.Errorf("logged the same failure %d times, want once: %q", n, logs)
	}
	if n := countLogs(logs, "two recovered"); n != 1 {
		t.Errorf("logged the recovery %d times, want once: %q", n, logs)
	}
	// 5+10+20+40+80+160+300+300+300 seconds.
	if n := countLogs(logs, "[request-set] two done in 20m15s since this process first ran it"); n != 1 {
		t.Errorf("logs = %q, want one completion line for two timed at 20m15s", logs)
	}
	if n := countLogs(logs, "done"); n != 3 {
		t.Errorf("logged %d completed steps, want 3: %q", n, logs)
	}
}

// A step that committed part of its work before failing -- a message_id window,
// a batch -- is getting through, not stuck: the failure count starts over and
// the next try comes 5s later again.
func TestRequestSetMigrationStartsTheCountOverAfterProgress(t *testing.T) {
	held := errors.New("held row")
	var calls []string
	clock := &testClock{}
	runRequestSetMigration(context.Background(),
		fakeSteps(&calls, map[string][]stepOutcome{"walk": {
			{err: held}, {err: held}, {progressed: true, err: held}, {err: held}, {progressed: true, err: held},
		}}, "walk"),
		clock.now, clock.after, func(string, ...any) {})

	want := []time.Duration{5 * time.Second, 10 * time.Second, 5 * time.Second, 10 * time.Second, 5 * time.Second}
	if !slices.Equal(clock.waits, want) {
		t.Errorf("waits = %v, want %v", clock.waits, want)
	}
}

// A failure is logged when its message differs from the one before, not on
// every try. The recovery is logged whether or not the call that succeeded did
// any work -- here it finds the step already done.
func TestRequestSetMigrationLogsOnlyAChangedFailure(t *testing.T) {
	a, b := errors.New("failure a"), errors.New("failure b")
	var calls, logs []string
	clock := &testClock{}
	runRequestSetMigration(context.Background(),
		fakeSteps(&calls, map[string][]stepOutcome{"step": {
			{err: a}, {err: a}, {err: b}, {err: b}, {err: b}, {err: a}, {progressed: false},
		}}, "step"),
		clock.now, clock.after, collectLogs(&logs))

	var failed []string
	for _, l := range logs {
		if strings.Contains(l, "failed") {
			failed = append(failed, l)
		}
	}
	if len(failed) != 3 || !strings.Contains(failed[0], "failure a") ||
		!strings.Contains(failed[1], "failure b") || !strings.Contains(failed[2], "failure a") {
		t.Errorf("failure lines = %q, want a, b, a", failed)
	}
	if n := countLogs(logs, "recovered"); n != 1 {
		t.Errorf("logged the recovery %d times, want once: %q", n, logs)
	}
	if n := countLogs(logs, "done"); n != 0 {
		t.Errorf("logged a completion for a step this process did not do: %q", logs)
	}
}

// Shutdown cancels the managed repairs and waits for them, so the runner has to
// return at once whether it is waiting out a backoff or inside a step, and run
// no later step on the way out.
func TestRequestSetMigrationReturnsWhenCancelled(t *testing.T) {
	t.Run("mid-wait", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var calls []string
		waiting := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			runRequestSetMigration(ctx,
				fakeSteps(&calls, map[string][]stepOutcome{"one": {{err: errors.New("down")}}}, "one", "two"),
				time.Now,
				func(time.Duration) <-chan time.Time {
					close(waiting)
					return nil // never fires
				}, func(string, ...any) {})
		}()
		<-waiting
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("the runner did not return while waiting out a backoff")
		}
		if !slices.Equal(calls, []string{"one"}) {
			t.Errorf("calls = %v, want only the failing step's one try", calls)
		}
	})

	t.Run("mid-step", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var ran, logs []string
		steps := []store.RequestSetMigrationStep{
			{Marker: "one", Run: func(ctx context.Context) (bool, error) {
				ran = append(ran, "one")
				cancel()
				<-ctx.Done()
				return true, ctx.Err()
			}},
			{Marker: "two", Run: func(context.Context) (bool, error) {
				ran = append(ran, "two")
				return true, nil
			}},
		}
		clock := &testClock{}
		runRequestSetMigration(ctx, steps, clock.now, clock.after, collectLogs(&logs))
		if !slices.Equal(ran, []string{"one"}) || len(clock.waits) != 0 || len(logs) != 0 {
			t.Errorf("after a cancel inside a step: ran %v, waited %v, logged %q; want one, no wait, no line",
				ran, clock.waits, logs)
		}
	})
}
