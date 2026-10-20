package store

import (
	"testing"
	"time"
)

func TestSessionActivityDecisionTable(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	busy := func(changed *time.Time, hb time.Duration) activityLive {
		return activityLive{status: "busy", heartbeatAt: now.Add(hb), statusChangedAt: changed}
	}
	cases := []struct {
		name string
		in   sessionActivityInput
		want string
	}{
		{"old busy and old activity", sessionActivityInput{live: []activityLive{busy(at(-7*time.Hour), -time.Minute)}, lastActivityAt: at(-8 * time.Hour), anyLiveRow: true}, "stale_live"},
		{"old busy and recent activity", sessionActivityInput{live: []activityLive{busy(at(-7*time.Hour), -time.Minute)}, lastActivityAt: at(-time.Hour), anyLiveRow: true}, "running"},
		{"old waiting", sessionActivityInput{live: []activityLive{{status: "waiting", heartbeatAt: now, statusChangedAt: at(-7 * time.Hour)}}, lastActivityAt: at(-8 * time.Hour), anyLiveRow: true}, "stale_live"},
		{"old idle", sessionActivityInput{live: []activityLive{{status: "idle", heartbeatAt: now, statusChangedAt: at(-48 * time.Hour)}}, lastActivityAt: at(-48 * time.Hour), anyLiveRow: true}, "idle"},
		{"stale busy and fresh idle", sessionActivityInput{live: []activityLive{busy(at(-7*time.Hour), -time.Minute), {status: "idle", heartbeatAt: now}}, lastActivityAt: at(-8 * time.Hour), anyLiveRow: true}, "idle"},
		{"stale busy then observed end", sessionActivityInput{live: []activityLive{busy(at(-7*time.Hour), -time.Minute)}, lastActivityAt: at(-8 * time.Hour), endAt: at(-time.Hour), anyLiveRow: true}, "ended"},
		{"null changed is not stale", sessionActivityInput{live: []activityLive{busy(nil, -time.Minute)}, lastActivityAt: at(-8 * time.Hour), anyLiveRow: true}, "running"},
		{"null activity is not stale", sessionActivityInput{live: []activityLive{busy(at(-7*time.Hour), -time.Minute)}, anyLiveRow: true}, "running"},
		{"process I/O unknown status is not running", sessionActivityInput{live: []activityLive{{status: "unknown", heartbeatAt: now}}, anyLiveRow: true}, "inactive"},
		{"latest fresh idle wins over older waiting", sessionActivityInput{live: []activityLive{{status: "waiting", heartbeatAt: now.Add(-time.Minute)}, {status: "idle", heartbeatAt: now}}, anyLiveRow: true}, "idle"},
		{"latest fresh waiting wins over older idle", sessionActivityInput{live: []activityLive{{status: "idle", heartbeatAt: now.Add(-time.Minute)}, {status: "waiting", heartbeatAt: now}}, anyLiveRow: true}, "waiting"},
		{"gone and no live blocks open fallback", sessionActivityInput{live: []activityLive{{status: "gone", heartbeatAt: now}}, anyLiveRow: true, lastTurnStatus: "open", lastActivityAt: at(-time.Minute)}, "recent"},
		{"open fallback", sessionActivityInput{lastTurnStatus: "open", lastActivityAt: at(-5 * time.Minute)}, "running"},
		{"end before resume", sessionActivityInput{lastActivityAt: at(-time.Minute), endAt: at(-time.Hour)}, "recent"},
		{"end without known activity remains unknown", sessionActivityInput{endAt: at(-time.Minute)}, "inactive"},
		{"unanswered question", sessionActivityInput{openQuestions: 1, sameQuestionTurn: true}, "open_question"},
		{"delayed collector", sessionActivityInput{lastActivityAt: at(-time.Hour), collectorHeartbeat: at(-4 * time.Minute)}, "collector_delayed"},
		{"unknown collector is recent", sessionActivityInput{lastActivityAt: at(-time.Hour)}, "recent"},
		{"inactive", sessionActivityInput{lastActivityAt: at(-25 * time.Hour)}, "inactive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := evaluateSessionActivity(now, tc.in)
			if got.category != tc.want {
				t.Fatalf("category=%s, want %s", got.category, tc.want)
			}
		})
	}
}
