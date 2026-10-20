package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// markerRead is one answer of the marker read.
type markerRead struct {
	ready bool
	err   error
}

// fakeImputedRefresh answers the marker read from reads, one per call, fails
// every refresh with refreshErr, and counts the refreshes and the log lines.
type fakeImputedRefresh struct {
	reads      []markerRead
	refreshErr error
	refreshes  int
	logs       []string
}

func (f *fakeImputedRefresh) refresher() func(context.Context) {
	return claudeImputedRefresher(
		func(context.Context) (bool, error) {
			read := f.reads[0]
			f.reads = f.reads[1:]
			return read.ready, read.err
		},
		func(context.Context) error {
			f.refreshes++
			return f.refreshErr
		},
		func(format string, args ...any) { f.logs = append(f.logs, fmt.Sprintf(format, args...)) })
}

func (f *fakeImputedRefresh) run(times int) {
	refresh := f.refresher()
	for range times {
		refresh(context.Background())
	}
}

var (
	markerAbsent = markerRead{}
	markerReady  = markerRead{ready: true}
)

// Until step 3 of the transition has written its marker, neither the boot
// refresh nor the 30-minute one touches claude_imputed_cost; once it has, they
// run as before.
func TestClaudeImputedRefreshWaitsFor_Step3(t *testing.T) {
	f := &fakeImputedRefresh{reads: []markerRead{markerAbsent, markerAbsent, markerReady, markerReady}}
	refresh := f.refresher()
	refresh(context.Background())
	refresh(context.Background())
	if f.refreshes != 0 {
		t.Fatalf("refreshed %d times without the marker, want 0", f.refreshes)
	}
	refresh(context.Background())
	refresh(context.Background())
	if f.refreshes != 2 {
		t.Errorf("refreshed %d times with the marker, want 2", f.refreshes)
	}
}

// A marker that cannot be read skips the refresh -- which would read the same
// database -- and says why.
func TestClaudeImputedRefreshSkipsOnAMarkerReadError(t *testing.T) {
	f := &fakeImputedRefresh{reads: []markerRead{{err: errors.New("read claude imputed requests marker: connection refused")}}}
	f.run(1)
	if f.refreshes != 0 {
		t.Errorf("refreshed %d times after a failed marker read, want 0", f.refreshes)
	}
	if len(f.logs) != 1 || !strings.Contains(f.logs[0], "connection refused") {
		t.Errorf("logs = %q, want one line naming the read error", f.logs)
	}
}

// A skip lasts until step 3, every 30 minutes for hours: one line when it
// starts, not one per tick. A skip that starts again gets its own line.
func TestClaudeImputedRefreshLogsOncePerSkippingStreak(t *testing.T) {
	f := &fakeImputedRefresh{reads: []markerRead{markerAbsent, markerAbsent, markerAbsent, markerReady, markerAbsent, markerAbsent}}
	refresh := f.refresher()
	for range 3 {
		refresh(context.Background())
	}
	if len(f.logs) != 1 {
		t.Fatalf("logs after three skips = %q, want one line", f.logs)
	}
	for range 3 {
		refresh(context.Background())
	}
	if len(f.logs) != 2 || f.refreshes != 1 {
		t.Errorf("logs = %q after a run and a second streak (refreshes %d); want two lines and one refresh", f.logs, f.refreshes)
	}
}

// The lines a run of ticks leaves: a skip, a read error inside the streak, the
// streak going on without a second skip line, and the refresh's own error once
// the marker is there -- one line each.
func TestClaudeImputedRefreshLogSequence(t *testing.T) {
	f := &fakeImputedRefresh{
		reads:      []markerRead{markerAbsent, {err: errors.New("marker read: connection reset")}, markerAbsent, markerReady},
		refreshErr: errors.New("refresh: deadlock detected"),
	}
	f.run(4)

	if len(f.logs) != 3 ||
		!strings.Contains(f.logs[0], "skipped until the P1t transition") ||
		!strings.Contains(f.logs[1], "connection reset") ||
		!strings.Contains(f.logs[2], "deadlock detected") {
		t.Errorf("logs = %q, want a skip line, the read error and the refresh error, one each", f.logs)
	}
	if f.refreshes != 1 {
		t.Errorf("refreshed %d times, want once: after the marker was read as there", f.refreshes)
	}
}
