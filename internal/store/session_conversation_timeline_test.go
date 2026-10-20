package store

import (
	"testing"
	"time"
)

func TestBuildConversationTimelineMergesCloseCallsPerLane(t *testing.T) {
	at := func(sec float64) time.Time {
		return time.Unix(1_800_000_000, 0).Add(time.Duration(sec * float64(time.Second)))
	}
	got := buildConversationTimeline(
		[]time.Time{at(0), at(100)},
		[]time.Time{at(5), at(5.2), at(9)},
		[]timelineSpan{
			{start: at(1), end: at(2)},
			{start: at(2.5), end: at(4)},              // within 1s of the previous main call: merged
			{start: at(2), end: at(3), aside: true},   // other lane: kept apart
			{start: at(20), end: at(21)},              // far: its own bar
			{start: at(20.5), end: at(22), err: true}, // an error stays visible
		},
	)
	if !got.Base.Equal(at(0)) {
		t.Fatalf("base=%v", got.Base)
	}
	want := [][3]int64{{1000, 3000, 0}, {2000, 1000, 1}, {20000, 1000, 0}, {20500, 1500, 2}}
	if len(got.Spans) != len(want) {
		t.Fatalf("spans=%v want %v", got.Spans, want)
	}
	for i := range want {
		if got.Spans[i] != want[i] {
			t.Fatalf("spans=%v want %v", got.Spans, want)
		}
	}
	if len(got.Replies) != 2 || got.Replies[0] != 5000 || got.Replies[1] != 9000 {
		t.Fatalf("replies=%v", got.Replies)
	}
	if len(got.Requests) != 2 {
		t.Fatalf("requests=%v", got.Requests)
	}
}

func TestBuildConversationTimelineEmpty(t *testing.T) {
	got := buildConversationTimeline(nil, nil, nil)
	if got.Requests == nil || got.Spans == nil || got.Replies == nil || !got.Base.IsZero() {
		t.Fatalf("empty timeline must encode as empty arrays: %+v", got)
	}
}
